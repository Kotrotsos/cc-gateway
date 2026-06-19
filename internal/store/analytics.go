package store

import (
	"fmt"
	"strings"
)

// AnalyticsQuery scopes the analytics aggregates.
type AnalyticsQuery struct {
	Since     int64 // unix ms, 0 = open
	Until     int64 // unix ms, 0 = open
	SessionID int64 // 0 = all sessions
	BucketMs  int64 // time-series bucket width, 0 = 1h
}

// Totals are the headline analytics numbers.
type Totals struct {
	Sessions   int     `json:"sessions"`
	Requests   int     `json:"requests"`
	InTokens   int64   `json:"in_tokens"`
	OutTokens  int64   `json:"out_tokens"`
	CacheRead  int64   `json:"cache_read"`
	CacheWrite int64   `json:"cache_write"`
	EstCost    float64 `json:"est_cost"`
	ToolCalls  int     `json:"tool_calls"`
	Errors     int     `json:"errors"`
	AvgLatency float64 `json:"avg_latency_ms"`
}

// NameCount is a labelled count (tool name, model, stop reason) with tokens.
type NameCount struct {
	Name   string `json:"name"`
	Count  int    `json:"count"`
	Errors int    `json:"errors,omitempty"`
	Tokens int64  `json:"tokens,omitempty"`
}

// TimePoint is one bucket in a time series.
type TimePoint struct {
	Bucket    int64 `json:"bucket"`
	Requests  int   `json:"requests"`
	InTokens  int64 `json:"in_tokens"`
	OutTokens int64 `json:"out_tokens"`
	CacheRead int64 `json:"cache_read"`
}

// Analytics is the full payload for the analytics tab.
type Analytics struct {
	Totals     Totals      `json:"totals"`
	Tools      []NameCount `json:"tools"`
	Models     []NameCount `json:"models"`
	StopReason []NameCount `json:"stop_reason"`
	Series     []TimePoint `json:"series"`
	CacheHit   float64     `json:"cache_hit_rate"`
}

func (q AnalyticsQuery) reqWhere() (string, []any) {
	var where []string
	var args []any
	if q.Since > 0 {
		where = append(where, "ts_start >= ?")
		args = append(args, q.Since)
	}
	if q.Until > 0 {
		where = append(where, "ts_start <= ?")
		args = append(args, q.Until)
	}
	if q.SessionID > 0 {
		where = append(where, "session_id = ?")
		args = append(args, q.SessionID)
	}
	if len(where) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(where, " AND "), args
}

// Analytics computes all aggregates for the analytics tab in one call.
func (s *Store) Analytics(q AnalyticsQuery) (*Analytics, error) {
	if q.BucketMs <= 0 {
		q.BucketMs = 3600_000 // 1 hour
	}
	rw, args := q.reqWhere()
	a := &Analytics{Tools: []NameCount{}, Models: []NameCount{}, StopReason: []NameCount{}, Series: []TimePoint{}}

	// Totals from requests.
	row := s.db.QueryRow(`SELECT
		COUNT(DISTINCT session_id), COUNT(*), COALESCE(SUM(in_tokens),0), COALESCE(SUM(out_tokens),0),
		COALESCE(SUM(cache_read),0), COALESCE(SUM(cache_write),0), COALESCE(SUM(est_cost),0),
		COALESCE(SUM(num_tools),0), COALESCE(SUM(CASE WHEN status>=400 OR error<>'' THEN 1 ELSE 0 END),0),
		COALESCE(AVG(duration_ms),0)
		FROM requests`+rw, args...)
	t := &a.Totals
	if err := row.Scan(&t.Sessions, &t.Requests, &t.InTokens, &t.OutTokens, &t.CacheRead, &t.CacheWrite,
		&t.EstCost, &t.ToolCalls, &t.Errors, &t.AvgLatency); err != nil {
		return nil, err
	}
	if t.InTokens+t.CacheRead > 0 {
		a.CacheHit = float64(t.CacheRead) / float64(t.InTokens+t.CacheRead)
	}

	// Tool histogram (content_blocks scoped via a join to requests for time/session).
	bw, bargs := q.reqWhere()
	bw = strings.ReplaceAll(bw, "ts_start", "r.ts_start")
	bw = strings.ReplaceAll(bw, "session_id", "r.session_id")
	toolRows, err := s.db.Query(`SELECT b.tool_name, COUNT(*), COALESCE(SUM(b.is_error),0)
		FROM content_blocks b JOIN requests r ON r.id = b.request_id
		WHERE b.type='tool_use'`+andWhere(bw)+` GROUP BY b.tool_name ORDER BY COUNT(*) DESC`, bargs...)
	if err != nil {
		return nil, err
	}
	for toolRows.Next() {
		var n NameCount
		if err := toolRows.Scan(&n.Name, &n.Count, &n.Errors); err != nil {
			toolRows.Close()
			return nil, err
		}
		a.Tools = append(a.Tools, n)
	}
	toolRows.Close()

	// Model distribution.
	modelRows, err := s.db.Query(`SELECT COALESCE(NULLIF(model,''),'(unknown)'), COUNT(*),
		COALESCE(SUM(in_tokens+out_tokens),0) FROM requests`+rw+` GROUP BY model ORDER BY COUNT(*) DESC`, args...)
	if err != nil {
		return nil, err
	}
	for modelRows.Next() {
		var n NameCount
		if err := modelRows.Scan(&n.Name, &n.Count, &n.Tokens); err != nil {
			modelRows.Close()
			return nil, err
		}
		a.Models = append(a.Models, n)
	}
	modelRows.Close()

	// Stop reason breakdown.
	srRows, err := s.db.Query(`SELECT COALESCE(NULLIF(stop_reason,''),'(none)'), COUNT(*) FROM requests`+rw+
		` GROUP BY stop_reason ORDER BY COUNT(*) DESC`, args...)
	if err != nil {
		return nil, err
	}
	for srRows.Next() {
		var n NameCount
		if err := srRows.Scan(&n.Name, &n.Count); err != nil {
			srRows.Close()
			return nil, err
		}
		a.StopReason = append(a.StopReason, n)
	}
	srRows.Close()

	// Time series, bucketed.
	seriesArgs := append([]any{q.BucketMs, q.BucketMs}, args...)
	seriesRows, err := s.db.Query(fmt.Sprintf(`SELECT (ts_start/?)*? AS bucket, COUNT(*),
		COALESCE(SUM(in_tokens),0), COALESCE(SUM(out_tokens),0), COALESCE(SUM(cache_read),0)
		FROM requests%s GROUP BY bucket ORDER BY bucket`, rw), seriesArgs...)
	if err != nil {
		return nil, err
	}
	for seriesRows.Next() {
		var p TimePoint
		if err := seriesRows.Scan(&p.Bucket, &p.Requests, &p.InTokens, &p.OutTokens, &p.CacheRead); err != nil {
			seriesRows.Close()
			return nil, err
		}
		a.Series = append(a.Series, p)
	}
	seriesRows.Close()

	return a, nil
}

// andWhere converts a leading " WHERE ..." clause into " AND ..." for appending
// after an existing WHERE, or returns "" when empty.
func andWhere(w string) string {
	w = strings.TrimSpace(w)
	if w == "" {
		return ""
	}
	return " AND " + strings.TrimPrefix(w, "WHERE ")
}

// Models lists the distinct models seen, for the filter dropdown.
func (s *Store) Models() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT model FROM sessions WHERE model <> '' ORDER BY model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

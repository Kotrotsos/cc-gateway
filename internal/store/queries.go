package store

import (
	"database/sql"
	"fmt"
	"strings"

	"cc-gateway/internal/parse"
)

// SessionSummary is a row in the sessions list.
type SessionSummary struct {
	ID          int64   `json:"id"`
	SessionKey  string  `json:"session_key"`
	FirstSeen   int64   `json:"first_seen"`
	LastSeen    int64   `json:"last_seen"`
	Model       string  `json:"model"`
	Cwd         string  `json:"cwd"`
	GitBranch   string  `json:"git_branch"`
	CliVersion  string  `json:"cli_version"`
	NumRequests int     `json:"num_requests"`
	InTokens    int     `json:"in_tokens"`
	OutTokens   int     `json:"out_tokens"`
	CacheRead   int     `json:"cache_read"`
	CacheWrite  int     `json:"cache_write"`
	EstCost     float64 `json:"est_cost"`
	ErrorCount  int     `json:"error_count"`
}

// SessionQuery filters the sessions list.
type SessionQuery struct {
	Model     string
	Tool      string
	HasErrors bool
	Q         string
	Limit     int
	Offset    int
}

// ListSessions returns sessions newest-first, applying the given filters.
func (s *Store) ListSessions(q SessionQuery) ([]SessionSummary, error) {
	var where []string
	var args []any
	if q.Model != "" {
		where = append(where, "s.model = ?")
		args = append(args, q.Model)
	}
	if q.HasErrors {
		where = append(where, "s.error_count > 0")
	}
	if q.Tool != "" {
		where = append(where, "s.id IN (SELECT session_id FROM content_blocks WHERE type='tool_use' AND tool_name = ?)")
		args = append(args, q.Tool)
	}
	if q.Q != "" {
		where = append(where, "s.id IN (SELECT session_id FROM blocks_fts WHERE blocks_fts MATCH ?)")
		args = append(args, ftsQuery(q.Q))
	}
	sqlStr := "SELECT s.id, s.session_key, s.first_seen, s.last_seen, s.model, s.cwd, s.git_branch, s.cli_version, " +
		"s.num_requests, s.in_tokens, s.out_tokens, s.cache_read, s.cache_write, s.est_cost, s.error_count FROM sessions s"
	if len(where) > 0 {
		sqlStr += " WHERE " + strings.Join(where, " AND ")
	}
	sqlStr += " ORDER BY s.last_seen DESC"
	if q.Limit > 0 {
		sqlStr += fmt.Sprintf(" LIMIT %d OFFSET %d", q.Limit, q.Offset)
	}

	rows, err := s.db.Query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionSummary{}
	for rows.Next() {
		var x SessionSummary
		if err := rows.Scan(&x.ID, &x.SessionKey, &x.FirstSeen, &x.LastSeen, &x.Model, &x.Cwd, &x.GitBranch,
			&x.CliVersion, &x.NumRequests, &x.InTokens, &x.OutTokens, &x.CacheRead, &x.CacheWrite, &x.EstCost, &x.ErrorCount); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) getSessionSummary(id int64) (SessionSummary, error) {
	var x SessionSummary
	err := s.db.QueryRow(`SELECT id, session_key, first_seen, last_seen, model, cwd, git_branch, cli_version,
		num_requests, in_tokens, out_tokens, cache_read, cache_write, est_cost, error_count FROM sessions WHERE id = ?`, id).
		Scan(&x.ID, &x.SessionKey, &x.FirstSeen, &x.LastSeen, &x.Model, &x.Cwd, &x.GitBranch, &x.CliVersion,
			&x.NumRequests, &x.InTokens, &x.OutTokens, &x.CacheRead, &x.CacheWrite, &x.EstCost, &x.ErrorCount)
	return x, err
}

// ToolUseRef is a tool call the model made within a request.
type ToolUseRef struct {
	ToolUseID string `json:"tool_use_id"`
	Name      string `json:"name"`
	Input     string `json:"input_preview"`
}

// RequestSummary is one node in a session's trace.
type RequestSummary struct {
	ID               int64        `json:"id"`
	Seq              int          `json:"seq"`
	ThreadKey        string       `json:"thread_key"`
	TsStart          int64        `json:"ts_start"`
	TsEnd            int64        `json:"ts_end"`
	DurationMs       int64        `json:"duration_ms"`
	Status           int          `json:"status"`
	Model            string       `json:"model"`
	InTokens         int          `json:"in_tokens"`
	OutTokens        int          `json:"out_tokens"`
	CacheRead        int          `json:"cache_read"`
	CacheWrite       int          `json:"cache_write"`
	EstCost          float64      `json:"est_cost"`
	StopReason       string       `json:"stop_reason"`
	NumTools         int          `json:"num_tools"`
	Error            string       `json:"error"`
	AssistantPreview string       `json:"assistant_preview"`
	ToolUses         []ToolUseRef `json:"tool_uses"`
}

// Span is a reconstructed tool-call span linking the request that issued a
// tool_use to the request that returned its tool_result.
type Span struct {
	ToolUseID    string `json:"tool_use_id"`
	Name         string `json:"name"`
	CallRequest  int64  `json:"call_request_id"`
	ResultReq    int64  `json:"result_request_id"`
	Started      int64  `json:"started"`
	Ended        int64  `json:"ended"`
	DurationMs   int64  `json:"duration_ms"`
	IsError      bool   `json:"is_error"`
	HasResult    bool   `json:"has_result"`
	InputPreview string `json:"input_preview"`
}

// SessionDetail is the full payload backing the trace view.
type SessionDetail struct {
	Session  SessionSummary   `json:"session"`
	Requests []RequestSummary `json:"requests"`
	Spans    []Span           `json:"spans"`
}

// GetSession returns a session with its ordered requests and reconstructed tool
// spans. Previews and tool references come from content_blocks, so no blobs are
// unzipped here.
func (s *Store) GetSession(id int64) (*SessionDetail, error) {
	sum, err := s.getSessionSummary(id)
	if err != nil {
		return nil, err
	}
	d := &SessionDetail{Session: sum, Requests: []RequestSummary{}, Spans: []Span{}}

	rows, err := s.db.Query(`SELECT id, seq, COALESCE(thread_key,''), ts_start, ts_end, duration_ms, status, model, in_tokens, out_tokens,
		cache_read, cache_write, est_cost, stop_reason, num_tools, error FROM requests WHERE session_id = ? ORDER BY seq`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[int64]*RequestSummary{}
	for rows.Next() {
		var r RequestSummary
		if err := rows.Scan(&r.ID, &r.Seq, &r.ThreadKey, &r.TsStart, &r.TsEnd, &r.DurationMs, &r.Status, &r.Model,
			&r.InTokens, &r.OutTokens, &r.CacheRead, &r.CacheWrite, &r.EstCost, &r.StopReason, &r.NumTools, &r.Error); err != nil {
			return nil, err
		}
		r.ToolUses = []ToolUseRef{}
		d.Requests = append(d.Requests, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range d.Requests {
		byID[d.Requests[i].ID] = &d.Requests[i]
	}

	// Attach assistant preview + tool_use refs from content_blocks.
	bRows, err := s.db.Query(`SELECT request_id, role, type, tool_name, tool_use_id, text_preview
		FROM content_blocks WHERE session_id = ? ORDER BY request_id, idx`, id)
	if err != nil {
		return nil, err
	}
	defer bRows.Close()
	for bRows.Next() {
		var reqID int64
		var role, typ, toolName, toolUseID, preview string
		if err := bRows.Scan(&reqID, &role, &typ, &toolName, &toolUseID, &preview); err != nil {
			return nil, err
		}
		r := byID[reqID]
		if r == nil {
			continue
		}
		switch typ {
		case "tool_use":
			r.ToolUses = append(r.ToolUses, ToolUseRef{ToolUseID: toolUseID, Name: toolName, Input: preview})
		case "text":
			if role == "assistant" && r.AssistantPreview == "" {
				r.AssistantPreview = preview
			}
		}
	}
	if err := bRows.Err(); err != nil {
		return nil, err
	}

	// Reconstruct spans: join each tool_use to the matching tool_result.
	spanRows, err := s.db.Query(`
		SELECT u.tool_use_id, u.tool_name, u.text_preview, u.request_id, ru.ts_end,
		       r.request_id, rr.ts_start, COALESCE(r.is_error,0)
		FROM content_blocks u
		JOIN requests ru ON ru.id = u.request_id
		LEFT JOIN content_blocks r ON r.type='tool_result' AND r.session_id = u.session_id
			AND r.tool_use_id = u.tool_use_id AND r.request_id <> u.request_id
		LEFT JOIN requests rr ON rr.id = r.request_id
		WHERE u.session_id = ? AND u.type='tool_use'
		ORDER BY ru.ts_end`, id)
	if err != nil {
		return nil, err
	}
	defer spanRows.Close()
	for spanRows.Next() {
		var sp Span
		var resultReq sql.NullInt64
		var ended sql.NullInt64
		var isErr int
		if err := spanRows.Scan(&sp.ToolUseID, &sp.Name, &sp.InputPreview, &sp.CallRequest, &sp.Started,
			&resultReq, &ended, &isErr); err != nil {
			return nil, err
		}
		if resultReq.Valid {
			sp.HasResult = true
			sp.ResultReq = resultReq.Int64
			sp.Ended = ended.Int64
			sp.DurationMs = ended.Int64 - sp.Started
			if sp.DurationMs < 0 {
				sp.DurationMs = 0
			}
			sp.IsError = isErr == 1
		}
		d.Spans = append(d.Spans, sp)
	}
	return d, spanRows.Err()
}

// RequestDetail is the full content of one request/response exchange.
type RequestDetail struct {
	ID          int64                `json:"id"`
	SessionID   int64                `json:"session_id"`
	Seq         int                  `json:"seq"`
	TsStart     int64                `json:"ts_start"`
	TsEnd       int64                `json:"ts_end"`
	DurationMs  int64                `json:"duration_ms"`
	Method      string               `json:"method"`
	Path        string               `json:"path"`
	Status      int                  `json:"status"`
	Model       string               `json:"model"`
	IsSSE       bool                 `json:"is_sse"`
	Truncated   bool                 `json:"truncated"`
	StopReason  string               `json:"stop_reason"`
	Usage       parse.Usage          `json:"usage"`
	EstCost     float64              `json:"est_cost"`
	Error       string               `json:"error"`
	System      []parse.ContentBlock `json:"system"`
	Messages    []parse.Message      `json:"messages"`
	Response    parse.ParsedResponse `json:"response"`
	RawRequest  *string              `json:"raw_request,omitempty"`
	RawResponse *string              `json:"raw_response,omitempty"`
}

// GetRequest loads one exchange, unzipping and parsing the stored bodies. When
// raw is true the verbatim wire bytes are included as well.
func (s *Store) GetRequest(id int64, raw bool) (*RequestDetail, error) {
	var d RequestDetail
	var reqBlob, respBlob string
	var isSSE, truncated int
	err := s.db.QueryRow(`SELECT id, session_id, seq, ts_start, ts_end, duration_ms, method, path, status, model,
		is_sse, truncated, req_blob, resp_blob, in_tokens, out_tokens, cache_read, cache_write, est_cost, stop_reason, error
		FROM requests WHERE id = ?`, id).
		Scan(&d.ID, &d.SessionID, &d.Seq, &d.TsStart, &d.TsEnd, &d.DurationMs, &d.Method, &d.Path, &d.Status, &d.Model,
			&isSSE, &truncated, &reqBlob, &respBlob, &d.Usage.InputTokens, &d.Usage.OutputTokens,
			&d.Usage.CacheReadInputTokens, &d.Usage.CacheCreationInputTokens, &d.EstCost, &d.StopReason, &d.Error)
	if err != nil {
		return nil, err
	}
	d.IsSSE = isSSE == 1
	d.Truncated = truncated == 1

	reqBody, err := s.loadBlob(reqBlob)
	if err != nil {
		return nil, err
	}
	respBody, err := s.loadBlob(respBlob)
	if err != nil {
		return nil, err
	}

	pr := parse.ParseRequest(reqBody)
	d.System = pr.System
	d.Messages = pr.Messages
	if d.Messages == nil {
		d.Messages = []parse.Message{}
	}
	d.Response = parse.ParseResponse(respBody, d.IsSSE)

	if raw {
		rq := string(reqBody)
		rs := string(respBody)
		d.RawRequest = &rq
		d.RawResponse = &rs
	}
	return &d, nil
}

// ToolCallRef is one tool call in the analytics drill-down.
type ToolCallRef struct {
	RequestID int64  `json:"request_id"`
	SessionID int64  `json:"session_id"`
	TsStart   int64  `json:"ts_start"`
	ToolUseID string `json:"tool_use_id"`
	Input     string `json:"input_preview"`
}

// ToolCalls returns every call of a given tool, newest first, for drill-down.
func (s *Store) ToolCalls(name string, limit int) ([]ToolCallRef, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT b.request_id, b.session_id, r.ts_start, b.tool_use_id, b.text_preview
		FROM content_blocks b JOIN requests r ON r.id = b.request_id
		WHERE b.type='tool_use' AND b.tool_name = ? ORDER BY r.ts_start DESC LIMIT ?`, name, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ToolCallRef{}
	for rows.Next() {
		var x ToolCallRef
		if err := rows.Scan(&x.RequestID, &x.SessionID, &x.TsStart, &x.ToolUseID, &x.Input); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// SearchHit is one full-text search result.
type SearchHit struct {
	RequestID int64  `json:"request_id"`
	SessionID int64  `json:"session_id"`
	Snippet   string `json:"snippet"`
}

// Search runs an FTS5 query over message text and returns matching requests.
func (s *Store) Search(q string, limit int) ([]SearchHit, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT request_id, session_id, snippet(blocks_fts, 0, '[', ']', ' … ', 12)
		FROM blocks_fts WHERE blocks_fts MATCH ? ORDER BY rank LIMIT ?`, ftsQuery(q), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SearchHit{}
	for rows.Next() {
		var x SearchHit
		if err := rows.Scan(&x.RequestID, &x.SessionID, &x.Snippet); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ftsQuery wraps a user query so plain words are matched as a prefix-friendly
// phrase, avoiding FTS5 syntax errors on punctuation.
func ftsQuery(q string) string {
	q = strings.TrimSpace(q)
	q = strings.ReplaceAll(q, `"`, `""`)
	return `"` + q + `"`
}

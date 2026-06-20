package store

import (
	"database/sql"

	"cc-gateway/internal/parse"
)

// This file supports re-deriving token usage for exchanges that were captured
// before the proxy decoded gzip-compressed responses. The wire bytes are still
// stored, so usage can be recovered by re-parsing them.

// GetMeta returns a stored meta value, or "" when the key is absent.
func (s *Store) GetMeta(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// SetMeta upserts a meta value.
func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO meta(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// CostRow is a request's model and token usage, used to recompute its cost.
type CostRow struct {
	ID    int64
	Model string
	Usage parse.Usage
}

// CostRows returns the model and usage of every request, for cost recomputation.
func (s *Store) CostRows() ([]CostRow, error) {
	rows, err := s.db.Query(`SELECT id, model, in_tokens, out_tokens, cache_read, cache_write FROM requests`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CostRow
	for rows.Next() {
		var c CostRow
		if err := rows.Scan(&c.ID, &c.Model, &c.Usage.InputTokens, &c.Usage.OutputTokens,
			&c.Usage.CacheReadInputTokens, &c.Usage.CacheCreationInputTokens); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateCost rewrites just the est_cost column for one request.
func (s *Store) UpdateCost(id int64, cost float64) error {
	_, err := s.db.Exec(`UPDATE requests SET est_cost = ? WHERE id = ?`, cost, id)
	return err
}

// ZeroUsageRequests returns the ids of successful /v1/messages exchanges that
// have a stored response body but no recorded token usage — the rows eligible
// for re-parsing.
func (s *Store) ZeroUsageRequests() ([]int64, error) {
	rows, err := s.db.Query(`SELECT id FROM requests
		WHERE in_tokens = 0 AND out_tokens = 0 AND cache_read = 0 AND cache_write = 0
		  AND COALESCE(resp_blob,'') <> '' AND status = 200 AND path LIKE '%/messages'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// LoadResponseBody returns the decompressed stored response body for a request
// (the store's own gzip layer removed; any upstream encoding remains).
func (s *Store) LoadResponseBody(id int64) ([]byte, error) {
	var blob string
	if err := s.db.QueryRow(`SELECT COALESCE(resp_blob,'') FROM requests WHERE id = ?`, id).Scan(&blob); err != nil {
		return nil, err
	}
	return s.loadBlob(blob)
}

// UpdateUsage rewrites the usage-derived columns for one request. Empty model or
// stop_reason leave the existing values untouched.
func (s *Store) UpdateUsage(id int64, in, out, cacheRead, cacheWrite int, model, stop string, isSSE bool, cost float64) error {
	_, err := s.db.Exec(`UPDATE requests SET
		in_tokens = ?, out_tokens = ?, cache_read = ?, cache_write = ?,
		model = COALESCE(NULLIF(?,''), model),
		stop_reason = COALESCE(NULLIF(?,''), stop_reason),
		is_sse = ?, est_cost = ?
		WHERE id = ?`,
		in, out, cacheRead, cacheWrite, model, stop, b2i(isSSE), cost, id)
	return err
}

// RecomputeSessionAggregates rebuilds every session's rolling totals from its
// request rows. It is idempotent and cheap; run it after backfilling usage.
func (s *Store) RecomputeSessionAggregates() error {
	_, err := s.db.Exec(`UPDATE sessions SET
		num_requests = (SELECT COUNT(*) FROM requests WHERE session_id = sessions.id),
		in_tokens    = (SELECT COALESCE(SUM(in_tokens),0)  FROM requests WHERE session_id = sessions.id),
		out_tokens   = (SELECT COALESCE(SUM(out_tokens),0) FROM requests WHERE session_id = sessions.id),
		cache_read   = (SELECT COALESCE(SUM(cache_read),0) FROM requests WHERE session_id = sessions.id),
		cache_write  = (SELECT COALESCE(SUM(cache_write),0) FROM requests WHERE session_id = sessions.id),
		est_cost     = (SELECT COALESCE(SUM(est_cost),0)   FROM requests WHERE session_id = sessions.id),
		error_count  = (SELECT COUNT(*) FROM requests WHERE session_id = sessions.id AND (status >= 400 OR COALESCE(error,'') <> ''))`)
	return err
}

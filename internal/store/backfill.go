package store

// This file supports re-deriving token usage for exchanges that were captured
// before the proxy decoded gzip-compressed responses. The wire bytes are still
// stored, so usage can be recovered by re-parsing them.

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

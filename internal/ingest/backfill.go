package ingest

import (
	"cc-gateway/internal/parse"
	"cc-gateway/internal/store"
)

// Backfill re-derives token usage for exchanges captured before the proxy
// decoded gzip-compressed responses. It re-parses each zero-usage request's
// stored body, fills in usage, model, stop reason, cost and the is_sse flag,
// then rebuilds the per-session aggregates. It is safe to run on every startup:
// only zero-usage rows are considered, so a fully-parsed database is a no-op.
// Returns the number of requests updated.
func Backfill(st *store.Store) (int, error) {
	ids, err := st.ZeroUsageRequests()
	if err != nil {
		return 0, err
	}
	updated := 0
	for _, id := range ids {
		body, err := st.LoadResponseBody(id)
		if err != nil || len(body) == 0 {
			continue
		}
		resp := parse.ParseResponseAuto(body)
		u := resp.Usage
		if u == (parse.Usage{}) {
			continue // still nothing parseable (an error body or truncated capture)
		}
		cost := estCost(resp.Model, u)
		if err := st.UpdateUsage(id, u.InputTokens, u.OutputTokens, u.CacheReadInputTokens,
			u.CacheCreationInputTokens, resp.Model, resp.StopReason, parse.IsSSEBody(body), cost); err != nil {
			return updated, err
		}
		updated++
	}

	// Recompute every request's cost when the price table changed, so rows costed
	// under stale pricing (e.g. the old Opus rates) are corrected in place.
	if ver, _ := st.GetMeta("cost_version"); ver != CostVersion {
		costRows, err := st.CostRows()
		if err != nil {
			return updated, err
		}
		for _, c := range costRows {
			if err := st.UpdateCost(c.ID, estCost(c.Model, c.Usage)); err != nil {
				return updated, err
			}
		}
		if err := st.SetMeta("cost_version", CostVersion); err != nil {
			return updated, err
		}
	}

	if err := st.RecomputeSessionAggregates(); err != nil {
		return updated, err
	}
	return updated, nil
}

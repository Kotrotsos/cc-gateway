package ingest

import (
	"strings"

	"cc-gateway/internal/parse"
)

// CostVersion bumps whenever the price table below changes, so a stored database
// can detect that its recorded est_cost values are stale and recompute them.
const CostVersion = "2"

// price is per-million-token USD pricing for one model family. cacheRead is the
// 0.1x cache-hit rate; cacheWrite is the 1.25x rate for the default 5-minute
// ephemeral cache that Claude Code uses.
type price struct {
	in, out, cacheRead, cacheWrite float64
}

// prices is a small price table keyed by a model-id substring, most specific
// first. Unknown models yield a zero cost. Values are USD per million tokens and
// are labelled as an estimate in the UI.
//
// Opus 4.6 and later dropped to $5/$25 (a 3x cut from the $15/$75 of Opus 4 and
// 4.1), so the newer ids must be matched before the generic "opus" fallback.
var prices = []struct {
	match string
	price
}{
	{"opus-4-8", price{5, 25, 0.50, 6.25}},
	{"opus-4-7", price{5, 25, 0.50, 6.25}},
	{"opus-4-6", price{5, 25, 0.50, 6.25}},
	{"opus", price{15, 75, 1.50, 18.75}}, // Opus 4 / 4.1 (legacy pricing)
	{"sonnet", price{3, 15, 0.30, 3.75}},
	{"haiku", price{0.80, 4, 0.08, 1}},
}

// estCost returns an approximate USD cost for a usage record on a given model.
func estCost(model string, u parse.Usage) float64 {
	p, ok := lookupPrice(model)
	if !ok {
		return 0
	}
	const m = 1_000_000.0
	return float64(u.InputTokens)/m*p.in +
		float64(u.OutputTokens)/m*p.out +
		float64(u.CacheReadInputTokens)/m*p.cacheRead +
		float64(u.CacheCreationInputTokens)/m*p.cacheWrite
}

func lookupPrice(model string) (price, bool) {
	m := strings.ToLower(model)
	for _, p := range prices {
		if strings.Contains(m, p.match) {
			return p.price, true
		}
	}
	return price{}, false
}

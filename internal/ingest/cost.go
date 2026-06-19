package ingest

import (
	"strings"

	"cc-gateway/internal/parse"
)

// price is per-million-token USD pricing for one model family.
type price struct {
	in, out, cacheRead, cacheWrite float64
}

// prices is a small, approximate price table keyed by a model-id substring.
// Unknown models yield a zero cost. Values are USD per million tokens and are
// deliberately coarse; the UI labels cost as an estimate.
var prices = []struct {
	match string
	price
}{
	{"opus", price{15, 75, 1.50, 18.75}},
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

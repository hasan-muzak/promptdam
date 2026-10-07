package requestlog

import (
	"math"

	"github.com/hasan-muzak/keel/pkg/llm"
	"github.com/hasan-muzak/keel/pkg/llm/wire"

	"github.com/hasan-muzak/keel/products/promptdam/server/features/providers"
)

// Cost is what a call cost at list prices, in micro-USD: the provider's
// reported usage priced by the catalog (cache reads and writes included,
// Anthropic's one-hour cache writes at twice the input price). ok is false
// for a model the catalog doesn't know.
func Cost(prices *llm.Catalog, kind, model string, u wire.Usage) (micros int64, ok bool) {
	catalogName := ""
	if k, found := providers.LookupKind(kind); found {
		catalogName = k.Catalog
	}
	m, ok := prices.Lookup(catalogName, model)
	if !ok {
		return 0, false
	}
	cost := float64(m.CostMicros(u.Usage))
	// CostMicros prices every cache write at the five-minute rate.
	if extra := 2*m.Input - m.CacheWrite; u.CacheWrite1h > 0 && extra > 0 {
		cost += float64(min(u.CacheWrite1h, u.CacheWrite)) * extra
	}
	return int64(math.Round(cost)), true
}

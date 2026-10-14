package requestlog

import (
	"math"

	"github.com/hasan-muzak/keel-core/pkg/llm"
	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/server/features/providers"
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
	return CostAt(m, u), true
}

// CostAt is what a call cost at a model's prices, in micro-USD.
func CostAt(m llm.Model, u wire.Usage) int64 {
	cost := float64(m.CostMicros(u.Usage))
	// CostMicros prices every cache write at the five-minute rate; one-hour
	// writes cost the listed one-hour price, else twice the input price.
	if u.CacheWrite1h > 0 {
		at := m.At(u.Input)
		hour := at.CacheWrite1h
		if hour == 0 {
			hour = 2 * at.Input
		}
		if extra := hour - at.CacheWrite; extra > 0 {
			cost += float64(min(u.CacheWrite1h, u.CacheWrite)) * extra
		}
	}
	return int64(math.Round(cost))
}

// Savings reasons, in the order they explain a difference.
const (
	ReasonResponseCache = "response_cache"
	ReasonRouting       = "routing"
	ReasonFallback      = "fallback"
	ReasonPromptCache   = "prompt_cache"
)

// Savings bases: how the ledger knows a saving.
const (
	// BasisProvider: the provider's own cache counts, priced; the same
	// model, the same tokens.
	BasisProvider = "provider"
	// BasisCache: an answer the response cache kept; no provider was
	// called.
	BasisCache = "cache"
	// BasisEstimate: the model asked for, priced on the tokens of the model
	// that served the call (which may think or write more, or less).
	BasisEstimate = "estimate"
	// BasisReplays: the router's default tier, priced on the call's input
	// and the output that tier writes against the served one, measured on
	// the router's judged calls.
	BasisReplays = "replays"
)

// Ledger is what a call would have cost on the model asked for at list
// price without any cache, what it saved against that, why, and how the
// saving is known.
type Ledger struct {
	Counterfactual, Savings int64
	Reason, Basis           string
}

// counterfactual prices usage on model m with every input token fresh.
func counterfactual(m llm.Model, u wire.Usage) int64 {
	return int64(m.CostMicros(llm.Usage{Input: u.Input, Output: u.Output}))
}

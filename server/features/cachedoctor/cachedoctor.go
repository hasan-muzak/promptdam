// Package cachedoctor explains why a prompt family's calls miss providers'
// prompt caches, and what fixing it is worth. It reads only the prefix
// rollup (never prompts): how many different prefixes a family's calls
// sent, which changing values their instructions held, whether tools
// came in changing order, whether Anthropic calls had breakpoints, how
// long prefixes are, and how much input the caches served.
package cachedoctor

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/events"
	"github.com/hasan-muzak/keel-core/pkg/llm"

	"github.com/hasan-muzak/promptdam/server/features/families"
	"github.com/hasan-muzak/promptdam/server/features/prices"
	"github.com/hasan-muzak/promptdam/server/features/promptcache"
	"github.com/hasan-muzak/promptdam/server/features/providers"
)

// MaxRange bounds a diagnosis's time range.
const MaxRange = 90 * 24 * time.Hour

// ErrRange refuses a time range that is invalid or too long.
var ErrRange = errors.New("cachedoctor: the time range is invalid or too long")

// Finding codes.
const (
	OK                  = "ok"
	TooShort            = "prefix_too_short"
	Timestamps          = "timestamps_in_prefix"
	IDs                 = "ids_in_prefix"
	Numbers             = "numbers_in_prefix"
	ToolOrder           = "tool_order_changes"
	VaryingInstructions = "instructions_vary"
	NoBreakpoints       = "no_breakpoints"
)

// Finding is one reason a family misses the cache, and its fix.
type Finding struct {
	Code     string
	Severity string // info, warning
	Title    string
	Detail   string
	Fix      string
}

// Diagnosis is a family's prompt cache health over a range.
type Diagnosis struct {
	Family      string
	Model, Kind string
	Requests    uint64
	// Prefixes counts the different prefixes the calls sent.
	Prefixes        uint64
	InputTokens     uint64
	CacheReadTokens uint64
	// HitRate is the share of input tokens the caches served.
	HitRate      float64
	PrefixTokens uint64
	Findings     []Finding
	// PotentialMonthlyMicros is what the cache could save a month at most,
	// were every call after the first to read the prefix from it.
	PotentialMonthlyMicros int64
}

// Service diagnoses families.
type Service struct {
	db     *events.DB
	prices prices.Prices
}

// New returns a Service; db may be nil (no ClickHouse: nothing to say).
func New(db *events.DB, p prices.Prices) *Service { return &Service{db: db, prices: p} }

type prefixRow struct {
	Family     string `ch:"family_id"`
	Requests   uint64 `ch:"requests"`
	Input      uint64 `ch:"input_tokens"`
	CacheRead  uint64 `ch:"cache_read_tokens"`
	Flags      uint16 `ch:"prefix_flags"`
	Tokens     uint32 `ch:"prefix_tokens"`
	Model      string `ch:"served_model"`
	Kind       string `ch:"kind"`
	PrefixHash string `ch:"prefix_hash"`
}

// Diagnose diagnoses one family over [from, to), or, when family is "",
// the 20 families that sent the most input tokens, by what fixing them is
// worth.
func (s *Service) Diagnose(ctx context.Context, project uuid.UUID, family string, from, to time.Time) ([]Diagnosis, error) {
	if from.IsZero() || to.IsZero() || !from.Before(to) || to.Sub(from) > MaxRange {
		return nil, ErrRange
	}
	out := []Diagnosis{}
	if s.db == nil {
		return out, nil
	}
	window := ` WHERE {tenant} AND hour >= toStartOfHour(fromUnixTimestamp64Milli(?)) AND hour < fromUnixTimestamp64Milli(?)`
	args := []any{from.UnixMilli(), to.UnixMilli()}
	names := []string{family}
	if family == "" {
		var top []struct {
			Family string `ch:"family_id"`
		}
		if err := s.db.Select(ctx, &top, events.Project(project), `SELECT family_id FROM prefixes_hourly`+window+
			` GROUP BY family_id ORDER BY sum(input_tokens) DESC LIMIT 20`, args...); err != nil {
			return nil, fmt.Errorf("cachedoctor: top families: %w", err)
		}
		if len(top) == 0 {
			return out, nil
		}
		names = names[:0]
		for _, t := range top {
			names = append(names, t.Family)
		}
	}
	var rows []prefixRow
	err := s.db.Select(ctx, &rows, events.Project(project), `SELECT family_id, prefix_hash, sum(requests) AS requests,
		sum(input_tokens) AS input_tokens, sum(cache_read_tokens) AS cache_read_tokens,
		groupBitOr(prefix_flags) AS prefix_flags, max(prefix_tokens) AS prefix_tokens,
		any(served_model) AS served_model, any(kind) AS kind
		FROM prefixes_hourly`+window+` AND family_id IN (?) GROUP BY family_id, prefix_hash`,
		append(args, names)...)
	if err != nil {
		return nil, fmt.Errorf("cachedoctor: %w", err)
	}
	byFamily := map[string][]prefixRow{}
	for _, r := range rows {
		byFamily[r.Family] = append(byFamily[r.Family], r)
	}
	days := to.Sub(from).Hours() / 24
	for f, rs := range byFamily {
		out = append(out, s.diagnose(f, rs, days))
	}
	slices.SortFunc(out, func(a, b Diagnosis) int {
		if a.PotentialMonthlyMicros != b.PotentialMonthlyMicros {
			return int(b.PotentialMonthlyMicros - a.PotentialMonthlyMicros)
		}
		return int(b.InputTokens) - int(a.InputTokens)
	})
	return out, nil
}

func (s *Service) diagnose(family string, rows []prefixRow, days float64) Diagnosis {
	d := Diagnosis{Family: family, Prefixes: uint64(len(rows))}
	var flags families.Flags
	var weighted uint64
	byModel := map[[2]string]uint64{}
	for _, r := range rows {
		d.Requests += r.Requests
		d.InputTokens += r.Input
		d.CacheReadTokens += r.CacheRead
		flags |= families.Flags(r.Flags)
		weighted += uint64(r.Tokens) * r.Requests
		byModel[[2]string{r.Kind, r.Model}] += r.Requests
	}
	var top uint64
	for k, n := range byModel {
		if n > top {
			top, d.Kind, d.Model = n, k[0], k[1]
		}
	}
	if d.Requests > 0 {
		d.PrefixTokens = weighted / d.Requests
	}
	if d.InputTokens > 0 {
		d.HitRate = float64(d.CacheReadTokens) / float64(d.InputTokens)
	}
	d.Findings = findings(d, flags)

	// At most every call after the first reads the prefix from the cache.
	if price, ok := s.price(d.Kind, d.Model); ok && d.Requests > 1 && days > 0 {
		could := float64(d.Requests-1) * float64(d.PrefixTokens)
		missed := math.Max(0, could-float64(d.CacheReadTokens))
		cached := price.CachedInput
		if cached == 0 || cached >= price.Input {
			cached = price.Input * 0.1
		}
		d.PotentialMonthlyMicros = int64(math.Round(missed * (price.Input - cached) * 30 / days))
	}
	// Nothing to fix, nothing to win.
	if code := d.Findings[0].Code; code == OK || code == TooShort {
		d.PotentialMonthlyMicros = 0
	}
	return d
}

func (s *Service) price(kind, model string) (llm.Model, bool) {
	catalogName := ""
	if k, ok := providers.LookupKind(kind); ok {
		catalogName = k.Catalog
	}
	return s.prices.Current().Lookup(catalogName, model)
}

// findings explain a family's misses, most important first.
func findings(d Diagnosis, flags families.Flags) []Finding {
	var out []Finding
	anthropic := d.Kind == "anthropic" || (d.Kind == providers.Custom && len(d.Model) > 7 && d.Model[:7] == "claude-")
	if d.PrefixTokens < uint64(promptcache.MinTokens(d.Model)) {
		return []Finding{{Code: TooShort, Severity: "info", Title: "The prefix is too short to cache",
			Detail: fmt.Sprintf("Providers cache prefixes from about %d tokens; this family's are about %d.",
				promptcache.MinTokens(d.Model), d.PrefixTokens),
			Fix: "Nothing to fix. Caching pays off once the instructions and tools are long and stable."}}
	}
	changing := d.Prefixes > 1 && float64(d.Prefixes) > 0.05*float64(d.Requests)
	if changing {
		share := fmt.Sprintf("%d of %d calls sent a prefix of their own.", d.Prefixes, d.Requests)
		switch {
		case flags&(families.Dates|families.Times) != 0:
			out = append(out, Finding{Code: Timestamps, Severity: "warning", Title: "A date or time in the instructions",
				Detail: share + " The instructions hold the current date or time, so no two calls share a prefix.",
				Fix: "Move the date or time out of the system prompt, into the last user message (\"Today is …\"), " +
					"so the instructions stay the same from call to call."})
		case flags&(families.UUIDs|families.HexIDs) != 0:
			out = append(out, Finding{Code: IDs, Severity: "warning", Title: "IDs in the instructions",
				Detail: share + " The instructions hold request, user or trace IDs.",
				Fix:    "Pass IDs in the user message or in metadata, not in the system prompt."})
		case flags&families.Numbers != 0:
			out = append(out, Finding{Code: Numbers, Severity: "warning", Title: "Changing numbers in the instructions",
				Detail: share + " The instructions hold numbers that change (counts, prices, versions).",
				Fix:    "Keep the instructions fixed; pass the numbers with the user's message."})
		case flags&families.ToolsUnsorted != 0:
			out = append(out, Finding{Code: ToolOrder, Severity: "warning", Title: "Tools in changing order",
				Detail: share + " The tools are listed in a different order from call to call.",
				Fix:    "Sort the tools by name (or keep one fixed list) before sending them."})
		default:
			out = append(out, Finding{Code: VaryingInstructions, Severity: "warning", Title: "The instructions vary",
				Detail: share + " The system prompt or tools differ between calls.",
				Fix: "Keep everything that changes per call (user data, retrieved documents) after the stable " +
					"instructions, in the messages."})
		}
	}
	if anthropic && flags&(families.Breakpoints|families.GatewayBreakpoints) == 0 {
		out = append(out, Finding{Code: NoBreakpoints, Severity: "warning", Title: "No cache breakpoints",
			Detail: "Anthropic caches only prefixes marked with cache_control, and these calls mark none.",
			Fix:    "Turn on prompt_cache on the route, or add cache_control to the last system block and tool."})
	}
	if len(out) == 0 {
		out = append(out, Finding{Code: OK, Severity: "info", Title: "Caching well",
			Detail: fmt.Sprintf("The prefix is stable; the caches served %.0f%% of input tokens.", d.HitRate*100)})
	}
	return out
}

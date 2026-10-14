// Package cachedoctor explains why a prompt family's calls miss providers'
// prompt caches, and what fixing it is worth. It reads only the prefix
// rollup (never prompts): how many different prefixes a family's calls
// sent, which changing values their instructions held, whether tools
// came in changing order, whether calls had the breakpoints their model
// needs, how long prefixes are, and how much input the caches served;
// and what every project's calls showed of the model (cacheprofile): one
// that never reads its cache, backends that never cache it.
package cachedoctor

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/events"
	"github.com/hasan-muzak/keel-core/pkg/llm"

	"github.com/hasan-muzak/promptdam/server/features/cacheprofile"
	"github.com/hasan-muzak/promptdam/server/features/cachettl"
	"github.com/hasan-muzak/promptdam/server/features/families"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
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
	// KeptOneHour: the family's calls come back 5 to 60 minutes apart often
	// enough that its cache is kept an hour (learned, features/cachepolicy).
	KeptOneHour = "kept_one_hour"
	// ColdParallel: calls wrote the same prefix at once, none reading it.
	ColdParallel = "parallel_cold_writes"
	// ModelNeverCaches: across projects, the model read next to nothing
	// of what its cache should have served.
	ModelNeverCaches = "model_never_caches"
	// BackendNeverCaches: some of a router of providers' backends never
	// cached the model while another did; the gateway keeps calls off
	// them.
	BackendNeverCaches = "backend_never_caches"
)

// neverCaches is the read share below which a model is said not to cache.
const neverCaches = 0.05

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
	// Target is the model the gateway asked for (a router of providers
	// names the one it served its own way, Model).
	Target   string
	Requests uint64
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
	// TTL are the family's learned cache lifetimes ("1h", else five
	// minutes).
	TTL cachettl.TTL
}

// Policies says what was learned of a project's families' cache lifetimes
// (features/cachepolicy).
type Policies interface {
	Families(ctx context.Context, project uuid.UUID) (map[string]cachettl.Family, error)
}

// Profiles are every model's caching as all projects' calls showed it
// (cacheprofile.Store).
type Profiles interface {
	Current() *cacheprofile.Doc
}

// Service diagnoses families.
type Service struct {
	db       *events.DB
	prices   prices.Prices
	policies Policies
	profiles Profiles
}

// New returns a Service; db may be nil (no ClickHouse: nothing to say),
// and policies and profiles (nothing learned to report).
func New(db *events.DB, p prices.Prices, policies Policies, profiles Profiles) *Service {
	return &Service{db: db, prices: p, policies: policies, profiles: profiles}
}

type prefixRow struct {
	Family     string `ch:"family_id"`
	Requests   uint64 `ch:"requests"`
	Input      uint64 `ch:"input_tokens"`
	CacheRead  uint64 `ch:"cache_read_tokens"`
	Flags      uint16 `ch:"prefix_flags"`
	Tokens     uint32 `ch:"prefix_tokens"`
	Model      string `ch:"served_model"`
	Target     string `ch:"target_model"`
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
		any(served_model) AS served_model, any(target_model) AS target_model, any(kind) AS kind
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
	learned := map[string]cachettl.Family{}
	if s.policies != nil {
		if fams, err := s.policies.Families(ctx, project); err == nil {
			learned = fams
		}
	}
	for f, rs := range byFamily {
		d := s.diagnose(f, rs, days)
		if l, ok := learned[f]; ok {
			lifetimes(&d, l)
		}
		out = append(out, d)
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
	byModel := map[[3]string]uint64{}
	for _, r := range rows {
		d.Requests += r.Requests
		d.InputTokens += r.Input
		d.CacheReadTokens += r.CacheRead
		flags |= families.Flags(r.Flags)
		weighted += uint64(r.Tokens) * r.Requests
		byModel[[3]string{r.Kind, r.Model, r.Target}] += r.Requests
	}
	var top uint64
	for k, n := range byModel {
		if n > top {
			top, d.Kind, d.Model, d.Target = n, k[0], k[1], k[2]
		}
	}
	if d.Requests > 0 {
		d.PrefixTokens = weighted / d.Requests
	}
	if d.InputTokens > 0 {
		d.HitRate = float64(d.CacheReadTokens) / float64(d.InputTokens)
	}
	minTokens := promptcache.DefaultMinTokens
	price, priced := s.price(d.Kind, d.Model)
	if !priced && d.Target != "" {
		price, priced = s.price(d.Kind, d.Target)
	}
	if priced && price.MinCacheTokens > 0 {
		minTokens = int(price.MinCacheTokens)
	}
	d.Findings = findings(d, flags, minTokens)
	if d.Findings[0].Code != TooShort {
		var doc *cacheprofile.Doc
		if s.profiles != nil {
			doc = s.profiles.Current()
		}
		pooled(&d, flags, doc, price.CacheWritePriced)
	}

	// At most every call after the first reads the prefix from the cache.
	if priced && d.Requests > 1 && days > 0 {
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

// pooled adds what every project's calls showed of the family's model
// (doc: nil when nothing was learned): breakpoints a router of providers'
// model needs and these calls lack (its strategy as learned, else by its
// price list: writePriced), a model that doesn't read its cache, backends
// the gateway keeps its calls off. Figures from other projects are shown
// only from cacheprofile.MinProjects up; a warning replaces "caching
// well".
func pooled(d *Diagnosis, flags families.Flags, doc *cacheprofile.Doc, writePriced bool) {
	model := d.Target
	if model == "" {
		model = d.Model
	}
	var add []Finding
	strategies := gateway.CacheDialectOf(d.Kind, false).Strategies
	strategy := gateway.DefaultCacheStrategy(d.Kind, false, writePriced)
	if p, ok := doc.Of(d.Kind, model); ok && slices.Contains(strategies, p.Use) {
		strategy = p.Use
	}
	if len(strategies) > 1 && promptcache.Marks(strategy) && flags&(families.Breakpoints|families.GatewayBreakpoints) == 0 {
		add = append(add, Finding{Code: NoBreakpoints, Severity: "warning", Title: "No cache breakpoints",
			Detail: "This model caches only prompts marked with cache_control, and these calls mark none.",
			Fix:    "Turn on prompt_cache on the route (the gateway marks them), or add cache_control to the last system message."})
	}
	if p, ok := doc.Shown(d.Kind, model); ok {
		if p.Evidence >= cacheprofile.Evidence && p.ReadShare < neverCaches {
			add = append(add, Finding{Code: ModelNeverCaches, Severity: "warning", Title: "The model doesn't read its cache",
				Detail: fmt.Sprintf("Across %d projects' calls, %s read %.0f%% of the prompt its cache should have served.",
					p.Projects, model, p.ReadShare*100),
				Fix: "Prompt changes won't help here: for long conversations, prefer a model that caches (the router report " +
					"shows each model's cache rate)."})
		}
		if len(p.Avoid) > 0 {
			add = append(add, Finding{Code: BackendNeverCaches, Severity: "info", Title: "Some of the model's backends never cache",
				Detail: fmt.Sprintf("Across %d projects' calls, %s never read this model's cache while another backend did.",
					p.Projects, strings.Join(p.Avoid, ", ")),
				Fix: "Nothing to do: the gateway keeps your calls off them, unless your calls set their own provider preferences."})
		}
	}
	if len(add) == 0 {
		return
	}
	if d.Findings[0].Code == OK && slices.ContainsFunc(add, func(f Finding) bool { return f.Severity == "warning" }) {
		d.Findings = d.Findings[:0]
	}
	d.Findings = append(d.Findings, add...)
}

// policyDays is the window the learner weighs (cachepolicy.Window), to make
// its sums monthly.
const policyDays = 3

// lifetimes adds what the learner found of a family: an hour kept, and
// calls that wrote the same prefix at once.
func lifetimes(d *Diagnosis, f cachettl.Family) {
	d.TTL = f.TTLOf()
	if d.TTL != (cachettl.TTL{}) {
		saved := f.Tail.NetMicros
		if d.TTL.Prefix == cachettl.OneHour {
			saved += f.Prefix.NetMicros
		}
		d.Findings = append(d.Findings, Finding{Code: KeptOneHour, Severity: "info", Title: "Cached for an hour",
			Detail: fmt.Sprintf("The family's calls often come back 5 to 60 minutes later, past the default five minutes: "+
				"its cache is kept an hour (written at twice the input price instead of 1.25 times), worth about $%.2f a month.",
				float64(saved)*30/policyDays/1e6),
			Fix: "Nothing to do: the gateway sets the longer lifetime, and returns to five minutes if the gaps change."})
	}
	if f.ColdParallel > 0 && f.ColdParallelMicros > 0 {
		monthly := f.ColdParallelMicros * 30 / policyDays
		d.Findings = append(d.Findings, Finding{Code: ColdParallel, Severity: "warning", Title: "Calls write the same prefix at once",
			Detail: fmt.Sprintf("%d calls in the last %d days wrote the instructions within 10 seconds of another call and read "+
				"none of them: a cache entry is readable only once the first answer begins.", f.ColdParallel, policyDays),
			Fix: "Send one call first and the others once its answer begins streaming (or warm the cache before a batch)."})
		d.PotentialMonthlyMicros += monthly
	}
}

// findings explain a family's misses, most important first. minTokens is
// the shortest prefix its model's provider caches.
func findings(d Diagnosis, flags families.Flags, minTokens int) []Finding {
	var out []Finding
	anthropic := d.Kind == "anthropic" || (d.Kind == providers.Custom && len(d.Model) > 7 && d.Model[:7] == "claude-")
	if d.PrefixTokens < uint64(minTokens) {
		return []Finding{{Code: TooShort, Severity: "info", Title: "The prefix is too short to cache",
			Detail: fmt.Sprintf("Providers cache prefixes from about %d tokens; this family's are about %d.",
				minTokens, d.PrefixTokens),
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
	if len(out) == 0 && d.HitRate >= neverCaches {
		out = append(out, Finding{Code: OK, Severity: "info", Title: "Caching well",
			Detail: fmt.Sprintf("The prefix is stable; the caches served %.0f%% of input tokens.", d.HitRate*100)})
	} else if len(out) == 0 {
		out = append(out, Finding{Code: OK, Severity: "info", Title: "The prefix is stable",
			Detail: fmt.Sprintf("The caches served %.0f%% of input tokens: calls may come back after their cache expired, "+
				"or the model may not cache.", d.HitRate*100)})
	}
	return out
}

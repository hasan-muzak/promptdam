// Package requestlog turns each finished gateway call into its record:
// what it cost (counted against the caller's budgets) and a row of the
// request log in ClickHouse, written in batches off the call's path.
package requestlog

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/billing"
	"github.com/hasan-muzak/keel-core/pkg/events"
	"github.com/hasan-muzak/keel-core/pkg/llm"
	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/features/budgets"
	"github.com/hasan-muzak/promptdam/server/features/conversation"
	"github.com/hasan-muzak/promptdam/server/features/families"
	"github.com/hasan-muzak/promptdam/server/features/firewall"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
	"github.com/hasan-muzak/promptdam/server/features/payloads"
	"github.com/hasan-muzak/promptdam/server/features/prices"
	"github.com/hasan-muzak/promptdam/server/features/providers"
	"github.com/hasan-muzak/promptdam/server/features/resolver"
)

// Table is the request log.
const Table = "requests"

// defaultRetention applies when the plan can't be read: the Free plan's.
const defaultRetention = 7

// Row is one call in the request log.
type Row struct {
	ProjectID, OrgID, KeyID                                 uuid.UUID
	TS                                                      time.Time
	Trace, Developer, User, Route                           string
	Conversation, TurnKind, ChainState                      string
	HumanTurns                                              uint16
	ChainGapMs                                              uint32
	FamilyID, PrefixHash                                    string
	PrefixFlags                                             uint16
	PrefixTokens, EstInput, Messages, MaxTokens             uint32
	HasTools, JSONOutput                                    bool
	Rule                                                    string
	Router, Tier, DecidedTier, TierSource                   string
	TierConfidence                                          float32
	Shadow                                                  bool
	Effort, Healed                                          string
	RouterMicros                                            int64
	Decision                                                string
	DecisionMs                                              uint32
	FirewallAction                                          string
	FirewallKinds                                           []string
	PayloadRef                                              string
	Tags                                                    []string
	APIFormat, Provider, Kind, RequestedModel, ServedModel  string
	Backend, TargetModel, CacheStrategy                     string
	CacheTrial                                              bool
	ComputedMicros                                          int64
	Stream, Translated                                      bool
	Status                                                  uint16
	ErrorCode                                               string
	Attempts                                                uint8
	TTFBms, LatencyMs, UpstreamMs                           uint32
	UsageKnown                                              bool
	InputTokens, OutputTokens, CacheRead, CacheWrite, Think uint32
	CostMicros                                              int64
	Priced                                                  bool
	Counterfactual, Savings                                 int64
	SavingsReason, SavingsBasis, Cache                      string
	RequestBytes                                            uint32
	ResponseBytes                                           uint64
	RetentionDays                                           uint16
}

var columns = []string{
	"project_id", "ts", "trace", "org_id", "key_id", "developer", "end_user", "tags", "route", "conversation", "turn_kind",
	"human_turns", "chain_gap_ms", "chain_state", "family_id", "prefix_hash",
	"prefix_flags", "prefix_tokens", "est_input_tokens", "messages", "max_tokens", "has_tools", "json_output", "rule",
	"router", "tier", "decided_tier", "tier_source", "tier_confidence", "shadow", "effort", "healed", "router_micros", "decision", "decision_ms",
	"firewall_action", "firewall_kinds", "payload_ref", "api_format", "provider",
	"kind", "requested_model", "served_model", "backend", "target_model", "computed_micros", "cache_strategy", "cache_trial", "stream", "translated", "status", "error_code", "attempts", "ttfb_ms", "latency_ms",
	"upstream_ms", "usage_known", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens",
	"reasoning_tokens", "cost_micros", "priced", "counterfactual_micros", "savings_micros", "savings_reason",
	"savings_basis", "cache", "request_bytes", "response_bytes", "retention_days",
}

func values(r Row) []any {
	return []any{
		r.ProjectID, r.TS, r.Trace, r.OrgID, r.KeyID, r.Developer, r.User, r.Tags, r.Route, r.Conversation, r.TurnKind,
		r.HumanTurns, r.ChainGapMs, r.ChainState, r.FamilyID, r.PrefixHash,
		r.PrefixFlags, r.PrefixTokens, r.EstInput, r.Messages, r.MaxTokens, r.HasTools, r.JSONOutput, r.Rule,
		r.Router, r.Tier, r.DecidedTier, r.TierSource, r.TierConfidence, r.Shadow, r.Effort, r.Healed, r.RouterMicros, r.Decision, r.DecisionMs,
		r.FirewallAction, r.FirewallKinds, r.PayloadRef, r.APIFormat, r.Provider,
		r.Kind, r.RequestedModel, r.ServedModel, r.Backend, r.TargetModel, r.ComputedMicros, r.CacheStrategy, r.CacheTrial, r.Stream, r.Translated, r.Status, r.ErrorCode, r.Attempts, r.TTFBms, r.LatencyMs,
		r.UpstreamMs, r.UsageKnown, r.InputTokens, r.OutputTokens, r.CacheRead, r.CacheWrite, r.Think, r.CostMicros,
		r.Priced, r.Counterfactual, r.Savings, r.SavingsReason, r.SavingsBasis, r.Cache, r.RequestBytes, r.ResponseBytes,
		r.RetentionDays,
	}
}

// Config configures a Log.
type Config struct {
	// Events is ClickHouse; without it calls are costed and counted but
	// not logged.
	Events *events.DB
	Prices prices.Prices
	Spend  *budgets.Spend
	// Entitlements says how long the organization's plan keeps logs
	// (metering.Service.Entitlements, cached in process).
	Entitlements func(ctx context.Context, org uuid.UUID) (billing.Entitlements, error)
	// Payloads keeps the payloads of the calls projects capture (nil:
	// none are kept).
	Payloads *payloads.Store
	// Turns learns what each turn's calls cost and cached, and how long
	// after the conversation's previous call each started (nil: nothing).
	Turns *conversation.Store
}

// Log records finished calls.
type Log struct {
	cfg     Config
	w       *events.Writer[Row]
	fw      *events.Writer[firewall.Event]
	dropped atomic.Int64
	stop    sync.Once
	// writing counts the rows on their way to the writers, which Stop
	// waits for.
	writing sync.WaitGroup
}

// New returns a Log.
func New(cfg Config) *Log {
	l := &Log{cfg: cfg}
	if cfg.Events != nil {
		l.w = events.NewWriter(cfg.Events, events.WriterOptions{Table: Table, Columns: columns}, values)
		l.fw = events.NewWriter(cfg.Events, events.WriterOptions{Table: firewall.EventsTable, Columns: firewall.EventColumns},
			firewall.EventValues)
	}
	return l
}

// Start and Stop run the batch writer (flushing what is left on Stop).
func (l *Log) Start(ctx context.Context) error {
	if l.w == nil {
		return nil
	}
	if err := l.w.Start(ctx); err != nil {
		return err
	}
	return l.fw.Start(ctx)
}

// Stop flushes the rows waiting and stops the writer; later calls do
// nothing.
func (l *Log) Stop(ctx context.Context) error {
	var err error
	l.stop.Do(func() {
		l.writing.Wait()
		if l.w != nil {
			err = errors.Join(l.w.Stop(ctx), l.fw.Stop(ctx))
		}
	})
	return err
}

// Dropped counts rows the log refused while ClickHouse fell behind.
func (l *Log) Dropped() int64 { return l.dropped.Load() }

// Record is the gateway's Done: it costs a call, counts its spend against
// the caller's budgets and writes its row, in the background. Calls the
// gateway refused before resolving a caller (no or a bad key) aren't
// anyone's to log.
func (l *Log) Record(r *gateway.Record) {
	m, ok := r.Meta.(*resolver.Meta)
	if !ok {
		return
	}
	var cost, computed int64
	var led Ledger
	// priced is false when a model without a price served the call and
	// its provider didn't say what it cost: the cost is unknown, so it
	// claims no savings either (a cost of 0 would claim the whole
	// counterfactual).
	priced := true
	if r.UsageKnown {
		if r.Cache != "hit" { // a cached answer cost nothing
			price, known := l.price(m, r.Provider, r.Kind, r.ServedModel)
			if known {
				computed = CostAt(price, r.Usage)
			}
			switch {
			case r.Usage.CostReported:
				cost = int64(math.Round(r.Usage.Cost * 1e6)) // the provider's own figure, as for router_micros
			case known:
				cost = computed
			default:
				priced = false
			}
			l.cfg.Spend.Add(m.Who(), cost)
		}
		if priced {
			led = l.ledger(r, m, cost)
		}
	}
	// A router's decision model spent on the call, whatever became of it.
	var routerMicros int64
	if d := m.Decision; d.Target.Model != "" {
		if d.Micros > 0 {
			routerMicros = d.Micros // the provider's own figure
		} else if price, ok := l.price(m, d.Target.Provider, d.Target.Kind, d.Target.Model); ok {
			routerMicros = CostAt(price, d.Usage)
		}
		l.cfg.Spend.Add(m.Who(), routerMicros)
		led.Savings -= routerMicros
	}
	if l.w == nil {
		return
	}
	row := Row{
		ProjectID: m.Project, OrgID: m.Org, KeyID: m.Key, TS: r.Start.UTC(), Trace: r.Trace, Developer: m.Developer,
		User: m.User, Tags: m.Tags, Route: m.Route, FamilyID: r.Shape.Family, PrefixHash: r.Shape.Prefix,
		PrefixFlags: uint16(flags(r)), PrefixTokens: uint32(min(r.Shape.PrefixTokens, 1<<31)),
		EstInput: uint32(min(r.RequestBytes/4, 1<<31)), Messages: uint32(min(r.Request.Messages, 1<<31)),
		MaxTokens: uint32(min(r.Request.MaxTokens, 1<<31)), HasTools: r.Request.Tools, JSONOutput: r.Request.Prefix.Format != "",
		Rule: m.Rule, Router: m.Router, Tier: m.Tier, DecidedTier: m.Decided, TierSource: m.TierSource,
		TierConfidence: float32(m.TierConfidence), Shadow: m.Shadow, Effort: r.Effort, Healed: r.Healed, RouterMicros: routerMicros, Decision: m.Decision.Outcome,
		DecisionMs: ms(m.Decision.Took), APIFormat: r.Format.String(),
		Provider: r.Provider, Kind: r.Kind, Backend: r.Backend, TargetModel: r.TargetModel, ComputedMicros: computed,
		CacheStrategy: r.CacheStrategy, CacheTrial: r.CacheTrial,
		RequestedModel: r.RequestedModel, ServedModel: r.ServedModel, Stream: r.Stream, Translated: r.Translated, Status: uint16(min(r.Status, 65535)),
		ErrorCode: r.ErrorCode, Attempts: uint8(min(r.Attempts, 255)), TTFBms: ms(r.TTFB), LatencyMs: ms(r.Latency),
		UpstreamMs: ms(r.Upstream), UsageKnown: r.UsageKnown, InputTokens: u32(r.Usage.Input),
		OutputTokens: u32(r.Usage.Output), CacheRead: u32(r.Usage.CacheRead), CacheWrite: u32(r.Usage.CacheWrite),
		Think: u32(r.Usage.Reasoning), CostMicros: cost, Priced: priced, Counterfactual: led.Counterfactual,
		Savings: led.Savings, SavingsReason: led.Reason, SavingsBasis: led.Basis, Cache: r.Cache,
		RequestBytes:  uint32(min(r.RequestBytes, 1<<31)),
		ResponseBytes: uint64(max(r.ResponseBytes, 0)),
	}
	if row.Tags == nil {
		row.Tags = []string{}
	}
	// A served call adds to its turn (in the background, below): an
	// answer from the response cache touched no provider's cache.
	var turn *turnCall
	if m.InTurn {
		row.Conversation, row.HumanTurns = m.Turn.Conversation.String(), uint16(min(m.Turn.HumanTurns, 65535))
		row.TurnKind = TurnStart
		if m.Turn.Continuation {
			row.TurnKind = TurnContinuation
		}
		if r.UsageKnown && r.Cache != "hit" {
			u := r.Usage
			turn = &turnCall{project: m.Project, keys: m.Turn, f: conversation.Finished{Start: r.Start.UnixMilli(),
				Input: int64(u.Input), CacheRead: int64(u.CacheRead), CacheWrite: int64(u.CacheWrite), Output: int64(u.Output),
				Model: r.ServedModel, Provider: r.Provider, Kind: r.Kind, TTL: cacheTTL(u)}}
		}
	}
	var fw []firewall.Event
	row.FirewallKinds = []string{}
	if o := m.Firewall; o != nil {
		var answer []firewall.Hit
		if m.Answer != nil {
			answer = m.Answer.Hits()
		}
		row.FirewallAction = o.Action()
		if row.FirewallAction == "" && len(answer) > 0 {
			row.FirewallAction = firewall.Alert
		}
		row.FirewallKinds = kinds(o.Hits, answer)
		fw = firewall.Events(firewall.Event{ProjectID: m.Project, TS: row.TS, Trace: r.Trace, OrgID: m.Org, KeyID: m.Key,
			Developer: m.Developer, Route: m.Route, RequestedModel: r.RequestedModel, Provider: r.Provider}, o, answer)
	}
	var keep *payloads.Entry
	if m.Capture != nil && r.Body != nil {
		keep = &payloads.Entry{Org: m.Org, Project: m.Project, At: row.TS, Capture: m.Capture, Format: r.Format,
			Stream: r.Stream, Status: r.Status, Request: r.Body, Answer: r.Answer}
	}
	l.writing.Go(func() { l.write(m.Org, row, fw, keep, turn) })
}

// Turn kinds: a call that starts its turn (answers a human message), or one
// that continues it (an agent's tool loop).
const (
	TurnStart        = "start"
	TurnContinuation = "continuation"
)

// turnCall is a served call to add to its turn.
type turnCall struct {
	project uuid.UUID
	keys    conversation.Keys
	f       conversation.Finished
}

// cacheTTL is how long a call's prompt cache entries live: an hour when it
// wrote one-hour entries, five minutes when it read or wrote others, ""
// when it used no cache.
func cacheTTL(u wire.Usage) string {
	switch {
	case u.CacheWrite1h > 0:
		return "1h"
	case u.CacheRead > 0 || u.CacheWrite > 0:
		return "5m"
	}
	return ""
}

// kinds are the detectors of hits, sorted, each once.
func kinds(hits ...[]firewall.Hit) []string {
	out := []string{}
	for _, hs := range hits {
		for _, h := range hs {
			out = append(out, h.Detector)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// price is a model's price for the project: its own (for that provider,
// then for any), else the price list's.
func (l *Log) price(m *resolver.Meta, provider, kind, model string) (llm.Model, bool) {
	return Price(l.cfg.Prices, m.Prices, provider, kind, model)
}

// Price is a model's price for a project: its own (for that provider, then
// for any), else the price list's.
func Price(list prices.Prices, own prices.Project, provider, kind, model string) (llm.Model, bool) {
	if p, ok := own.Lookup(provider, model); ok {
		return p, true
	}
	catalogName := ""
	if k, ok := providers.LookupKind(kind); ok {
		catalogName = k.Catalog
	}
	return list.Current().Lookup(catalogName, model)
}

// ledger weighs a call against the model asked for at list price, without
// any cache: a call that named a model router, against the router's
// default tier. A model nobody prices claims no savings.
func (l *Log) ledger(r *gateway.Record, m *resolver.Meta, cost int64) Ledger {
	model := r.RequestedModel
	rerouted := r.TargetModel != "" && r.TargetModel != model
	provider, kind := r.Provider, r.Kind
	if rerouted {
		provider, kind = "", "" // the model asked for might be anyone's
	}
	if b := m.Baseline; b.Model != "" {
		model, provider, kind = b.Model, b.Provider, b.Kind
		rerouted = r.TargetModel != "" && (r.TargetModel != b.Model || r.Provider != b.Provider)
	}
	price, ok := l.price(m, provider, kind, model)
	if !ok {
		return Ledger{Counterfactual: cost}
	}
	led := Ledger{Counterfactual: counterfactual(price, r.Usage)}
	// A router's call whose tier's writing was measured against the
	// default tier: the counterfactual writes what the default would have.
	measured := rerouted && !r.Fallback && r.Cache != "hit" && m.OutputRatio > 0
	if measured {
		u := r.Usage
		u.Output = uint64(math.Round(float64(u.Output) * m.OutputRatio))
		led.Counterfactual = counterfactual(price, u)
	}
	led.Savings = led.Counterfactual - cost
	switch {
	case r.Cache == "hit":
		led.Reason, led.Basis = ReasonResponseCache, BasisCache
	case rerouted && r.Fallback:
		led.Reason, led.Basis = ReasonFallback, BasisEstimate
	case measured:
		led.Reason, led.Basis = ReasonRouting, BasisReplays
	case rerouted:
		led.Reason, led.Basis = ReasonRouting, BasisEstimate
	case r.Usage.CacheRead > 0 || r.Usage.CacheWrite > 0:
		led.Reason, led.Basis = ReasonPromptCache, BasisProvider
	}
	return led
}

func (l *Log) write(org uuid.UUID, row Row, fw []firewall.Event, keep *payloads.Entry, turn *turnCall) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if turn != nil && l.cfg.Turns != nil {
		gap, state := l.cfg.Turns.Finish(ctx, turn.project, turn.keys.Turn, turn.keys.Prev, turn.f)
		row.ChainGapMs, row.ChainState = uint32(min(max(gap, 0), 1<<31)), state
	}
	row.RetentionDays = defaultRetention
	if e, err := l.cfg.Entitlements(ctx, org); err == nil {
		if days, ok := e.Limit(catalog.RetentionDays); ok && days > 0 {
			row.RetentionDays = uint16(min(days, 3650))
		}
	}
	if keep != nil {
		// Masked and sealed here, off the call's path; kept as long as
		// the plan keeps the row.
		keep.RetentionDays = int(row.RetentionDays)
		row.PayloadRef = l.cfg.Payloads.Add(ctx, *keep)
	}
	if err := l.w.Write(ctx, row); err != nil {
		if n := l.dropped.Add(1); n == 1 || n%1000 == 0 {
			slog.Warn("requestlog: a row was not written", "dropped", n, "err", err)
		}
	}
	for _, e := range fw {
		e.RetentionDays = row.RetentionDays
		if err := l.fw.Write(ctx, e); err != nil {
			if n := l.dropped.Add(1); n == 1 || n%1000 == 0 {
				slog.Warn("requestlog: a firewall event was not written", "dropped", n, "err", err)
			}
		}
	}
}

// flags are the call's prefix flags, with the breakpoints the gateway added.
func flags(r *gateway.Record) families.Flags {
	f := r.Shape.Flags
	if r.PromptCached {
		f |= families.GatewayBreakpoints
	}
	return f
}

func ms(d time.Duration) uint32 { return uint32(min(max(d.Milliseconds(), 0), 1<<31)) }

func u32(n uint64) uint32 { return uint32(min(n, 1<<32-1)) }

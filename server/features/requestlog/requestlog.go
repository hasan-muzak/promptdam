// Package requestlog turns each finished gateway call into its record:
// what it cost (counted against the caller's budgets) and a row of the
// request log in ClickHouse, written in batches off the call's path.
package requestlog

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/billing"
	"github.com/hasan-muzak/keel-core/pkg/events"
	"github.com/hasan-muzak/keel-core/pkg/llm"

	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/features/budgets"
	"github.com/hasan-muzak/promptdam/server/features/families"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
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
	FamilyID, PrefixHash                                    string
	PrefixFlags                                             uint16
	PrefixTokens, EstInput, Messages, MaxTokens             uint32
	HasTools, JSONOutput                                    bool
	Rule                                                    string
	Tags                                                    []string
	APIFormat, Provider, Kind, RequestedModel, ServedModel  string
	Stream, Translated                                      bool
	Status                                                  uint16
	ErrorCode                                               string
	Attempts                                                uint8
	TTFBms, LatencyMs, UpstreamMs                           uint32
	UsageKnown                                              bool
	InputTokens, OutputTokens, CacheRead, CacheWrite, Think uint32
	CostMicros, Counterfactual, Savings                     int64
	SavingsReason, Cache                                    string
	RequestBytes                                            uint32
	ResponseBytes                                           uint64
	RetentionDays                                           uint16
}

var columns = []string{
	"project_id", "ts", "trace", "org_id", "key_id", "developer", "end_user", "tags", "route", "family_id", "prefix_hash",
	"prefix_flags", "prefix_tokens", "est_input_tokens", "messages", "max_tokens", "has_tools", "json_output", "rule",
	"api_format", "provider",
	"kind", "requested_model", "served_model", "stream", "translated", "status", "error_code", "attempts", "ttfb_ms", "latency_ms",
	"upstream_ms", "usage_known", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens",
	"reasoning_tokens", "cost_micros", "counterfactual_micros", "savings_micros", "savings_reason", "cache",
	"request_bytes", "response_bytes", "retention_days",
}

func values(r Row) []any {
	return []any{
		r.ProjectID, r.TS, r.Trace, r.OrgID, r.KeyID, r.Developer, r.User, r.Tags, r.Route, r.FamilyID, r.PrefixHash,
		r.PrefixFlags, r.PrefixTokens, r.EstInput, r.Messages, r.MaxTokens, r.HasTools, r.JSONOutput, r.Rule,
		r.APIFormat, r.Provider,
		r.Kind, r.RequestedModel, r.ServedModel, r.Stream, r.Translated, r.Status, r.ErrorCode, r.Attempts, r.TTFBms, r.LatencyMs,
		r.UpstreamMs, r.UsageKnown, r.InputTokens, r.OutputTokens, r.CacheRead, r.CacheWrite, r.Think, r.CostMicros,
		r.Counterfactual, r.Savings, r.SavingsReason, r.Cache, r.RequestBytes, r.ResponseBytes, r.RetentionDays,
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
}

// Log records finished calls.
type Log struct {
	cfg     Config
	w       *events.Writer[Row]
	dropped atomic.Int64
	stop    sync.Once
}

// New returns a Log.
func New(cfg Config) *Log {
	l := &Log{cfg: cfg}
	if cfg.Events != nil {
		l.w = events.NewWriter(cfg.Events, events.WriterOptions{Table: Table, Columns: columns}, values)
	}
	return l
}

// Start and Stop run the batch writer (flushing what is left on Stop).
func (l *Log) Start(ctx context.Context) error {
	if l.w == nil {
		return nil
	}
	return l.w.Start(ctx)
}

// Stop flushes the rows waiting and stops the writer; later calls do
// nothing.
func (l *Log) Stop(ctx context.Context) error {
	var err error
	l.stop.Do(func() {
		if l.w != nil {
			err = l.w.Stop(ctx)
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
	var cost int64
	var led Ledger
	if r.UsageKnown {
		if r.Cache != "hit" { // a cached answer cost nothing
			if price, ok := l.price(m, r.Provider, r.Kind, r.ServedModel); ok {
				cost = CostAt(price, r.Usage)
			}
			l.cfg.Spend.Add(m.Who(), cost)
		}
		led = l.ledger(r, m, cost)
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
		Rule: m.Rule, APIFormat: r.Format.String(),
		Provider: r.Provider, Kind: r.Kind,
		RequestedModel: r.RequestedModel, ServedModel: r.ServedModel, Stream: r.Stream, Translated: r.Translated, Status: uint16(min(r.Status, 65535)),
		ErrorCode: r.ErrorCode, Attempts: uint8(min(r.Attempts, 255)), TTFBms: ms(r.TTFB), LatencyMs: ms(r.Latency),
		UpstreamMs: ms(r.Upstream), UsageKnown: r.UsageKnown, InputTokens: u32(r.Usage.Input),
		OutputTokens: u32(r.Usage.Output), CacheRead: u32(r.Usage.CacheRead), CacheWrite: u32(r.Usage.CacheWrite),
		Think: u32(r.Usage.Reasoning), CostMicros: cost, Counterfactual: led.Counterfactual, Savings: led.Savings,
		SavingsReason: led.Reason, Cache: r.Cache, RequestBytes: uint32(min(r.RequestBytes, 1<<31)),
		ResponseBytes: uint64(max(r.ResponseBytes, 0)),
	}
	if row.Tags == nil {
		row.Tags = []string{}
	}
	go l.write(m.Org, row)
}

// price is a model's price for the project: its own (for that provider,
// then for any), else the price list's.
func (l *Log) price(m *resolver.Meta, provider, kind, model string) (llm.Model, bool) {
	if p, ok := m.Prices.Lookup(provider, model); ok {
		return p, true
	}
	catalogName := ""
	if k, ok := providers.LookupKind(kind); ok {
		catalogName = k.Catalog
	}
	return l.cfg.Prices.Current().Lookup(catalogName, model)
}

// ledger weighs a call against the model asked for at list price, without
// any cache. A model nobody prices claims no savings.
func (l *Log) ledger(r *gateway.Record, m *resolver.Meta, cost int64) Ledger {
	rerouted := r.TargetModel != "" && r.TargetModel != r.RequestedModel
	provider, kind := r.Provider, r.Kind
	if rerouted {
		provider, kind = "", "" // the model asked for might be anyone's
	}
	price, ok := l.price(m, provider, kind, r.RequestedModel)
	if !ok {
		return Ledger{Counterfactual: cost}
	}
	led := Ledger{Counterfactual: counterfactual(price, r.Usage)}
	led.Savings = led.Counterfactual - cost
	switch {
	case r.Cache == "hit":
		led.Reason = ReasonResponseCache
	case rerouted && r.Fallback:
		led.Reason = ReasonFallback
	case rerouted:
		led.Reason = ReasonRouting
	case r.Usage.CacheRead > 0 || r.Usage.CacheWrite > 0:
		led.Reason = ReasonPromptCache
	}
	return led
}

func (l *Log) write(org uuid.UUID, row Row) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	row.RetentionDays = defaultRetention
	if e, err := l.cfg.Entitlements(ctx, org); err == nil {
		if days, ok := e.Limit(catalog.RetentionDays); ok && days > 0 {
			row.RetentionDays = uint16(min(days, 3650))
		}
	}
	if err := l.w.Write(ctx, row); err != nil {
		if n := l.dropped.Add(1); n == 1 || n%1000 == 0 {
			slog.Warn("requestlog: a row was not written", "dropped", n, "err", err)
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

// Package evaluate measures a model router's tiers on the project's own
// calls (project.md §6.2). A sample of a router's calls is replayed, in the
// gateway's memory and off the call's path, on the router's other tiers,
// and each answer is compared with the default tier's: checks first, then
// a pairwise judge, the answers in random order. Every verdict goes to
// ClickHouse's router_evals with the call's features (hashes, never text);
// requests and answers are never stored.
//
// Replays and the judge run on the project's own provider keys, within the
// router's monthly evaluation budget, and count as the project's spend.
package evaluate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/billing"
	"github.com/hasan-muzak/keel-core/pkg/counter"
	"github.com/hasan-muzak/keel-core/pkg/events"
	"github.com/hasan-muzak/keel-core/pkg/llm"
	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/features/budgets"
	"github.com/hasan-muzak/promptdam/server/features/classifier"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
	"github.com/hasan-muzak/promptdam/server/features/prices"
	"github.com/hasan-muzak/promptdam/server/features/requestlog"
	"github.com/hasan-muzak/promptdam/server/features/resolver"
	"github.com/hasan-muzak/promptdam/server/features/routers"
	"github.com/hasan-muzak/promptdam/server/features/routers/tiers"
	"github.com/hasan-muzak/promptdam/server/features/snapshot"
	"github.com/hasan-muzak/promptdam/server/features/translate"
)

// Table holds the verdicts.
const Table = "router_evals"

// DefaultJudge judges for a router that names no judge, when one of the
// project's providers serves it.
const DefaultJudge = "claude-sonnet-5-5"

// CheckJudgeError is the check of a judge's answer that couldn't be read
// (no verdict).
const CheckJudgeError = "judge_error"

// Row is one verdict: a sampled call's answer on one tier against the
// default tier's.
type Row struct {
	ProjectID, OrgID                    uuid.UUID
	TS                                  time.Time
	Router, Trace, Family               string
	ServedTier, DecidedTier, TierSource string
	BaselineTier, BaselineModel         string
	CandidateTier, CandidateModel       string
	CandidateRank                       int8
	Verdict, Check, JudgeModel          string
	// Consistent is false when the judge, asked in both orders
	// (BothOrders), disagreed with itself (the verdict is then a tie).
	Consistent, BothOrders                       bool
	CandidateMicros, BaselineMicros, JudgeMicros int64
	// CandidateOutput and BaselineOutput are the two answers' output
	// tokens: what the ledger learns each tier writes against the default.
	CandidateOutput, BaselineOutput uint32
	Features                        []uint32
	RetentionDays                   uint16
}

var columns = []string{
	"project_id", "ts", "org_id", "router", "trace", "family_id", "served_tier", "decided_tier", "tier_source",
	"baseline_tier", "baseline_model", "candidate_tier", "candidate_model", "candidate_rank", "verdict", "check",
	"judge_model", "consistent", "both_orders", "candidate_micros", "baseline_micros", "judge_micros", "candidate_output_tokens",
	"baseline_output_tokens", "features", "retention_days",
}

func values(r Row) []any {
	return []any{
		r.ProjectID, r.TS, r.OrgID, r.Router, r.Trace, r.Family, r.ServedTier, r.DecidedTier, r.TierSource,
		r.BaselineTier, r.BaselineModel, r.CandidateTier, r.CandidateModel, r.CandidateRank, r.Verdict, r.Check,
		r.JudgeModel, r.Consistent, r.BothOrders, r.CandidateMicros, r.BaselineMicros, r.JudgeMicros, r.CandidateOutput, r.BaselineOutput,
		r.Features, r.RetentionDays,
	}
}

// Config configures an Evaluator.
type Config struct {
	// Resolver finds tiers' and judges' targets among the project's
	// providers, and what their models take.
	Resolver *resolver.Resolver
	// Transport sends the calls (safehttp.NewTransport).
	Transport http.RoundTripper
	// Events is ClickHouse; without it nothing is evaluated.
	Events *events.DB
	Prices prices.Prices
	Spend  *budgets.Spend
	// Counters counts each router's evaluation spend a month.
	Counters *counter.Counters
	// Entitlements says how long the plan keeps the rows.
	Entitlements func(ctx context.Context, org uuid.UUID) (billing.Entitlements, error)
	// Queue bounds the calls waiting (default 64): past it, samples are
	// dropped. Workers evaluate at once (default 4).
	Queue, Workers int
	// Both is the share of judged pairs judged again in the other order
	// (default 0.1): a disagreement makes a tie.
	Both float64
	// Rand draws in [0, 1) (default rand.Float64).
	Rand func() float64
	// Timeout bounds each model call (default 2 minutes).
	Timeout time.Duration
}

// Evaluator evaluates sampled router calls in the background.
type Evaluator struct {
	cfg    Config
	jobs   chan *gateway.Record
	w      *events.Writer[Row]
	client *http.Client

	dropped, evaluated atomic.Int64
	wg                 sync.WaitGroup
	cancel             context.CancelFunc
	stop               sync.Once
}

// New returns an Evaluator.
func New(cfg Config) *Evaluator {
	if cfg.Queue <= 0 {
		cfg.Queue = 64
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	if cfg.Both == 0 {
		cfg.Both = 0.1
	}
	if cfg.Rand == nil {
		cfg.Rand = rand.Float64
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 2 * time.Minute
	}
	// Redirects aren't followed: a provider's key goes to its own endpoint
	// only (net/http keeps X-Api-Key on a redirect to another host).
	client := &http.Client{Transport: cfg.Transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	e := &Evaluator{cfg: cfg, jobs: make(chan *gateway.Record, cfg.Queue), client: client}
	if cfg.Events != nil {
		e.w = events.NewWriter(cfg.Events, events.WriterOptions{Table: Table, Columns: columns}, values)
	}
	return e
}

// Offer queues a finished call for evaluation when its router sampled it.
// It never blocks: a sample that finds the queue full is dropped.
func (e *Evaluator) Offer(r *gateway.Record) {
	m, ok := r.Meta.(*resolver.Meta)
	if !ok || !m.Sampled || e.w == nil || r.Body == nil || r.Status != http.StatusOK || r.Cache == "hit" {
		return
	}
	rec := *r
	select {
	case e.jobs <- &rec:
	default:
		e.dropped.Add(1)
	}
}

// Dropped counts samples dropped (a full queue, a spent budget).
func (e *Evaluator) Dropped() int64 { return e.dropped.Load() }

// Evaluated counts samples evaluated.
func (e *Evaluator) Evaluated() int64 { return e.evaluated.Load() }

// Start runs the workers and the row writer.
func (e *Evaluator) Start(ctx context.Context) error {
	if e.w == nil {
		return nil
	}
	if err := e.w.Start(ctx); err != nil {
		return err
	}
	wctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	for range e.cfg.Workers {
		e.wg.Go(func() {
			for {
				select {
				case <-wctx.Done():
					return
				case rec := <-e.jobs:
					e.run(wctx, rec)
				}
			}
		})
	}
	return nil
}

// Stop drops the samples still waiting, stops the workers (cancelling
// their calls) and flushes the rows written.
func (e *Evaluator) Stop(ctx context.Context) error {
	var err error
	e.stop.Do(func() {
		if e.cancel == nil {
			return
		}
		e.cancel()
		e.wg.Wait()
		err = e.w.Stop(ctx)
	})
	return err
}

// tierRun is a tier's answer to a sampled call.
type tierRun struct {
	answer Answer
	target gateway.Target
	// micros is what getting the answer cost the evaluation (0 for the
	// answer the client got); output is its output tokens (0: unknown).
	micros int64
	output uint64
	err    error
}

// run evaluates one sampled call.
func (e *Evaluator) run(ctx context.Context, rec *gateway.Record) {
	m := rec.Meta.(*resolver.Meta)
	rt, snap := m.Placed, m.Snap
	spec := &rt.Spec
	budgetKey := m.Project.String() + ":" + rt.Name
	if spent, err := e.cfg.Counters.Value(ctx, budgetKey, counter.Month); err == nil && spent >= rt.EvalBudgetMicros {
		e.dropped.Add(1)
		return
	}
	def, served := spec.Index(spec.Default), spec.Index(m.Tier)
	if def < 0 || served < 0 {
		return
	}

	runs := make([]*tierRun, len(spec.Tiers))
	if rec.Answer != nil {
		if a, err := answerOf(rec.Format, rec.Answer, rec.Stream); err == nil {
			runs[served] = &tierRun{answer: a, target: gateway.Target{Provider: rec.Provider, Kind: rec.Kind, Model: rec.TargetModel}}
			if rec.UsageKnown {
				runs[served].output = rec.Usage.Output
			}
		}
	}
	// The other tiers answer at once, each on its own model and fallbacks.
	var wg sync.WaitGroup
	for i := range runs {
		if runs[i] != nil {
			continue
		}
		run := &tierRun{}
		runs[i] = run
		wg.Go(func() { *run = e.replay(ctx, m, snap, spec.Tiers[i], rec.Format, rec.Body) })
	}
	wg.Wait()
	if ctx.Err() != nil {
		return
	}

	base := runs[def]
	total := base.micros
	if base.err != nil {
		// Without the default tier's answer there is nothing to judge
		// against; what was spent still counts.
		e.account(m, budgetKey, total+sumMicros(runs, def))
		slog.Debug("evaluate: the default tier failed on a sample", "router", rt.Name, "err", base.err)
		return
	}
	judge, judgeModel := e.judgeTarget(snap, rt)
	ask := rec.Request.Turn.Text
	features := classifier.Features(classifier.InputOf(rec.Request, rec.Shape.Family, m.Tags))
	retention := e.retention(ctx, m.Org)
	wantJSON := rec.Request.Prefix.Format != ""
	now := time.Now().UTC()

	rows := make([]Row, 0, len(runs)-1)
	for i, run := range runs {
		if i == def {
			continue
		}
		row := Row{ProjectID: m.Project, OrgID: m.Org, TS: now, Router: rt.Name, Trace: rec.Trace, Family: rec.Shape.Family,
			ServedTier: m.Tier, DecidedTier: m.Decided, TierSource: m.TierSource, BaselineTier: spec.Default,
			BaselineModel: base.target.Model, CandidateTier: spec.Tiers[i].Name, CandidateModel: run.target.Model,
			CandidateRank: int8(i - def), CandidateMicros: run.micros, Consistent: true, Features: features,
			RetentionDays: retention}
		if len(rows) == 0 {
			row.BaselineMicros = base.micros
		}
		if run.err == nil {
			row.CandidateOutput, row.BaselineOutput = uint32(min(run.output, 1<<31)), uint32(min(base.output, 1<<31))
		}
		total += run.micros
		switch v, how, ok := check(base.answer, run.answer, wantJSON); {
		case run.err != nil:
			row.Verdict, row.Check = Loss, CheckError
		case ok:
			row.Verdict, row.Check = v, how
		case judge == nil:
			row.Check = CheckNoJudge
		default:
			row.Check, row.JudgeModel = CheckJudge, judgeModel
			verdict, consistent, twice, micros, err := e.judge(ctx, m, *judge, rec.Request.Prefix.System, ask, base.answer,
				run.answer, e.bothRate(rt))
			row.BothOrders = twice
			row.JudgeMicros, total = micros, total+micros
			if err != nil {
				row.Check = CheckJudgeError
				slog.Debug("evaluate: the judge failed", "router", rt.Name, "err", err)
			} else {
				row.Verdict, row.Consistent = verdict, consistent
			}
		}
		rows = append(rows, row)
	}
	e.account(m, budgetKey, total)
	for _, row := range rows {
		if err := e.w.Write(ctx, row); err != nil {
			slog.Warn("evaluate: a verdict was not written", "err", err)
		}
	}
	e.evaluated.Add(1)
}

func sumMicros(runs []*tierRun, skip int) int64 {
	var n int64
	for i, r := range runs {
		if i != skip {
			n += r.micros
		}
	}
	return n
}

// account counts what evaluating a call cost against the router's monthly
// budget and the project's spend.
func (e *Evaluator) account(m *resolver.Meta, key string, micros int64) {
	if micros <= 0 {
		return
	}
	e.cfg.Counters.Add(key, counter.Month, micros)
	if e.cfg.Spend != nil {
		e.cfg.Spend.Add(budgets.Who{Project: m.Project}, micros)
	}
}

func (e *Evaluator) retention(ctx context.Context, org uuid.UUID) uint16 {
	if e.cfg.Entitlements != nil {
		if ent, err := e.cfg.Entitlements(ctx, org); err == nil {
			if days, ok := ent.Limit(catalog.RetentionDays); ok && days > 0 {
				return uint16(min(days, 3650))
			}
		}
	}
	return 7
}

// replay sends a sampled call, not streamed, to a tier: its model, then
// its fallbacks, until one answers.
func (e *Evaluator) replay(ctx context.Context, m *resolver.Meta, snap *snapshot.Snapshot, t tiers.Tier, f wire.Format, body []byte) tierRun {
	var targets []gateway.Target
	for _, model := range append([]string{t.Model}, t.Fallbacks...) {
		for _, tg := range e.cfg.Resolver.Targets(snap, f, model) {
			// A replay runs at the tier's effort, as the tier's calls do.
			targets = append(targets, e.cfg.Resolver.WithEffort(tg, t.Effort))
		}
	}
	if len(targets) == 0 {
		return tierRun{err: errors.New("evaluate: no provider serves tier " + t.Name)}
	}
	var out tierRun
	for _, tg := range targets {
		a, usage, status, err := e.callTier(ctx, tg, f, body)
		out = tierRun{answer: a, target: tg, micros: e.cost(m, tg, usage), output: usage.Output, err: err}
		if err == nil || !retryable(status) {
			return out
		}
	}
	return out
}

func retryable(status int) bool {
	return status == 0 || status == http.StatusTooManyRequests || status >= 500
}

// cost is what a call cost at the project's prices.
func (e *Evaluator) cost(m *resolver.Meta, t gateway.Target, u wire.Usage) int64 {
	if p, ok := requestlog.Price(e.cfg.Prices, m.Prices, t.Provider, t.Kind, t.Model); ok {
		return requestlog.CostAt(p, u)
	}
	return 0
}

// callTier sends a call of format f to a target, translated when the
// target speaks the other API, not streamed.
func (e *Evaluator) callTier(ctx context.Context, t gateway.Target, f wire.Format, body []byte) (Answer, wire.Usage, int, error) {
	payload, err := unstreamed(body)
	if err != nil {
		return Answer{}, wire.Usage{}, 0, err
	}
	tf := f
	if t.Format != 0 && t.Format != f {
		tf = t.Format
		if payload, _, err = translate.Request(f, tf, payload, translate.Options{Model: t.Model, Kind: t.Kind,
			Reasoning: t.Reasoning, MaxTokens: t.MaxTokens}); err != nil {
			return Answer{}, wire.Usage{}, http.StatusBadRequest, err
		}
	} else {
		model, _ := json.Marshal(t.Model)
		if payload, err = wire.SetField(payload, "model", model); err != nil {
			return Answer{}, wire.Usage{}, 0, err
		}
	}
	payload, _, _ = gateway.ApplyEffort(payload, tf, t, true)
	if t.NoSampling {
		payload, _ = translate.StripSampling(payload)
	}
	resp, status, err := e.send(ctx, t, tf, payload)
	if err != nil {
		return Answer{}, wire.Usage{}, status, err
	}
	meter := wire.NewMeter(tf)
	meter.Body(resp)
	usage, _ := meter.Usage()
	a, err := answerOf(tf, resp, false)
	return a, usage, status, err
}

// unstreamed is a request body asking for a whole answer.
func unstreamed(body []byte) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, err
	}
	if _, ok := top["stream"]; !ok {
		return body, nil
	}
	delete(top, "stream")
	delete(top, "stream_options")
	return marshal(top)
}

// marshal encodes v as providers write JSON: without escaping <, > and &.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// maxAnswerBody bounds what is read of a provider's answer.
const maxAnswerBody = 16 << 20

// send posts a request in format f to a target and returns a successful
// answer's body.
func (e *Evaluator) send(ctx context.Context, t gateway.Target, f wire.Format, payload []byte) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, e.cfg.Timeout)
	defer cancel()
	path := "/chat/completions"
	if f.Anthropic() {
		path = "/messages"
	}
	payload, _, err := gateway.CloudBody(t, f, path, payload)
	if err != nil {
		return nil, 0, fmt.Errorf("evaluate: %s can't take the call: %w", t.Provider, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, gateway.UpstreamURL(t, f, path, false), bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	if f.Anthropic() {
		req.Header.Set("Anthropic-Version", "2023-06-01")
	}
	for k, v := range t.Header {
		req.Header[k] = v
	}
	gateway.CloudHeader(t, f, req.Header, false)
	if err := gateway.Sign(ctx, t, req, payload); err != nil {
		return nil, 0, fmt.Errorf("evaluate: signing for %s: %w", t.Provider, err)
	}
	res, err := e.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("evaluate: calling %s: %w", t.Provider, err)
	}
	gateway.CloudResponse(t, f, res)
	defer func() { _ = res.Body.Close() }()
	resp, err := io.ReadAll(io.LimitReader(res.Body, maxAnswerBody))
	if err != nil {
		return nil, res.StatusCode, fmt.Errorf("evaluate: reading %s's answer: %w", t.Provider, err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, res.StatusCode, fmt.Errorf("evaluate: %s answered %d", t.Provider, res.StatusCode)
	}
	return resp, res.StatusCode, nil
}

// judgeTarget is the router's judge among the project's providers: the
// model it names, else DefaultJudge when a provider serves it; nil when
// none does.
func (e *Evaluator) judgeTarget(snap *snapshot.Snapshot, rt *routers.Router) (*gateway.Target, string) {
	name := rt.Judge
	if name == "" {
		name = DefaultJudge
	}
	ts := e.cfg.Resolver.Targets(snap, wire.OpenAIChat, name)
	if len(ts) == 0 {
		return nil, ""
	}
	return &ts[0], ts[0].Model
}

// judge compares a candidate's answer with the default tier's: the answers
// in random order, and a share of pairs again in the other order, a
// disagreement making a tie. The verdict is the candidate's.
// bothRate is the share of a router's verdicts judged in both orders:
// every one when its judge was found to flip too often (training), else
// the evaluator's sample.
func (e *Evaluator) bothRate(rt *routers.Router) float64 {
	if rt.Classifier != nil && rt.Classifier.JudgeBoth {
		return 1
	}
	return e.cfg.Both
}

func (e *Evaluator) judge(ctx context.Context, m *resolver.Meta, t gateway.Target, instructions, request string, base, cand Answer,
	both float64,
) (verdict string, consistent, twice bool, micros int64, err error) {
	candFirst := e.cfg.Rand() < 0.5
	ask := func(candFirst bool) (string, error) {
		a, b := base, cand
		if candFirst {
			a, b = cand, base
		}
		better, cost, err := e.judgeOnce(ctx, t, judgePrompt(instructions, request, a, b))
		micros += e.cost(m, t, cost)
		if err != nil {
			return "", err
		}
		switch {
		case better == "tie":
			return Tie, nil
		case (better == "a") == candFirst:
			return Win, nil
		}
		return Loss, nil
	}
	if verdict, err = ask(candFirst); err != nil {
		return "", false, false, micros, err
	}
	if both >= 1 || e.cfg.Rand() < both {
		again, err := ask(!candFirst)
		if err == nil {
			if again != verdict {
				return Tie, false, true, micros, nil
			}
			return verdict, true, true, micros, nil
		}
	}
	return verdict, true, false, micros, nil
}

// judgeOnce asks the judge which of two answers is better: "a", "b" or
// "tie". Its answer is held to a schema where the model takes one.
func (e *Evaluator) judgeOnce(ctx context.Context, t gateway.Target, prompt string) (string, wire.Usage, error) {
	f := wire.OpenAIChat
	if t.Format != 0 {
		f = t.Format
	}
	md, known := e.cfg.Resolver.Model(t.Kind, t.Model)
	schema, _ := md.Has(llm.FeatureJSONSchema)
	schema = schema || !known || md.Features == nil
	var req map[string]any
	if f.Anthropic() {
		limit := 1024
		if md.ThinkingOff != "" || md.AlwaysThinks {
			limit = 4096 // the model thinks first
		}
		req = map[string]any{"model": t.Model, "max_tokens": limit, "system": judgeSystem,
			"messages": []map[string]string{{"role": "user", "content": prompt}}}
		if schema {
			req["output_config"] = map[string]any{"format": map[string]any{"type": "json_schema", "schema": verdictSchema}}
		}
	} else {
		req = map[string]any{"model": t.Model, "messages": []map[string]string{
			{"role": "system", "content": judgeSystem}, {"role": "user", "content": prompt}}}
		reasons, _ := md.Has(llm.FeatureReasoning)
		switch {
		case translate.MaxCompletionTokens(t.Kind):
			req["max_completion_tokens"] = 4096 // reasoning models think first
		case reasons:
			req["max_tokens"] = 4096
		default:
			req["max_tokens"] = 1024
		}
		if schema {
			req["response_format"] = map[string]any{"type": "json_schema",
				"json_schema": map[string]any{"name": "verdict", "schema": verdictSchema, "strict": true}}
		}
	}
	payload, err := marshal(req)
	if err != nil {
		return "", wire.Usage{}, err
	}
	resp, status, err := e.send(ctx, t, f, payload)
	if err != nil {
		return "", wire.Usage{}, fmt.Errorf("evaluate: the judge (%d): %w", status, err)
	}
	meter := wire.NewMeter(f)
	meter.Body(resp)
	usage, _ := meter.Usage()
	a, err := answerOf(f, resp, false)
	if err != nil {
		return "", usage, err
	}
	better := readVerdict(a.Text)
	if better == "" {
		// Not quoted: the judge's answer may repeat the call.
		return "", usage, fmt.Errorf("evaluate: the judge's answer (%d bytes) has no verdict", len(a.Text))
	}
	return better, usage, nil
}

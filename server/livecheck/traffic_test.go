package livecheck_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/events"
	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/features/analytics"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/promptdam/server/features/requestlog"
	"github.com/hasan-muzak/promptdam/server/features/routers"
)

// TestCustomsTraffic is CustomsPilot's traffic for half an hour: ten users
// (brokers, accountants, an importer, a forwarder, an online seller) chat
// at once, arriving at different times. Each reads an answer and types the
// next message before sending it, and seven take a break longer than the
// provider's five-minute prompt cache. The questions mix easy and hard in
// different shares per user.
//
// Run: task demo:traffic (PROMPTDAM_TEST_ANTHROPIC_KEY in .env, the stack
// up; about $1 and 30 minutes; it stops past maxTrafficSpend), or on
// OpenRouter's models with PROMPTDAM_DEMO_STACK=openrouter (trafficStack).
// Without the stack's key it runs on the fake provider, unpaced, to check
// the harness. PROMPTDAM_DEMO_PACE scales the pauses (1 by default),
// PROMPTDAM_DEMO_USERS picks users (comma-separated); a transcript goes to
// PROMPTDAM_DEMO_TRANSCRIPT.
const maxTrafficSpend = 3.00 // dollars

// A chat: who, when they open it, and their messages, each sent after a
// pause (reading the last answer, typing).
type chat struct {
	user, who string
	opens     time.Duration
	turns     []message
}

type message struct {
	pause time.Duration
	ask   string
}

var chats = []chat{
	{"ayse", "CPA and customs broker, a client founding an importer", 0, []message{
		{0, "Hi, my client is setting up a company to import spare parts for textile machinery. What legal forms of company are there in Turkey? A short list, please."},
		{75 * time.Second, "Can you compare a single-partner limited company and a single-shareholder joint-stock company on liability, set-up cost, accounting burden and profit distribution?"},
		{40 * time.Second, "What was the minimum capital for a joint-stock company again?"},
		{9 * time.Minute, "The client expects TRY 8 million of imports and TRY 900,000 of pre-tax profit in the first year. If all of the profit is distributed, roughly what is the total tax burden in a limited company? State your assumptions."},
		{2 * time.Minute, "And as a sole proprietorship, roughly what income tax would the same profit pay? I know I must check the current brackets; a rough calculation is fine."},
		{60 * time.Second, "I'll send this comparison to the client. Can you write a plain five-point summary?"},
	}},
	{"mehmet", "customs broker, classifying headphones", 2 * time.Minute, []message{
		{0, "My client will import wireless Bluetooth headphones from China. Can you suggest a tariff code? Check the tariff database."},
		{50 * time.Second, "Can you get the rates for this code with China origin?"},
		{100 * time.Second, "If the charging case is invoiced separately, does the classification change? Explain why."},
		{6*time.Minute + 30*time.Second, "What should I watch out for regarding TAREKS and CE marking?"},
		{70 * time.Second, "Can you write a short email to the client listing the documents we need?"},
		{45 * time.Second, "Thanks. Finally, can you sum up what to do for this import in five points?"},
	}},
	{"zeynep", "importer's trade specialist, costing a CNC machine", 4 * time.Minute, []message{
		{0, "We're importing a CNC machine worth EUR 120,000 from Germany, delivery terms EXW Stuttgart. Freight EUR 6,800, insurance EUR 900. How do I find the customs value?"},
		{90 * time.Second, "The origin is the EU and we have an A.TR certificate. Assume EUR 1 = TRY 44.80; with 0% customs duty and 20% VAT, can you calculate the taxes to pay?"},
		{150 * time.Second, "Port and storage charges came to TRY 38,000. Do they go into the VAT base, and how does the calculation change?"},
		{8 * time.Minute, "If we had an investment incentive certificate for the machine, what would happen to the VAT?"},
		{60 * time.Second, "I'll present this calculation to management. Can you summarize it in a table?"},
	}},
	{"can", "junior broker's assistant, learning the terms", time.Minute, []message{
		{0, "What is a delivery order (ordino)?"},
		{35 * time.Second, "What's the difference between a bill of lading and a CMR?"},
		{40 * time.Second, "Are an A.TR certificate and a certificate of origin the same thing?"},
		{45 * time.Second, "Can you explain the customs warehousing procedure in two sentences?"},
		{60 * time.Second, "When is a release for free circulation declaration filed?"},
		{90 * time.Second, "Thanks, I'll take notes. Can you list what I asked today as a short glossary?"},
	}},
	{"elif", "CPA, booking an import", 6 * time.Minute, []message{
		{0, "In which month is VAT paid on imports deducted?"},
		{110 * time.Second, "The import invoice is in USD, and the exchange rate changed between delivery and payment. How do I book the exchange difference? Can you give a sample journal entry?"},
		{70 * time.Second, "Are customs duty and freight added to the cost of the goods, or expensed?"},
		{11 * time.Minute, "Example: goods USD 50,000, freight USD 3,000, customs duty TRY 61,600, rate TRY 41.00. Can you calculate the amount to post to account 153 (merchandise)?"},
		{50 * time.Second, "Thanks. Can you write a three-point summary so I can explain this to my intern?"},
	}},
	{"burak", "exporter's accounting manager", 8 * time.Minute, []message{
		{0, "What is the inward processing regime, briefly?"},
		{80 * time.Second, "If we sell raw materials imported under an inward processing authorization in Turkey instead of exporting them, what happens? Can you assess it in terms of tax and penalties?"},
		{100 * time.Second, "In export VAT refunds, what's the difference between a refund by offset and a cash refund?"},
		{60 * time.Second, "I'll present these three topics to the board; can you give me a one-sentence summary of each?"},
	}},
	{"selin", "customs broker, checking a toy shipment's papers", 3 * time.Minute, []message{
		{0, `The supplier's documents arrived. Can you compare them?

COMMERCIAL INVOICE No: NB-2611  Date: 2026-10-02
Seller: Ningbo Bright Toys Co., Ltd.   Buyer: Anadolu Oyuncak A.S., Bursa
Terms: CIF Mersin
Description: Plastic building block sets, 520 pcs/set, age 6+
Quantity: 1,200 sets in 100 cartons   Unit price: USD 7.80   Total: USD 9,360.00
Gross weight: 1,450 kg   Country of origin: China   HS code: 9503.00

PACKING LIST No: NB-2611  Date: 2026-10-02
Description: Plastic building block sets, 520 pcs/set
Quantity: 1,200 sets in 120 cartons
Net weight: 1,180 kg   Gross weight: 1,630 kg
HS code: 9504.90   Country of origin: China`},
		{60 * time.Second, "Can you write a short email to the supplier?"},
		{7 * time.Minute, "What inspections apply to importing these toys? Can you check the tariff database? Origin China."},
		{2 * time.Minute, "Should I hold the declaration until the corrected packing list arrives, or start with what I have? What are the risks?"},
		{45 * time.Second, "Finally, can you make a to-do list?"},
	}},
	{"hakan", "freight forwarder, Incoterms and customs value", 10 * time.Minute, []message{
		{0, "Can you briefly explain the difference between FOB and CIF?"},
		{75 * time.Second, "With DAP Istanbul delivery, is freight included in the customs value? The seller didn't show freight separately on the invoice."},
		{210 * time.Second, "Would the calculation change if the seller had shown freight separately? Goods USD 25,000, freight to the Turkish border USD 2,000, from the border to the warehouse USD 600."},
		{55 * time.Second, "Can you write a four-point list of advice for a client choosing Incoterms?"},
	}},
	{"deniz", "CPA, a client's undervaluation penalty", 12 * time.Minute, []message{
		{0, "My client under-declared the value on a past import, and an additional assessment arrived. How is the penalty calculated under Law No. 4458, arts. 234 and 235?"},
		{90 * time.Second, "What are the settlement and appeal options against this penalty, and what are the deadlines?"},
		{330 * time.Second, "How much can the penalty be reduced in settlement? Can voluntary disclosure provisions apply?"},
		{40 * time.Second, "Can you write four plain points to explain this to my client?"},
	}},
	{"ali", "online seller, first commercial import", 5 * time.Minute, []message{
		{0, "How are taxes calculated on small parcels that come from abroad by post? Briefly."},
		{150 * time.Second, "I want to bring in 200 phone cases a month. Should I use individual courier shipments or a commercial import? Assess it on cost and risk."},
		{60 * time.Second, "Is a sole proprietorship enough for commercial imports?"},
		{9*time.Minute + 30*time.Second, "Can you find the tariff code for a phone case? Check the tariff database."},
		{70 * time.Second, "Thanks. Can you write a short checklist for my first import?"},
	}},
}

// spend counts what the chats paid, by the app's own reckoning, and stops
// them all past the cap.
type spend struct {
	mu     sync.Mutex
	usd    float64
	cancel context.CancelFunc
}

func (s *spend) add(usd float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usd += usd
	if s.usd > maxTrafficSpend {
		s.cancel()
	}
}

func TestCustomsTraffic(t *testing.T) {
	st := trafficStack(t)
	w := st.w
	pace := 1.0
	if st.key == "" {
		pace = 0 // the fake provider: check the harness, unpaced
	}
	if p, err := strconv.ParseFloat(os.Getenv("PROMPTDAM_DEMO_PACE"), 64); err == nil {
		pace = p
	}
	users := chats
	if pick := os.Getenv("PROMPTDAM_DEMO_USERS"); pick != "" {
		names := strings.Split(pick, ",")
		users = slices.DeleteFunc(slices.Clone(chats), func(c chat) bool { return !slices.Contains(names, c.user) })
	}
	t.Logf("running on %s", st.label)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	paid := &spend{cancel: cancel}
	start := time.Now()

	transcripts := make([]strings.Builder, len(users))
	errs := make([]error, len(users))
	var wg sync.WaitGroup
	for i, c := range users {
		wg.Go(func() {
			errs[i] = runChat(ctx, t, st, c, pace, paid, &transcripts[i])
		})
	}
	wg.Wait()
	t.Logf("the chats took %s; the app's own count of spend: $%.4f", time.Since(start).Round(time.Second), paid.usd)
	for i, err := range errs {
		if err != nil {
			t.Errorf("%s: %v", users[i].user, err)
		}
	}
	if err := w.Log.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	report := trafficLedger(t, st, start)
	if st.model == "advisor" {
		routerReport(t, w, start, report)
	}
	cachingByBackend(t, w, report)
	t.Log("\n" + report.String())
	if path := os.Getenv("PROMPTDAM_DEMO_TRANSCRIPT"); path != "" {
		var out strings.Builder
		out.WriteString("# CustomsPilot traffic through Promptdam\n\nOn " + st.label + ".\n\n")
		for i := range transcripts {
			out.WriteString(transcripts[i].String())
		}
		out.WriteString("# The gateway's ledger\n\n" + report.String())
		if err := os.WriteFile(path, []byte(out.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// runChat holds one user's chat: each message after its pause, with the
// whole history, tool calls answered from the demo data.
func runChat(ctx context.Context, t *testing.T, st stack, c chat, pace float64, paid *spend, transcript *strings.Builder) error {
	wait := func(d time.Duration) error {
		select {
		case <-time.After(time.Duration(float64(d) * pace)):
			return nil
		case <-ctx.Done():
			return fmt.Errorf("stopped: spend passed $%.2f", maxTrafficSpend)
		}
	}
	client := &http.Client{Timeout: 4 * time.Minute}
	system, _ := json.Marshal(pilotSystem)
	var history []json.RawMessage
	call := func() (pilotAnswer, http.Header, error) {
		msgs, _ := json.Marshal(history)
		model, _ := json.Marshal(st.model)
		body := `{"model":` + string(model) + `,"max_tokens":4000,"system":` + string(system) + `,"tools":` + pilotTools +
			`,"messages":` + string(msgs) + `}`
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, st.w.Gateway.URL+"/v1/messages", bytes.NewReader([]byte(body)))
		req.Header.Set("Authorization", "Bearer "+st.w.Raw)
		req.Header.Set(brand.HeaderSession, "chat-"+c.user)
		req.Header.Set(brand.HeaderUser, c.user)
		res, err := client.Do(req)
		if err != nil {
			return pilotAnswer{}, nil, err
		}
		defer func() { _ = res.Body.Close() }()
		out, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK {
			return pilotAnswer{}, nil, fmt.Errorf("status %d: %s", res.StatusCode, clip(string(out), 300))
		}
		var a pilotAnswer
		if err := json.Unmarshal(out, &a); err != nil {
			return pilotAnswer{}, nil, err
		}
		// The model the answer names (a router of the provider's answers
		// with the model it chose), else the one the gateway served.
		p, ok := st.price(a.Model)
		if !ok {
			p, ok = st.price(res.Header.Get(brand.HeaderServedModel))
		}
		if ok {
			paid.add(float64(requestlog.CostAt(p, wire.Usage{Usage: wireUsage(a.Usage)})) / 1e6)
		}
		assistant, _ := json.Marshal(map[string]any{"role": "assistant", "content": a.Content})
		history = append(history, assistant)
		return a, res.Header, nil
	}

	fmt.Fprintf(transcript, "## %s: %s\n\n", c.user, c.who)
	if err := wait(c.opens); err != nil {
		return err
	}
	for i, m := range c.turns {
		if err := wait(m.pause); err != nil {
			return err
		}
		user, _ := json.Marshal(map[string]any{"role": "user", "content": m.ask})
		history = append(history, user)
		fmt.Fprintf(transcript, "### %d. %s (after %s)\n\n%s\n\n", i+1, c.user, m.pause, m.ask)
		a, h, err := call()
		for loop := 0; err == nil && a.StopReason == "tool_use" && loop < 4; loop++ {
			var results []map[string]any
			for _, raw := range a.Content {
				var blk struct {
					Type, ID, Name string
					Input          json.RawMessage
				}
				_ = json.Unmarshal(raw, &blk)
				if blk.Type == "tool_use" {
					out := runTool(blk.Name, blk.Input)
					fmt.Fprintf(transcript, "*Tool %s(%s) → %s*\n\n", blk.Name, blk.Input, clip(out, 100))
					results = append(results, map[string]any{"type": "tool_result", "tool_use_id": blk.ID, "content": out})
				}
			}
			user, _ := json.Marshal(map[string]any{"role": "user", "content": results})
			history = append(history, user)
			a, h, err = call()
		}
		if err != nil {
			return fmt.Errorf("message %d: %w", i+1, err)
		}
		served := h.Get(brand.HeaderServedModel)
		if served == st.model && a.Model != "" {
			served = a.Model // a provider's router answers with the model it chose
		}
		tier := h.Get(brand.HeaderTier) + " (" + h.Get(brand.HeaderTierSource) + "), " + served
		t.Logf("%-7s %d/%d %s: in %d, cache read %d, write %d, out %d", c.user, i+1, len(c.turns), tier,
			a.Usage.Input+a.Usage.CacheRead+a.Usage.CacheWrite, a.Usage.CacheRead, a.Usage.CacheWrite, a.Usage.Output)
		fmt.Fprintf(transcript, "**CustomsPilot** (%s)\n\n%s\n\n", tier, a.text())
	}
	return nil
}

// trafficRow is a call as the request log has it.
type trafficRow struct {
	User           string    `ch:"end_user"`
	TS             time.Time `ch:"ts"`
	TurnKind       string    `ch:"turn_kind"`
	Tier           string    `ch:"tier"`
	Source         string    `ch:"tier_source"`
	Served         string    `ch:"served_model"`
	Input          uint32    `ch:"input_tokens"`
	CacheRead      uint32    `ch:"cache_read_tokens"`
	CacheWrite     uint32    `ch:"cache_write_tokens"`
	Output         uint32    `ch:"output_tokens"`
	Cost           int64     `ch:"cost_micros"`
	Counterfactual int64     `ch:"counterfactual_micros"`
	Savings        int64     `ch:"savings_micros"`
	Reason         string    `ch:"savings_reason"`
	Router         int64     `ch:"router_micros"`
	GapMs          uint32    `ch:"chain_gap_ms"`
	ChainState     string    `ch:"chain_state"`
	Decision       string    `ch:"decision"`
}

// trafficLedger sums the request log per chat and per model that
// answered, against the stack's baseline: every call on its strongest
// model, priced at list price without caching (the ledger's own
// counterfactual for a router's calls; computed here so a run without
// Promptdam's router compares the same way). It also prices what the
// provider's one-hour cache would have changed, on Anthropic: calls after
// a pause over five minutes wrote their conversation's cache again; under
// a one-hour cache they'd have read it, and every write would have cost
// the one-hour price.
func trafficLedger(t *testing.T, st stack, start time.Time) *strings.Builder {
	t.Helper()
	var rows []trafficRow
	if err := st.w.C.Events.Select(context.Background(), &rows, events.Project(st.w.Project), `SELECT end_user, ts, turn_kind, tier,
		tier_source, served_model, input_tokens, cache_read_tokens, cache_write_tokens, output_tokens, cost_micros,
		counterfactual_micros, savings_micros, savings_reason, router_micros, chain_gap_ms, chain_state, decision
		FROM requests WHERE {tenant} ORDER BY end_user, ts`); err != nil {
		t.Fatal(err)
	}
	base, baseKnown := st.price(st.baseline)
	type sums struct {
		calls, turns, cheaper, longPauses, unpriced int
		input, cacheRead                            uint64
		cost, base, router, cacheSaved, routeSaved  int64
		hourCost                                    float64
	}
	per := map[string]*sums{}
	byModel := map[string]*sums{}
	var all sums
	sources := map[string]int{}
	var prev *trafficRow
	for i := range rows {
		r := &rows[i]
		if prev != nil && prev.User != r.User {
			prev = nil
		}
		model := r.Served
		if m, ok := st.price(r.Served); ok {
			model = m.ID
		}
		if per[r.User] == nil {
			per[r.User] = &sums{}
		}
		if byModel[model] == nil {
			byModel[model] = &sums{}
		}
		long := r.ChainState == "continued" && r.GapMs >= 300_000 && r.GapMs < 3_600_000
		// What the call would have cost on the baseline model, no cache.
		var baseline int64
		if baseKnown {
			baseline = int64(math.Round(float64(r.Input)*base.Input + float64(r.Output)*base.Output))
		}
		// The one-hour cache: every write at its price, and after a long
		// pause on the same model the conversation read back, not written.
		hour := float64(r.Cost) / 1e6
		if p, ok := st.list.Lookup("anthropic", r.Served); ok && p.CacheWrite1h > 0 {
			reused := uint32(0)
			if long && prev != nil && prev.Served == r.Served {
				reused = min(r.CacheWrite, prev.CacheRead+prev.CacheWrite)
			}
			hour += (float64(r.CacheWrite-reused)*(p.CacheWrite1h-p.CacheWrite) + float64(reused)*(p.CachedInput-p.CacheWrite)) / 1e6
		}
		_, priced := st.price(r.Served)
		for _, x := range []*sums{per[r.User], byModel[model], &all} {
			x.calls++
			if r.TurnKind != "continuation" {
				x.turns++
				if !strings.HasPrefix(model, st.baseline) {
					x.cheaper++
				}
				if long {
					x.longPauses++
				}
			}
			if !priced {
				x.unpriced++
			}
			x.input += uint64(r.Input)
			x.cacheRead += uint64(r.CacheRead)
			x.cost += r.Cost
			x.base += baseline
			x.router += r.Router
			x.hourCost += hour + float64(r.Router)/1e6
			switch r.Reason {
			case requestlog.ReasonPromptCache:
				x.cacheSaved += r.Savings
			case requestlog.ReasonRouting:
				x.routeSaved += r.Savings
			}
		}
		if r.TurnKind != "continuation" {
			sources[r.Source]++
		}
		prev = r
	}
	b := &strings.Builder{}
	fmt.Fprintf(b, "On %s. Without Promptdam: every call on %s at list price, no caching.\n\n", st.label, st.baseline)
	b.WriteString("| User | Messages | Not on " + st.baseline + " | Pauses over 5 min | Cost | Without Promptdam | Saved |\n|---|---|---|---|---|---|---|\n")
	row := func(name string, x *sums) {
		saved := x.base - x.cost - x.router
		fmt.Fprintf(b, "| %s | %d (%d calls) | %d | %d | $%.4f | $%.4f | $%.4f (%.0f%%) |\n", name, x.turns, x.calls, x.cheaper,
			x.longPauses, usd(x.cost+x.router), usd(x.base), usd(saved), pct(saved, x.base))
	}
	for _, u := range slices.Sorted(maps.Keys(per)) {
		row(u, per[u])
	}
	row("**All**", &all)
	b.WriteString("\n| Model | Messages | Calls | Input tokens | Read from cache | Cost |\n|---|---|---|---|---|---|\n")
	for _, m := range slices.Sorted(maps.Keys(byModel)) {
		x := byModel[m]
		fmt.Fprintf(b, "| %s | %d | %d | %d | %.0f%% | $%.4f |\n", m, x.turns, x.calls, x.input,
			100*float64(x.cacheRead)/float64(max(x.input, 1)), usd(x.cost))
	}
	fmt.Fprintf(b, "\nThe ledger's savings: by the prompt cache $%.4f, by routing $%.4f; the decision model cost $%.4f (taken off).\n",
		usd(all.cacheSaved), usd(all.routeSaved), usd(all.router))
	if all.unpriced > 0 {
		fmt.Fprintf(b, "%d calls answered by a model the price list doesn't have: their cost is missing above.\n", all.unpriced)
	}
	fmt.Fprintf(b, "Messages placed by: %v.\n", sources)
	if st.catalog == "anthropic" {
		fmt.Fprintf(b, "With the provider's one-hour cache instead of five minutes (an estimate from the same calls): cost $%.4f.\n", all.hourCost)
	}
	fmt.Fprintf(b, "Took %s.\n", time.Since(start).Round(time.Second))
	return b
}

// routerReport adds the router's report: how its decision model answered.
func routerReport(t *testing.T, w *gatewaytest.World, start time.Time, b *strings.Builder) {
	t.Helper()
	rows, err := w.App.Routers.List(w.Scoped(), w.Project)
	if err != nil || len(rows) != 1 {
		t.Fatalf("%d routers, %v", len(rows), err)
	}
	fmt.Fprintf(b, "Decision model: %s.\n", rows[0].DecisionModel)
	rep, err := routers.BuildReport(context.Background(), w.C.Events, w.Project, routers.Of(rows[0]), start.Add(-time.Hour),
		time.Now().Add(time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range rep.Decisions {
		fmt.Fprintf(b, "Decision model %s: %d messages, $%.4f, wait median %.0f ms, p95 %.0f ms, p99 %.0f ms.\n", d.Outcome, d.Calls,
			usd(d.CostMicros), d.WaitMs[0], d.WaitMs[1], d.WaitMs[2])
	}
	savings, err := analytics.New(w.C.Events).Savings(context.Background(), w.Project, start.Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(b, "The savings report (what the dashboard reads): cost $%.4f, without Promptdam $%.4f, saved $%.4f.\n",
		usd(savings.Total.Cost), usd(savings.Total.Counterfactual), usd(savings.Total.Savings))
}

// cachingByBackend adds how each model's calls used the prompt cache, by
// the backend that answered (a router of providers') and the gateway's
// strategy: the calls that should have read their conversation's cache and
// those that did, the share of input read, and whether the provider
// reported each call's cost.
func cachingByBackend(t *testing.T, w *gatewaytest.World, b *strings.Builder) {
	t.Helper()
	var rows []struct {
		Model    string `ch:"target_model"`
		Backend  string `ch:"backend"`
		Strategy string `ch:"cache_strategy"`
		Healed   string `ch:"healed"`
		Calls    uint64 `ch:"calls"`
		Expects  uint64 `ch:"expects"`
		Hits     uint64 `ch:"hits"`
		Input    uint64 `ch:"input"`
		Read     uint64 `ch:"was_read"`
		Reported uint64 `ch:"reported"`
		Cost     int64  `ch:"cost"`
	}
	if err := w.C.Events.Select(context.Background(), &rows, events.Project(w.Project), `SELECT target_model, backend,
		cache_strategy, healed, count() AS calls, countIf(cache_expected_tokens > 0) AS expects,
		countIf(cache_expected_tokens > 0 AND cache_read_tokens > 0) AS hits, sum(input_tokens) AS input,
		sum(cache_read_tokens) AS was_read, countIf(cost_reported) AS reported, sum(cost_micros) AS cost
		FROM requests WHERE {tenant} AND usage_known GROUP BY target_model, backend, cache_strategy, healed
		ORDER BY target_model, backend, cache_strategy, healed`); err != nil {
		t.Fatal(err)
	}
	b.WriteString("\n| Model | Backend | Caching | Healed | Calls | Expected a cache read | Read it | Input read from cache | Cost reported | Cost |\n" +
		"|---|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range rows {
		fmt.Fprintf(b, "| %s | %s | %s | %s | %d | %d | %d | %.0f%% | %d | $%.4f |\n", r.Model, r.Backend, r.Strategy, r.Healed,
			r.Calls, r.Expects, r.Hits, 100*float64(r.Read)/float64(max(r.Input, 1)), r.Reported, usd(r.Cost))
	}
}

func pct(part, whole int64) float64 { return 100 * float64(part) / float64(max(whole, 1)) }

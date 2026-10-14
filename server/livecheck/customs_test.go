// Package livecheck_test runs Promptdam against real providers, on keys
// the developer supplies; without them every test skips.
package livecheck_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/events"
	"github.com/hasan-muzak/keel-core/pkg/llm"
	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/features/analytics"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/promptdam/server/features/providers"
	"github.com/hasan-muzak/promptdam/server/features/requestlog"
	"github.com/hasan-muzak/promptdam/server/features/routers"
	"github.com/hasan-muzak/promptdam/server/features/routers/tiers"
	"github.com/hasan-muzak/promptdam/server/features/routes"
)

// CustomsPilot is a customer's product: an assistant for Turkish customs
// brokers and accountants (document checks, tariff classification,
// legislation, import costs). It calls Promptdam with the Anthropic SDK's
// request shape and model "advisor", a router: Haiku 5.5 at low effort for
// the easy turns, Sonnet 5.5 for the rest, Haiku 5.5 deciding.
// TestCustomsBroker holds one long conversation with a broker who is also
// a CPA; TestCustomsTraffic (traffic_test.go) ten users' chats at once,
// paced like people. Both end with the gateway's own ledger.
//
// Run: task demo:customs (PROMPTDAM_TEST_ANTHROPIC_KEY in .env, the stack
// up). PROMPTDAM_DEMO_TRANSCRIPT names a file for the whole conversation.
// It stops past maxSpend.
const maxSpend = 0.50 // dollars

const pilotSystem = `You are CustomsPilot, a legislation and operations assistant for customs brokers, their assistants and certified public accountants in Turkey. Your users are professionals who know their trade: answer them as a colleague would, clearly and briefly.

# Your tasks
1. Document analysis: extract the goods, quantities, weights, prices and countries of origin from commercial invoices, packing lists, bills of lading and certificates of origin, and find inconsistencies between documents.
2. Tariff classification (GTIP, the Turkish 12-digit tariff code built on the HS): propose candidate codes from a product description, its specifications or a catalog, with reasoning based on the Harmonized System's general interpretive rules and the chapter and heading notes.
3. Legislation assistant: answer questions on Customs Law No. 4458, the Customs Regulation, communiqués and circulars, citing sources, and compare old and new rules.
4. Import cost and risk analysis: from the tariff code, origin, customs value and procedure, calculate customs duty, additional customs duty (IGV), VAT and other charges, and name the permits and inspections required (TAREKS, conformity assessment, import licences).
5. Company and tax questions: users often ask about their clients' legal form, tax burden and liability. Answer within the Turkish Commercial Code No. 6102, Income Tax Law No. 193, Corporate Tax Law No. 5520, VAT Law No. 3065 and Law No. 6183 on the collection of public receivables.

# How to answer
- Answer in English, even when a user writes in another language, unless they ask for a text in another language.
- Lead with the direct answer, then the reasoning. Stay under 250 words unless the user asks for detail; use a table for comparisons and calculations.
- Cite the law's name, number and article. If you are not sure of an article number, don't invent one: say the article should be checked.
- Separate what is certain from what must be checked. Rates, amounts, thresholds and brackets change often: remind the user to check the current Official Gazette, the Turkish Customs Tariff Schedule, the Import Regime Decision and the relevant Presidential Decisions.
- Tool results are your source. When a tool result says DEMO DATA, say so in your answer.
- For matters with legal or tax consequences, note in one sentence that the final decision belongs to the user and their client, and that a written opinion may be worth getting. Don't add long disclaimers.
- Don't use em dashes; use a hyphen, a comma or a colon.
- Use the exchange rates, amounts and rates the user gives; if you must assume something, say so.

# Calculation rules
- The customs value is the value of the goods delivered to the place of entry into the Turkish customs territory: for FOB or EXW terms, add freight and insurance up to that point (the CIF basis).
- Currency is converted at the Central Bank of Turkey's buying rate on the day the declaration is registered; if the user gives a rate, use it and say so.
- Customs duty = customs value x the duty rate. Additional customs duty (IGV) = customs value x the IGV rate (IGV applies by origin, under the lists annexed to the Import Regime Decision).
- The VAT base on imports (VAT Law No. 3065, art. 21): the value for customs duty + taxes, duties and charges paid on import (customs duty, IGV, any special consumption tax) + other costs incurred until the goods are delivered that the invoice doesn't include. The general VAT rate is 20%; check the rate for the goods.
- Show every calculation step by step: item, formula, amount. End with the total tax burden and its share of the customs value. Round to two decimal places.

# Document check rules
Compare invoices, packing lists and other documents on: document number and date, seller and buyer, description and model, quantity and unit, number and kind of packages, net and gross weight, unit price and total, delivery terms (Incoterms), country of origin and tariff code. For each inconsistency give: the field, the value in document A, the value in document B, the risk (high, medium, low) and the action to take. Treat differences in origin, quantity and value as high risk: they mean a false declaration, unpaid duty, origin proof problems and penalties.

# Legal forms: reference notes (Commercial Code No. 6102 and the tax laws)
- Sole proprietorship: no legal personality; the owner is personally and fully liable for all debts. Profit is taxed under the Income Tax Law at progressive rates (the brackets change every year). Quick and cheap to set up.
- General partnership (kollektif şirket): the partners, natural persons, are jointly and fully liable for its debts. It pays no corporate tax: each partner's share of the profit is their business income under the Income Tax Law (art. 37). Rarely used.
- Limited partnership (komandit şirket): at least one partner has unlimited liability, the others limited. An ordinary one pays no corporate tax (the unlimited partners' shares are their business income); one with its capital divided into shares is a corporate taxpayer. Rarely used.
- Limited liability company (limited şirket): 1 to 50 partners. Minimum capital TRY 50,000 (from 1 January 2024, by Presidential Decision). Partners are liable to the company up to their capital commitment, but may be held liable for public receivables the company can't pay, in proportion to their shares (Law No. 6183, art. 35). A corporate taxpayer.
- Joint-stock company (anonim şirket): can be founded by one shareholder. Minimum capital TRY 250,000, or TRY 500,000 initial capital under the registered capital system (from 1 January 2024). Shareholders are as a rule liable only to the company, up to their committed capital; legal representatives may be liable for public receivables under Law No. 6183, art. 35 (repeated). A corporate taxpayer. Shares transfer more easily than in a limited company.
- Cooperative: serves its members' economic interests through mutual help; rarely suitable for commercial imports.
- The general corporate tax rate is 25%; reduced rates may apply to export and manufacturing profits, so check the current rules. Distributed profit is also subject to withholding tax.
- When choosing a legal form, weigh together: liability, the effective tax burden at the expected profit, withholding on distributions, set-up and accounting costs, capital needs, the number of partners and share transfers, standing with banks and suppliers, and later applications for statuses such as Authorized Economic Operator (YYS).

# Customs brokerage notes (Customs Law No. 4458)
- Representation: anyone may appoint a representative before the customs authorities; representation is direct (in another's name and on their account) or indirect (in one's own name, on another's account) (Law No. 4458, art. 5).
- Customs brokers and their assistants work under the Law's provisions on customs brokerage (art. 227 onward) and the Customs Regulation; remind users of their duties on the accuracy of declarations and keeping documents.
- On liability and penalties (for example the duty differences and fines under arts. 234 and 235), note that the facts of the case decide.

# Output formats
- Comparison: a Markdown table, then "Recommendation" with 2 or 3 points.
- Calculation: "Assumptions", "Calculation" (a table), "Result".
- Document check: a table (Field | Invoice | Packing list | Risk | Action), then "First steps".
- Email: a subject line and a body; polite, short, with points.
- Summaries and to-do lists: a numbered list, each item with an owner and a priority.`

// The tools CustomsPilot gives the model; their answers are demo data.
const pilotTools = `[
 {"name":"tariff_search","description":"Searches the Turkish Customs Tariff Schedule for candidate tariff codes (GTIP) matching a product description. Use it first for classification questions.",
  "input_schema":{"type":"object","properties":{"query":{"type":"string","description":"The product's description and distinguishing features"}},"required":["query"]}},
 {"name":"tariff_rates","description":"Gets the customs duty, additional customs duty (IGV) and VAT rates and the import inspections for a tariff code and country of origin.",
  "input_schema":{"type":"object","properties":{"code":{"type":"string"},"origin":{"type":"string","description":"Country of origin"}},"required":["code","origin"]}}
]`

func runTool(name string, input json.RawMessage) string {
	switch name {
	case "tariff_search":
		var in struct{ Query string }
		_ = json.Unmarshal(input, &in)
		q := strings.ToLower(in.Query)
		for _, c := range demoCandidates {
			for _, word := range c.words {
				if strings.Contains(q, word) {
					return "DEMO DATA (not a real tariff search). Candidates:\n" + c.list
				}
			}
		}
		return "DEMO DATA: no match. Describe the product's material, function and use."
	case "tariff_rates":
		var in struct{ Code, Origin string }
		_ = json.Unmarshal(input, &in)
		rates := demoRates[""]
		for prefix, r := range demoRates {
			if prefix != "" && strings.HasPrefix(strings.ReplaceAll(in.Code, ".", ""), prefix) {
				rates = r
			}
		}
		return fmt.Sprintf("DEMO DATA (not a real tariff search). Code %s, origin %s:\n%s", in.Code, in.Origin, rates)
	}
	return "Unknown tool."
}

// The tariff tools' demo data, by what a query names.
var demoCandidates = []struct {
	words []string
	list  string
}{
	{[]string{"dishwasher"}, `1. 8422.19.00.00.00 Dishwashing machines: other (not household). Commercial, tunnel and hood types belong here.
2. 8422.11.00.00.00 Dishwashing machines: household.
3. 8419.89.98.00.19 Other machinery treating materials by a change of temperature (if a drying unit comes separately).`},
	{[]string{"headphone", "earbud", "earphone"}, `1. 8518.30.95.00.00 Headphones and earphones, combined or not with a microphone: other.
2. 8517.62.00.00.39 Machines for the reception and transmission of voice, images or other data (if data transmission is the main function).
3. 8504.40.83.00.00 Chargers (if the charging case is presented separately).`},
	{[]string{"toy", "block"}, `1. 9503.00.70.00.00 Other toys, put up in sets or outfits.
2. 9503.00.99.00.00 Other toys.
3. 9504.50.00.00.00 Video game consoles and machines (electronic games).`},
	{[]string{"phone case", "case", "cover"}, `1. 3926.90.97.90.18 Other articles of plastics (phone cases of plastic).
2. 4202.32.10.00.00 Articles of a kind carried in the pocket or handbag, outer surface of plastic sheeting.
3. 4202.31.00.00.00 Articles of a kind carried in the pocket or handbag, outer surface of leather.`},
}

var demoRates = map[string]string{
	"": `- Customs duty: 3.0%
- Additional customs duty (IGV): none (demo)
- VAT: 20%`,
	"8422": `- Customs duty: 2.2%
- Additional customs duty (IGV): 20% (for China origin, demo)
- VAT: 20%
- Inspections: TAREKS registration; machinery safety (CE declaration of conformity and technical file).
- Note: for Vietnam origin the IGV rate could differ; proof of origin is needed.`,
	"8518": `- Customs duty: 2.0%
- Additional customs duty (IGV): 20% (for China origin, demo)
- VAT: 20%
- Inspections: TAREKS registration; radio equipment (Bluetooth) and EMC conformity, CE marking; registration with the ICT Authority may be required.`,
	"9503": `- Customs duty: 4.7%
- Additional customs duty (IGV): 30% (for China origin, demo)
- VAT: 20%
- Inspections: toy safety, TAREKS registration and a CE declaration of conformity; sample testing may be required.`,
	"3926": `- Customs duty: 6.5%
- Additional customs duty (IGV): 30% (for China origin, demo)
- VAT: 20%
- Inspections: none (demo).`,
	"4202": `- Customs duty: 9.7%
- Additional customs duty (IGV): 30% (for China origin, demo)
- VAT: 20%
- Inspections: none (demo).`,
}

// pilotGateway is a gateway serving CustomsPilot's project: an Anthropic
// provider on key, router "advisor" (Haiku 5.5 at low effort, Sonnet 5.5
// at medium, Haiku 5.5 deciding within the auto timeout), prompt caching
// on, and the prices production syncs daily. With no key it serves the
// same router on the fake provider, for checking a demo for free.
func pilotGateway(t *testing.T, key string) (*gatewaytest.World, *llm.Catalog) {
	t.Helper()
	ctx := context.Background()
	opts := gatewaytest.Options{ClickHouse: true}
	if key == "" {
		opts.Upstream.Reply = func(model string, body []byte) string {
			if strings.Contains(string(body), "You route requests") {
				return `{"tier": "fast"}`
			}
			return "Answered by " + model
		}
	}
	w := gatewaytest.New(t, opts)
	w.Grant(catalog.Routers, catalog.RoutingLearned, catalog.PromptCaching)
	fast, expert := "main/fast-model", "main/expert-model"
	if key != "" {
		if err := w.App.Prices.Sync(ctx, true); err != nil {
			t.Fatalf("syncing prices: %v", err)
		}
		for _, m := range []string{"claude-haiku-5-5", "claude-sonnet-5-5"} {
			p, ok := w.App.Prices.Current().Lookup("anthropic", m)
			if !ok {
				t.Fatalf("no price for %s", m)
			}
			t.Logf("price %s: input $%g, cache write $%g (one hour $%g), cache read $%g, output $%g per million", m,
				p.Input, p.CacheWrite, p.CacheWrite1h, p.CachedInput, p.Output)
		}
		if _, err := w.App.Providers.Create(w.Scoped(), w.Org, w.Project, providers.Input{Name: "anthropic", Kind: "anthropic",
			APIKey: &key, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		fast, expert = "anthropic/claude-haiku-5-5", "anthropic/claude-sonnet-5-5"
	}
	if _, err := w.App.Routers.Create(w.Scoped(), w.Org, w.Project, routers.Input{Name: "advisor", Status: tiers.Active,
		DecisionModel: fast,
		Spec: tiers.Spec{Tiers: []tiers.Tier{
			{Name: "fast", Model: fast, Effort: "low",
				Description: "Definitions and single facts, rewording, translations, short emails, summaries and to-do lists of what the conversation already covered.",
				Examples:    []string{"How many partners can a limited company have at most?", "Can you sum this up in three points?"}},
			{Name: "expert", Model: expert, Effort: "medium",
				Description: "Legal analysis and comparisons across laws, tax and liability trade-offs, customs value and tax calculations, tariff classification with reasoning, checking documents against each other.",
				Examples:    []string{"Compare a sole proprietorship and a limited company on tax and liability."}},
		}, Default: "expert"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.App.Routes.Create(w.Scoped(), w.Org, w.Project, routes.Input{Name: "default", PromptCache: true}); err != nil {
		t.Fatal(err)
	}
	return w, w.App.Prices.Current()
}

// The broker's side of the conversation: a CPA who is also a customs
// broker, helping a client start importing.
var brokerTurns = []string{
	"Hello. I'm a CPA and customs broker in Izmir. A client of mine is starting to import commercial kitchen equipment. Can you briefly list the legal forms of company in Turkey?",
	"My client is on their own and expects about TRY 15 million of imports and around TRY 1.5 million of pre-tax profit in the first year. Can you compare a sole proprietorship, a limited company and a joint-stock company on liability, minimum capital, tax burden and a later Authorized Economic Operator application? What would you recommend?",
	"What was the maximum number of partners in a limited company again?",
	"The client's first import: a tunnel-type commercial dishwasher from China, AISI 304 stainless steel, 60 racks an hour. Can you suggest a tariff code? Check the tariff database, then get the rates for China origin too.",
	"The invoice is USD 18,400 for 4 sets, FOB Shanghai. Freight USD 2,150, insurance USD 160. Let's assume USD 1 = TRY 41.20. Can you calculate the customs duty, IGV and VAT step by step?",
	`The supplier's documents arrived. Can you compare them?

COMMERCIAL INVOICE No: SH-26-0917  Date: 2026-09-17
Seller: Shanghai Hengli Kitchen Equipment Co., Ltd.
Buyer: Ege Mutfak Ekipmanlari Ltd. Sti., Izmir, Turkiye
Terms: FOB Shanghai
Description: Tunnel type commercial dishwasher, model HL-T60, 60 racks/hour, stainless steel AISI 304
Quantity: 4 sets   Unit price: USD 4,600.00   Total: USD 18,400.00
Country of origin: China   HS code: 8422.19

PACKING LIST No: SH-26-0917  Date: 2026-09-18
Description: Tunnel type commercial dishwasher, model HL-T60S
Quantity: 3 sets in 4 wooden cases
Net weight: 1,240 kg   Gross weight: 1,480 kg
Country of origin: Vietnam
Marks: EGE-IZM-01/04`,
	"Thanks. Can you draft a short email to the supplier about these inconsistencies?",
	"I'll handle this import on my client's behalf. Under Law No. 4458, what's the difference between direct and indirect representation, and how does my liability as a customs broker change in each?",
	"I'll explain this to my client; can you write a plain three-point summary?",
	"If the origin turns out to be Vietnam, how does the calculation change? Only tell me the items that change.",
	"Finally, can you make a to-do list from everything we discussed today?",
}

type pilotUsage struct {
	Input      uint64 `json:"input_tokens"`
	CacheWrite uint64 `json:"cache_creation_input_tokens"`
	CacheRead  uint64 `json:"cache_read_input_tokens"`
	Output     uint64 `json:"output_tokens"`
}

type pilotAnswer struct {
	Model      string            `json:"model"`
	Content    []json.RawMessage `json:"content"`
	StopReason string            `json:"stop_reason"`
	Usage      pilotUsage        `json:"usage"`
}

func (a pilotAnswer) text() string {
	var b strings.Builder
	for _, c := range a.Content {
		var blk struct{ Type, Text string }
		_ = json.Unmarshal(c, &blk)
		if blk.Type == "text" {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

func TestCustomsBroker(t *testing.T) {
	key := os.Getenv("PROMPTDAM_TEST_ANTHROPIC_KEY")
	if key == "" {
		t.Skip("no PROMPTDAM_TEST_ANTHROPIC_KEY")
	}
	ctx := context.Background()
	w, list := pilotGateway(t, key)

	system, _ := json.Marshal(pilotSystem)
	var history []json.RawMessage
	var spent float64
	var transcript strings.Builder
	transcript.WriteString("# CustomsPilot through Promptdam: the conversation\n\n")
	start := time.Now()

	call := func() (pilotAnswer, string) {
		t.Helper()
		msgs, _ := json.Marshal(history)
		body := `{"model":"advisor","max_tokens":4000,"system":` + string(system) + `,"tools":` + pilotTools + `,"messages":` + string(msgs) + `}`
		status, h, out := w.Call("/v1/messages", body, brand.HeaderSession, "pilot-conv-42", brand.HeaderUser, "smmm-izmir")
		if status != 200 {
			t.Fatalf("status %d: %s", status, out)
		}
		var a pilotAnswer
		if err := json.Unmarshal([]byte(out), &a); err != nil {
			t.Fatal(err)
		}
		served := h.Get(brand.HeaderServedModel)
		if p, ok := list.Lookup("anthropic", served); ok {
			spent += float64(requestlog.CostAt(p, wire.Usage{Usage: wireUsage(a.Usage)})) / 1e6
		}
		assistant, _ := json.Marshal(map[string]any{"role": "assistant", "content": a.Content})
		history = append(history, assistant)
		tier := h.Get(brand.HeaderTier) + " (" + h.Get(brand.HeaderTierSource) + "), " + served
		if e := h.Get(brand.HeaderEffort); e != "" {
			tier += ", effort " + e
		}
		t.Logf("   %s: in %d, cache write %d, read %d, out %d, stop %s", tier, a.Usage.Input, a.Usage.CacheWrite, a.Usage.CacheRead,
			a.Usage.Output, a.StopReason)
		if spent > maxSpend {
			t.Fatalf("spent $%.3f, over the demo's $%.2f", spent, maxSpend)
		}
		return a, tier
	}

	for i, ask := range brokerTurns {
		t.Logf("turn %d: %s", i+1, clip(ask, 80))
		user, _ := json.Marshal(map[string]any{"role": "user", "content": ask})
		history = append(history, user)
		fmt.Fprintf(&transcript, "## %d. Broker\n\n%s\n\n", i+1, ask)
		a, tier := call()
		for loop := 0; a.StopReason == "tool_use" && loop < 4; loop++ {
			var results []map[string]any
			for _, c := range a.Content {
				var blk struct {
					Type, ID, Name string
					Input          json.RawMessage
				}
				_ = json.Unmarshal(c, &blk)
				if blk.Type == "tool_use" {
					out := runTool(blk.Name, blk.Input)
					fmt.Fprintf(&transcript, "*Tool %s(%s) → %s*\n\n", blk.Name, blk.Input, clip(out, 120))
					results = append(results, map[string]any{"type": "tool_result", "tool_use_id": blk.ID, "content": out})
				}
			}
			user, _ := json.Marshal(map[string]any{"role": "user", "content": results})
			history = append(history, user)
			a, tier = call()
		}
		fmt.Fprintf(&transcript, "**CustomsPilot** (%s)\n\n%s\n\n", tier, a.text())
	}
	t.Logf("the conversation took %s; the app's own count of spend: $%.4f", time.Since(start).Round(time.Second), spent)

	// The gateway's own ledger, as the request log stored it.
	if err := w.Log.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		TurnKind       string `ch:"turn_kind"`
		HumanTurns     uint16 `ch:"human_turns"`
		Tier           string `ch:"tier"`
		Source         string `ch:"tier_source"`
		Decided        string `ch:"decided_tier"`
		Served         string `ch:"served_model"`
		Effort         string `ch:"effort"`
		Input          uint32 `ch:"input_tokens"`
		CacheRead      uint32 `ch:"cache_read_tokens"`
		CacheWrite     uint32 `ch:"cache_write_tokens"`
		Output         uint32 `ch:"output_tokens"`
		Cost           int64  `ch:"cost_micros"`
		Counterfactual int64  `ch:"counterfactual_micros"`
		Savings        int64  `ch:"savings_micros"`
		Reason         string `ch:"savings_reason"`
		Basis          string `ch:"savings_basis"`
		Router         int64  `ch:"router_micros"`
		Latency        uint32 `ch:"latency_ms"`
	}
	if err := w.C.Events.Select(ctx, &rows, events.Project(w.Project), `SELECT turn_kind, human_turns, tier, tier_source, decided_tier,
		served_model, effort, input_tokens, cache_read_tokens, cache_write_tokens, output_tokens, cost_micros, counterfactual_micros,
		savings_micros, savings_reason, savings_basis, router_micros, latency_ms FROM requests WHERE {tenant} ORDER BY ts`); err != nil {
		t.Fatal(err)
	}
	var ledger strings.Builder
	ledger.WriteString("| # | Turn | Kind | Tier (source) | Served | Effort | In | Cache read | Cache write | Out | Cost | Without Promptdam | Saved | Reason (basis) | Router | ms |\n")
	ledger.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	var cost, cf, saved, router int64
	for i, r := range rows {
		fmt.Fprintf(&ledger, "| %d | %d | %s | %s (%s) | %s | %s | %d | %d | %d | %d | $%.5f | $%.5f | $%.5f | %s (%s) | $%.5f | %d |\n",
			i+1, r.HumanTurns, r.TurnKind, r.Tier, r.Source, strings.TrimPrefix(r.Served, "claude-"), r.Effort, r.Input, r.CacheRead,
			r.CacheWrite, r.Output, usd(r.Cost), usd(r.Counterfactual), usd(r.Savings), r.Reason, r.Basis, usd(r.Router), r.Latency)
		cost, cf, saved, router = cost+r.Cost, cf+r.Counterfactual, saved+r.Savings, router+r.Router
	}
	fmt.Fprintf(&ledger, "\nCalls %d. Cost $%.4f (+ router $%.4f). Without Promptdam $%.4f. Saved $%.4f (%.0f%%).\n",
		len(rows), usd(cost), usd(router), usd(cf), usd(saved), 100*float64(saved)/float64(max(cf, 1)))
	report, err := analytics.New(w.C.Events).Savings(ctx, w.Project, start.Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&ledger, "\nThe savings report (what the dashboard reads): cost $%.4f, without Promptdam $%.4f, saved $%.4f.\n",
		usd(report.Total.Cost), usd(report.Total.Counterfactual), usd(report.Total.Savings))
	for _, r := range report.ByReason {
		fmt.Fprintf(&ledger, "- reason %q: %d calls, saved $%.4f\n", r.Key, r.Requests, usd(r.Savings))
	}
	for _, r := range report.ByBasis {
		fmt.Fprintf(&ledger, "- basis %q: %d calls, saved $%.4f\n", r.Key, r.Requests, usd(r.Savings))
	}
	t.Log("\n" + ledger.String())
	if path := os.Getenv("PROMPTDAM_DEMO_TRANSCRIPT"); path != "" {
		transcript.WriteString("# The gateway's ledger\n\n" + ledger.String())
		if err := os.WriteFile(path, []byte(transcript.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// wireUsage is an Anthropic answer's usage as the gateway counts it: input
// with the cached tokens in it.
func wireUsage(u pilotUsage) llm.Usage {
	return llm.Usage{Input: u.Input + u.CacheRead + u.CacheWrite, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, Output: u.Output}
}

func usd(micros int64) float64 { return float64(micros) / 1e6 }

func clip(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

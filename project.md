# Promptdam: cut the LLM bill, block the leaks

> **Status:** planning · **Owner:** Hasan · **Written:** 2026-10-03
> **MVP build:** Mar 8 – Apr 23, 2027 · **Beta:** Apr 26, 2027 · **Public launch:** Tue May 18, 2027 (moved 5 weeks later to make room for Runlens for Mobile)
> **Catalog:** #10 "AI Cost Optimization API" + #17 "AI Firewall". Revenue potential: High.
> **Location:** `keel/products/promptdam/` inside the root monorepo. Shared Go packages are in `../../pkg/`, shared Nuxt layers in `../../packages/`.
> **Built on:** Keel (`../../project.md`). Read it first for the stack, conventions, billing and shared modules.
> **Name (final):** **Promptdam**. A dam holds back spend and stops leaks. Owned by Badele LLC.
> - Domains: **promptdam.dev** (product) and **promptdam.com** (redirect). Both were unregistered on 2026-10-03 (RDAP/WHOIS check); register them now and run a trademark search before launch.
> - "Sluice" was dropped because sluice.dev and sluice.ai are taken.

---

## 1. One-liner and pitch

**Change one line (`base_url`) and Promptdam cuts your LLM bill 30–60% with proven, quality-guarded routing and caching, while blocking secrets and PII before they leave your company. Pay a share of what you save.**

LLM spend is now a top-3 cost line for many software companies. Most of it is waste:
- the most expensive model is used for every request, including trivial ones
- prompt caching is missing or broken because a timestamp sits in the system prompt
- identical requests are paid for twice
- non-urgent jobs run at full price instead of batch price.

At the same time, apps and coding agents send `.env` files, customer emails and card numbers to third-party providers.

**Promptdam is an OpenAI- and Anthropic-compatible gateway (BYOK) that:**
1. Finds savings automatically:
   - prompt families
   - cheaper-model experiments with shadow evaluation
   - caching fixes
2. Proves every saved dollar in a per-request ledger.
3. Enforces a firewall: secrets and PII detection, redaction and blocking, CEL policies, budgets.

Basic routing and logging are free elsewhere, so Promptdam charges for **outcomes** (savings, protection), not for passing traffic through.

---

## 2. Problem and customer

| Persona | Company | Trigger |
|---|---|---|
| **Eng lead / CTO** (primary) | AI-heavy startups and scale-ups spending **$2k–$100k/mo** on LLM APIs | "Our OpenAI/Anthropic bill doubled; finance is asking questions" |
| **Platform / security engineer** | Companies rolling out coding agents and internal AI tools | "Developers' coding agents send secrets and customer data to LLM providers; we need control and an audit trail" |
| **Indie / small team** | Apps with thin margins on LLM features | "Every cent per request matters" |

### Jobs to be done
1. "Lower my LLM spend without my product getting worse, and show me proof."
2. "Stop secrets and PII from reaching providers: apps, internal tools, coding agents."
3. "Give every team, key and developer a budget, and alert before we blow it."
4. "Survive provider outages and rate limits without code changes" (fallbacks).

### Interview script (V0.4: 5 interviews before the build)
1. What's your monthly LLM spend, and how fast is it growing? Which models and providers?
2. Do you know which features or prompts drive the cost? Have you tried cheaper models? What stopped you?
3. Do you use prompt caching? Do you know your cache hit rate?
4. How do you stop secrets and PII from reaching providers today? Have you had an incident?
5. Do you use a gateway or proxy today (OpenRouter, LiteLLM, Portkey, Cloudflare, Vercel)? Why, or why not?
6. If a tool provably saved you X% with no quality loss, would you pay 15% of the savings? Or would you prefer a flat fee?

---

## 3. Competitive landscape and wedge (checked 2026-10-03; re-verify at launch)

| Player (status) | Pricing | What it does | Gap Promptdam exploits |
|---|---|---|---|
| **OpenRouter** | 5.5% fee on credits (8% Business); BYOK first $25k/mo free, then 5% | Marketplace and router across hundreds of models | Routes by availability and price, not by task quality; no savings proof; firewall isn't the focus |
| **Vercel AI Gateway** | Zero markup incl. BYOK; $5/mo free credit | Gateway for Vercel users | Passthrough and observability; no savings engine, no firewall |
| **Cloudflare AI Gateway** | Core free (analytics, exact cache, rate limits); 5% on unified billing; DLP, Guardrails and Auto Router in beta | Edge gateway | Generic; firewall and router are beta; no shadow-eval savings proof |
| **Portkey** (acquired by **Palo Alto Networks**, closed May 2026; part of Prisma AIRS) | Dev free (10k logs); Production $49 (100k requests, +$9/100k) | Gateway, guardrails, observability | Moving into an enterprise security suite; startups underserved |
| **LiteLLM** (OSS proxy) | OSS; Enterprise ~$30k/yr (third-party figure) | Self-hosted proxy, broad provider support | Self-hosting burden; **PyPI supply-chain compromise in Mar 2026** (credential-stealing releases) hurts trust in self-run Python proxies |
| **Helicone AI Gateway** (Mintlify; **maintenance mode**) | 0% markup | Logging-first gateway | Users need a new home |
| **Requesty** | 5% markup; free 200 requests/day | Auto prompt caching (Anthropic/Gemini); PII on Enterprise only | Margin model; PII paywalled; no savings-share |
| **Not Diamond** | $0.05 per 1M tokens routed; refocused on coding-agent routing | Model router (not a gateway) | Router only |
| **Kong AI Gateway** | Enterprise (~$105 per gateway service, third-party figure) | Semantic cache, PII sanitizer, MCP proxy (Enterprise-only) | Enterprise sales, self-managed |

**No competitor charges a share of savings.** The models are % of spend (OpenRouter, Requesty, Cloudflare), per-token routing fees (Not Diamond) or flat SaaS.

**Wedge:**
1. **Savings you can audit.** A per-request ledger shows the counterfactual cost on the requested model at list price vs the actual cost, with the reason for each saving (route, response cache, prompt cache, batch). This proof makes savings-share pricing believable.
2. **Quality-guarded routing.** Cheaper models are adopted only after **shadow evaluation** on the customer's own traffic. A canary keeps sampling afterwards, and routing auto-reverts on drift. Nobody else sells "cheaper and proven not worse."
3. **The prompt-caching doctor.** It detects why cache hit rates are low (unstable prefixes, timestamps, unsorted JSON, tool-list churn) and fixes what it can automatically: it injects Anthropic cache breakpoints when none are set. Often the fastest win.
4. **A firewall built in, not an enterprise add-on.**
   - Secrets are blocked by default on every plan.
   - PII redaction with reversible tokenisation, CEL policies, per-developer budgets.
   - Works for **coding agents**: point Claude Code (`ANTHROPIC_BASE_URL`) or other OpenAI/Anthropic-compatible tools at Promptdam.
5. **Hosted, minimal-dependency Go gateway.** Low overhead, signed builds, no customer-run Python proxy to patch.

---

## 4. Product scope

### 4.1 MVP (beta Apr 26)
- **Data-plane endpoints** at `https://gateway.<domain>/v1` (BYOK; customers add provider keys, stored in `pkg/vault`):
  - OpenAI-compatible: `POST /v1/chat/completions`, `POST /v1/embeddings`, `GET /v1/models`.
  - `POST /v1/responses`: OpenAI Responses passthrough.
  - Anthropic-compatible: `POST /v1/messages` (+ `count_tokens` passthrough).
- **Providers at launch:**
  - OpenAI and Anthropic.
  - Google Gemini via its OpenAI-compatible endpoint.
  - Mistral, DeepSeek, Groq, Together, xAI and Fireworks (OpenAI-compatible).
  - Azure OpenAI, Bedrock and Vertex come post-launch.
- **Routing scope at MVP:** requests stay **within their API format family**. OpenAI-format requests can go to any OpenAI-compatible provider or model; Anthropic-format requests go to Anthropic models. Cross-format translation comes after launch (§4.2).
- **Promptdam keys** (`pd_sk_…`, via `pkg/apikeys`):
  - per project, team or developer
  - settings: allowed models, budget, policies, tags
  - attribution headers `x-promptdam-user`, `x-promptdam-route`, `x-promptdam-tags`.
- **Reliability:**
  - retries with jitter; fallback chains (model → model, provider → provider) on 429, 5xx and timeouts
  - circuit breaker per provider and model
  - per-key rate limits.
- **Cost levers:**
  - **Exact response cache:** opt-in per route; key = normalised request hash, project-scoped, TTL.
  - **Auto prompt caching for Anthropic:** when the request has no `cache_control`, add top-level automatic caching plus a breakpoint after the stable system/tools prefix (respecting the 4-breakpoint limit and per-model minimum prefix sizes).
  - **Cache doctor:** detects unstable prefixes across requests in a family and explains the fix.
  - **Routes and manual routing rules:** CEL conditions, e.g. `request.input_tokens < 1500 && !request.has_tools` routes the request to a cheaper model.
  - **Savings experiments:** auto-detected prompt families → candidate cheaper models → shadow sampling → judge → recommendation → approve → active rule with canary and auto-revert.
- **Savings ledger:** per request `counterfactual_cost`, `actual_cost`, `savings`, `reason`. A monthly verified-savings report, exportable as CSV.
- **Firewall:**
  - detectors from `pkg/redact` (secrets, PII, custom dictionaries)
  - actions block, redact (mask), tokenize (reversible, restored in responses) and alert
  - CEL policies per org, key, route or team (e.g. data-residency rules: `model.provider in ["deepseek"] && org.region == "eu" → deny`)
  - firewall events UI and alerts.
- **Budgets:**
  - scopes: org, project, key, developer, route
  - windows: daily or monthly
  - soft (alert) or hard (block with a clear error).
- **Analytics:**
  - spend by model, provider, key, route, developer and tag
  - latency (TTFB, total), errors, cache hit rates, savings over time
  - request log (metadata by default; payload capture opt-in per project with `redact` applied).
- **Platform (Keel):** orgs, members, keys, billing, usage, audit, webhooks (`budget.exceeded`, `firewall.blocked`, `experiment.ready`, `rule.reverted`).

### 4.2 Post-launch (May – Jul 2027)
- **Batch deferral:** `x-promptdam-defer: batch` sends requests to Anthropic Message Batches or the OpenAI Batch API (about 50% cheaper). Results arrive by webhook or polling.
- **Semantic cache:** embeddings, opt-in per route, similarity threshold, and an eval-guarded hit policy.
- **Learned router:** a per-family classifier (embedding + outcome features) that decides the model per request, not just per family.
- **Cross-format translation** (OpenAI ⇄ Anthropic, including tools and streaming), so any request can use any model.
- **Runlens integration:** every gateway request appears as an LLM span in the customer's Runlens project.
- **Coding-agent pack:**
  - per-developer keys with SSO-issued tokens
  - setup guides for Claude Code, Cursor and other base-URL-configurable tools
  - secrets-blocking defaults tuned for source code.
- **Employee chat:** guides for Open WebUI and LibreChat pointed at Promptdam (instant "AI firewall for employees" with no build).
- SSO, SCIM, audit export.

### 4.3 v2: Enterprise firewall (Q3 2027)
- An EU data-plane region and data-residency routing.
- A self-hosted data plane (Docker image syncing config and policies from the hosted control plane; logs stay in the customer's VPC).
- DLP integrations, SIEM export.
- No certifications (SOC 2 or ISO are out of reach for a solo company). Strict buyers get the BYOC data plane above, plus a security page, DPA and an independent pen test.

### Non-goals (until $10k MRR)
Reselling tokens or unified billing (no credit risk, no float, no fraud surface), fine-tuning or distillation, prompt management, a general API gateway.

---

## 5. Pricing (sync with `server/catalog/plans.go`)

| Plan | Price/mo | Requests/mo | Logs | Key features |
|---|---|---|---|---|
| Free | $0 | 50k (hard) | 7 days | Gateway, fallbacks, exact cache, **secrets blocking**, savings report (read-only) |
| Pro | $49 | 500k | 30 days | Auto prompt caching + cache doctor, routing rules, budgets and alerts, PII redaction, ledger export |
| Team | $199 | 3M | 90 days | Savings experiments (shadow eval) + auto-revert, CEL firewall policies, per-developer keys, audit log |
| Business | $499+ | 10M | 1 year | Custom policies, payload-free mode guarantees, DPA, SLA, SSO, priority support |
| **Savings plan** | **15% of verified savings** (min $500/mo) | custom | 1 year | Everything in Business; for teams spending above $5k/mo on LLMs |

- **Overage** (soft on paid plans): $5 per extra 100k requests on Pro, $3 on Team, $2 on Business.
- **Savings plan billing:**
  - a $500/mo base price that covers 15% of the first $3,333 saved
  - a metered `savings_share` (USD cents) Stripe meter, reported monthly from the verified ledger as `max(0, 0.15 × savings − 500)`
  - the customer approves the monthly savings report before invoicing, with a dispute window of 7 days.
- **Shadow evaluation cost:** experiments call models with the **customer's own provider keys**. Every experiment has a visible budget cap (default $25/experiment, $100/mo), and its cost is subtracted in the savings ledger.
- **Path to $10–15k MRR:** for example, 50 Pro ($2.45k) + 25 Team ($5k) + 5 Business ($2.5k) + 3 savings plans (~$1.3k each, $3.9k) ≈ $13.8k. The catalog benchmark is about 12 × $1.3k at 15% of savings.
- **Meters:** `requests` (soft on paid, hard on Free), `savings_share` (savings plan), `ai_credits` (judge calls when the customer opts for Promptdam-paid judging, e.g. no Anthropic key), `seats`.

---

## 6. Architecture

```
 customer app / coding agent / internal tool  (OpenAI SDK, Anthropic SDK, Vercel AI SDK, LangChain, Claude Code …)
   │  base_url = https://gateway.<domain>/v1     Authorization: Bearer pd_sk_…
   ▼
 ┌──────────────── gateway (Cloud Run, min 2, raw http.Handler hot path) ─────────────────┐
 │ key check (checksum → in-memory → Valkey) → budget/rate (local bucket + Valkey)         │
 │ → parse request (OpenAI | Anthropic) → family id (hash of normalised system+tools)      │
 │ → firewall: redact scan + CEL policy → block | redact | tokenize | alert                 │
 │ → route: rules (CEL) → served model (+ fallback chain) → cache lookup (exact)            │
 │ → prompt-cache injection (Anthropic) → provider adapter (BYOK key from vault cache)      │
 │ → stream passthrough (SSE tee: usage + response-side scan) → detokenize → client         │
 │ → async: ledger row → events.Writer (ClickHouse) · metering · shadow sample enqueue      │
 └──────────────────────────────────────────────────────────────────────────────────────────┘
 ┌──── worker (River) ─────────────────────────┐   ┌──── api.<domain> (muzak + keel) ─────────┐
 │ family stats · experiment runner (shadow     │   │ providers, keys, routes, rules, policies,│
 │ calls + judge) · canary checks · auto-revert │   │ budgets, experiments, analytics, ledger  │
 │ · savings reports · cache doctor · alerts    │   └──────────────────────────────────────────┘
 └──────────────────────────────────────────────┘            ▲ app.<domain> (Nuxt SPA, @keel/dashboard)
```

### 6.1 Hot path (`features/gateway`)
- **Overhead budget:** p50 < 10 ms, p99 < 40 ms excluding the provider. No synchronous DB calls on the hot path.
  - Keys, budgets, routes, rules, policies and provider credentials are served from in-memory snapshots refreshed through Valkey pub/sub.
  - Counters use the local token bucket + Valkey hybrid (Keel K7.2).
- **Streaming:**
  - Bytes are forwarded as they arrive. A tee parses SSE events for usage (OpenAI `usage` chunk with `stream_options.include_usage` injected; Anthropic `message_start` / `message_delta` usage) and runs response-side scans (Keel K7.1).
  - Response-side PII found while streaming defaults to **alert**, because blocking mid-stream breaks UX. **Tokenized placeholders** are restored in-stream.
- **Request-side firewall:** scans all text parts (system, messages, tool results; text extracted from images is out of scope). It decides before anything is sent upstream, so blocked data never leaves.
- **Fail behaviour:**
  - On an internal error, Promptdam fails open to direct upstream for routing and caching, but **never** for firewall decisions. A firewall-scanner error under a `block` policy fails closed, configurable per org.
  - A provider outage triggers the fallback chain, then a clear error with a `x-promptdam-trace` id.
- **Headers returned:** `x-promptdam-trace`, `x-promptdam-served-model`, `x-promptdam-cache` (hit/miss/prompt), `x-promptdam-savings-micros`.

### 6.2 Families, experiments and the savings engine (`features/families`, `features/experiments`)
1. **Family ID:** hash of the normalised system prompt (numbers, dates, UUIDs and whitespace normalised) + tool names + response format, or an explicit `x-promptdam-route`. Stats are kept per family: volume, tokens, cost, model mix, latency, error rate.
2. **Candidate selection:** a family qualifies with ≥ 1,000 requests/week and ≥ $50/week spend. Candidates are cheaper models with the required capabilities (tools, JSON/structured output, context ≥ p99 input, vision if needed), taken from the `pkg/llm` catalog.
3. **Shadow sampling:**
   - The worker re-sends a sample (default 5%) of the family's requests to the candidate, asynchronously and outside the user's path, within the budget cap.
   - Payload capture for the sample is held in memory or briefly in R2 with a 24h TTL, encrypted, and deleted after judging.
4. **Judging:**
   - Deterministic checks first: JSON schema validity, tool-call name and argument equivalence, exact match for classification-like outputs.
   - Otherwise a pairwise LLM judge (`claude-sonnet-5-5` by default, or `claude-haiku-4-5` for simple rubric checks) with a rubric (correctness, completeness, instruction/format adherence) and position-bias control (randomised order).
   - Each sample is recorded as a win, tie or loss.
5. **Recommendation:**
   - Requires ≥ 200 samples and a win + tie rate ≥ the threshold (default 95%, set per route).
   - Shows projected monthly savings with confidence intervals, plus example pairs (redacted).
   - The customer approves, or Team+ can enable auto-apply for low-risk families.
6. **Canary:** after activation, 1% of routed requests also run on the baseline model and are judged. When quality drops below the threshold over a rolling window, the rule **auto-reverts** and fires `rule.reverted`.
7. **Ledger:** per request, `counterfactual_cost = list price(requested model, tokens, no cache)` and `actual = provider-reported usage × list price` (cache read and write pricing included). Savings are attributed to their reason. Experiment and judge costs are recorded as negative savings.

### 6.3 Prompt-caching doctor (`features/cachedoctor`)
- For Anthropic-format families:
  - Compare consecutive requests' prefixes (tools → system → messages render order). Find the first divergent byte offset and classify the cause: timestamp, request id, unsorted JSON, tool-list churn, user content too early.
  - Report the expected hit rate vs actual (`cache_read_input_tokens`) and the dollar impact, with concrete fix snippets.
- **Auto-fix (opt-in per route):**
  - Add top-level automatic `cache_control` when none is present.
  - Add an explicit breakpoint at the end of the stable system/tools prefix when that prefix exceeds the model's minimum cacheable size.
  - Never add more than 4 breakpoints in total.
  - Never reorder content: reordering changes semantics.
- **OpenAI and Gemini** cache implicitly. There the doctor only reports prefix-stability problems.
- **Economics shown to the user:** cache reads cost about 0.1× base input (lower on some models), writes 1.25× for the 5-min TTL or 2× for the 1-hour TTL. The doctor only recommends 1-hour TTL when the start-to-start gaps in a family are 5–60 minutes.

### 6.4 Firewall (`features/firewall`)
- Detectors come from `pkg/redact`, plus Promptdam presets:
  - **"source code"** preset for coding agents (`.env` patterns, private keys, cloud credentials, connection strings)
  - **"customer data"** preset (email, phone, card, IBAN, national IDs).
- CEL policies (`pkg/policy`) see `principal` (key, team, developer), `request` (model, provider, route, tokens, has_tools), `findings` (kinds, counts, locations) and `org` (region, plan).
- **Actions:**
  - `block`: 4xx with a code and finding kinds, never the secret itself
  - `redact` (mask)
  - `tokenize`: `⟦EMAIL_1⟧`; the mapping lives only in request memory and is restored in the response
  - `alert`
  - `route_to` (e.g. send PII-bearing requests to an approved provider or region).
- Firewall events go to ClickHouse with masked excerpts. Alerts go through `pkg/notify`. A weekly digest summarises "secrets blocked, PII redacted".

### 6.5 Data model
**Postgres** (product Ent graph; `org_id` / `project_id` via TenantMixin):

| Table | Key columns |
|---|---|
| `providers` | kind (openai/anthropic/gemini/…/custom), name, base_url, key_ref (vault), regions, enabled, priority |
| `key_settings` | api_key_id (core apikeys), allowed_models[], default_route, budget_id, policy_set_id, tags, developer_email |
| `routes` | name, match (header / family ids), cache_policy, prompt_cache_autofix, quality_threshold, fallback_chain |
| `families` | hash, label (auto/human), sample_ref (redacted, TTL), first_seen, stats snapshot |
| `routing_rules` | route_id / family_id, from_model, to_model, condition (CEL), status (proposed/active/reverted/disabled), activated_by, activated_at, reverted_at, reason |
| `experiments` | family_id, baseline_model, candidate_model, sample_rate, budget_micros, spent_micros, samples, wins, ties, losses, status, est_monthly_savings_micros |
| `policy_sets` | name, rules jsonb (CEL), version, default_effect |
| `budgets` | scope (org/project/key/developer/route), scope_id, window (day/month), limit_micros, action (alert/block), thresholds[] |
| `savings_reports` | period, verified_savings_micros, share_micros, status (draft/approved/disputed/invoiced), approved_by |
| `alert_rules` | kind (spend_spike, error_rate, provider_down, budget, firewall), params, channel_ids[] |

**ClickHouse:**
- `requests(org_id, project_id, key_id, route, family_id, provider, requested_model, served_model, api_format, stream, status, http_status, ttfb_ms, latency_ms, upstream_ms, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_micros, counterfactual_micros, savings_micros, savings_reason, response_cache_hit, firewall_action, findings_kinds Array(LowCardinality(String)), developer, tags Array(String), error_code, payload_ref, ts, retention_days)`
- `firewall_events(…, finding_kind, location, action, policy_rule, excerpt_masked, ts)`
- `experiment_samples(experiment_id, request_id, verdict, scores, baseline_cost, candidate_cost, judge_cost, ts)`

All tables have TTL by plan, and `ORDER BY` starts with `(project_id, ts)`.

### 6.6 Folder layout (Keel standard from §5 of the root doc, with Promptdam specifics)
```
products/promptdam/
├── server/
│   ├── cmd/{api,gateway,worker,migrate}/
│   ├── features/
│   │   ├── gateway/        hot path: parse, snapshot config, route, cache, stream tee, ledger emit
│   │   ├── providers/      adapters (openai-compatible, anthropic, gemini-compat, azure later), errors → fallback
│   │   ├── families/       normalisation, hashing, stats
│   │   ├── experiments/    candidate selection, shadow runner, judge, canary, auto-revert
│   │   ├── cachedoctor/    prefix-stability analysis, autofix
│   │   ├── firewall/       presets, policy evaluation, tokenisation, events
│   │   ├── budgets/        counters, windows, enforcement, alerts
│   │   ├── ledger/         counterfactual pricing, savings attribution, reports
│   │   ├── analytics/      ClickHouse queries
│   │   └── audit/          "LLM bill audit" lead-magnet analyser (CSV/usage import → savings estimate)
│   ├── schemas/ handlers/ routers/
│   ├── catalog/plans.go
│   ├── ent/schema/
│   └── migrations/{postgres,clickhouse}/
├── web/{app,site,admin,api}/       (root pnpm workspace members; extend @keel/* layers)
│   └── app/pages: index (spend + savings), requests/, requests/[id], families/, families/[id], experiments/,
│                  routes/, firewall/, budgets/, providers/, savings/ (ledger + reports), settings/* (keel)
├── bench/                         latency + throughput harness (vegeta/k6 scripts, fake upstream)
├── infra/{dev,prod}/              (root infra/terraform/modules; later a multi-region gateway)
└── docs/{adr,interviews}/
```

---

## 7. Timeline and phases (detailed todos)

Prerequisites: core milestones `core-v0.3.0` (policy, vault) and `pkg/llm` from v0.2. Keel K7 (streaming redact, hybrid limiter, budget counters) is built inside S1–S3 and tagged `core-v0.4.0`. Runlens and Plugport post-launch work takes about 20% of time during this build.

### S0: Scaffold and spikes (Mon Mar 8 – Wed Mar 10)
- [ ] S0.1 `keelctl new-product promptdam --prefix pd --domain <domain>` scaffolds `products/promptdam/`; CI path filter and the deploy workflow are green.
- [ ] S0.2 Spike: streaming passthrough + usage tee for OpenAI Chat Completions, OpenAI Responses, Anthropic Messages and Gemini OpenAI-compat. Measure the added latency against direct calls.
- [ ] S0.3 `bench/` harness with a fake upstream (configurable latency and streaming) for repeatable overhead measurement in CI.
- [ ] S0.4 Catalog (`plans.go`), Stripe sync (test), real landing page; recruit 10 design partners with ≥ $2k/mo LLM spend (waitlist, interviews, Runlens customers with high cost-per-run).
- **Exit:** proxy overhead p99 < 40 ms on the bench with streaming; OpenAI and Anthropic SDKs work unmodified with only `base_url` changed.

### S1: Gateway core (Thu Mar 11 – Mon Mar 22)
- [ ] S1.1 Provider registry + BYOK credentials in `pkg/vault`, provider key validation call, per-provider settings.
- [ ] S1.2 Endpoints: chat/completions, responses (passthrough), messages, embeddings, models; request parsing for both formats; error normalisation.
- [ ] S1.3 Promptdam keys (key_settings: allowed models, tags, developer), attribution headers.
- [ ] S1.4 Config snapshots in memory (keys, routes, rules, policies, providers) with Valkey pub/sub refresh; no DB on the hot path.
- [ ] S1.5 Retries, fallback chains, circuit breakers; provider error taxonomy → fallback decisions.
- [ ] S1.6 Keel K7.2: hybrid local/Valkey limiter + budget counters (micro-USD), budgets CRUD and enforcement, alerts.
- [ ] S1.7 Cost calculation via the `pkg/llm` catalog (incl. cache read/write pricing); ClickHouse `requests` writer; metering `requests`.
- [ ] S1.8 Dashboard: providers, keys, requests log (metadata), spend analytics (by model/provider/key/developer/tag), budgets.
- [ ] S1.9 Quickstarts: OpenAI SDK (Py/TS), Anthropic SDK (Py/TS), Vercel AI SDK, LangChain, Claude Code (`ANTHROPIC_BASE_URL`), curl.
- **Exit:** design partners route production traffic through Promptdam with fallbacks and budgets; spend analytics match provider invoices within ±2%.

### S2: Cost levers (Tue Mar 23 – Wed Mar 31)
- [ ] S2.1 Families: normalisation + hashing, stats rollups, families UI (top families by spend).
- [ ] S2.2 Exact response cache (opt-in per route; Valkey with size limits; TTL; project-scoped; `x-promptdam-cache` header).
- [ ] S2.3 Prompt-caching doctor: prefix divergence analysis, cause classification, dollar impact, fix snippets.
- [ ] S2.4 Anthropic auto prompt caching (top-level automatic + stable-prefix breakpoint; respect the 4-breakpoint limit and minimum sizes); verify through `cache_read_input_tokens`.
- [ ] S2.5 Routes + manual routing rules (CEL conditions, same-format targets only), rules UI with dry-run on recent traffic ("would have saved $X").
- [ ] S2.6 Savings ledger (counterfactual pricing, reasons) and savings dashboard.
- **Exit:** at least 3 design partners show ≥ 15% verified savings from caching and manual rules alone.

### S3: Firewall and savings experiments (Thu Apr 1 – Mon Apr 12)
- [ ] S3.1 Keel K7.1: streaming-safe redact scanner with chunk overlap; reversible tokenisation restored in streamed responses.
- [ ] S3.2 Firewall presets (source code, customer data), CEL policy sets, actions (block/redact/tokenize/alert/route_to), fail-closed option.
- [ ] S3.3 Firewall UI (events with masked excerpts, stats), alerts, weekly digest.
- [ ] S3.4 Experiments: candidate selection by capabilities, shadow runner within budget caps, deterministic checks + pairwise LLM judge with position randomisation.
- [ ] S3.5 Recommendations UI (savings projection with confidence, example pairs, approve) → active routing rule.
- [ ] S3.6 Canary sampling + auto-revert + `rule.reverted` webhook/email.
- [ ] S3.7 Tag core milestone **`core-v0.4.0`**.
- **Exit:** an end-to-end experiment on a design partner's family reaches a recommendation; a forced quality drop triggers auto-revert within one window; secrets in a test prompt are blocked before leaving.

### S4: Billing, launch readiness and lead magnet (Tue Apr 13 – Fri Apr 23)
- [ ] S4.1 Billing: plans, `requests` overage meter, savings-plan base + `savings_share` meter, the monthly savings report approval flow.
- [ ] S4.2 "LLM Bill Audit" free tool on the site. The user uploads OpenAI/Anthropic usage exports (CSV), or a 7-day sample through Promptdam's free plan. Output:
  - an estimated savings breakdown (model downgrade potential by family size, caching potential, batch potential)
  - a PDF/email report and a CTA.
- [ ] S4.3 Docs: concepts (families, rules, experiments, ledger, firewall), provider setup, migration guides (from OpenRouter, LiteLLM, Portkey, Helicone), security and data handling page, API reference, `llms.txt`.
- [ ] S4.4 Load test at 1k req/s per instance with streaming; multi-zone Cloud Run (min 2); uptime checks; status page.
- [ ] S4.5 Security pass: vault for provider keys, payload-free default, tenant isolation, SSRF guard on custom provider URLs, dependency audit, signed container images.
- [ ] S4.6 Prod deploy; go-live checklist (§16 of the root doc).
- **Exit:** ≥ 5 design partners in production, ≥ 2 willing to sign the savings plan. → **Beta Mon Apr 26**.

### S5: Beta → launch (Mon Apr 26 – Fri May 14)
- [ ] S5.1 Weekly partner calls; fix the top issues; tune detector precision and judge thresholds.
- [ ] S5.2 Case studies with real numbers ("$X/mo → $Y/mo, quality unchanged, evidence attached").
- [ ] S5.3 Launch assets: demo video ("one line change, 41% cheaper"), Show HN, Product Hunt, comparison pages (vs OpenRouter, LiteLLM, Portkey, Cloudflare AI Gateway, Vercel AI Gateway, Helicone), "Secrets your coding agent sent to the LLM" blog post (with anonymised aggregate data, if partners opt in).
- [ ] S5.4 Stripe live mode; savings-plan contract template (click-through terms with dispute window).

### Launch: Tue May 18, 2027
- [ ] Show HN + Product Hunt + X/LinkedIn; r/LocalLLaMA, r/LLMDevs, r/ExperiencedDevs (cost angle); engineering-manager newsletters.
- [ ] Outbound to Runlens customers with high cost-per-run ("Runlens shows the cost; Promptdam cuts it").

### S6: Post-launch (May – Jul 2027; shares time with Runlens for Games, May 19 – Jun 25)
- [ ] S6.1 Batch deferral (Anthropic Message Batches, OpenAI Batch).
- [ ] S6.2 Semantic cache with eval guard.
- [ ] S6.3 Cross-format translation (OpenAI ⇄ Anthropic incl. tools + streaming), unlocking cross-provider routing.
- [ ] S6.4 Learned per-request router.
- [ ] S6.5 Runlens span export; coding-agent pack (per-developer SSO keys); employee-chat guides (Open WebUI, LibreChat).
- [ ] S6.6 SSO/SCIM; Azure OpenAI, Bedrock and Vertex providers.

### S7: Enterprise firewall (Q3 2027)
- [ ] S7.1 EU data-plane region + residency routing.
- [ ] S7.2 Self-hosted data plane (config sync, local logs).
- [ ] S7.3 SIEM export; security page, DPA and an independent pen test. No certifications: BYOC (S7.2) is the answer for buyers who need data to stay in their cloud.

---

## 8. Go-to-market
- **Channels (ranked):**
  1. The "LLM Bill Audit" lead magnet (high intent, shareable numbers).
  2. Cross-sell from Runlens (cost data already there).
  3. Migration guides for orphaned or acquired-gateway users (Helicone, Portkey-to-enterprise).
  4. A content series: "where your LLM money goes" and caching deep dives.
  5. HN/PH launch.
  6. A coding-agent security angle aimed at platform/security leads.
- **Sales motion:** self-serve to Team. The savings plan is the only plan with a call (optional 20-minute review of the savings report). Still no custom contracts below $2k/mo.
- **Activation:** ≥ 1,000 requests through Promptdam within 7 days **and** the savings or firewall dashboard viewed.
- **Expansion:** request volume, developers (keys), savings share growing with the customer's spend.

## 9. Metrics
| Metric | Beta target | Launch + 90 days |
|---|---|---|
| Orgs routing traffic | 8 | 150 |
| Monthly requests through Promptdam | 5M | 150M |
| Median verified savings (orgs with experiments) | 25% | 30% |
| Secrets blocked (all orgs, monthly) | tracked | tracked (marketing stat) |
| Paying orgs | 0 (coupons) | 40 |
| MRR | $0 | $3,000 |
| Gateway overhead p99 | < 40 ms | < 40 ms |
| Gateway availability | 99.9% | 99.95% |

## 10. Risks
| Risk | Mitigation |
|---|---|
| Passthrough gateways are free or zero-markup (Vercel, Cloudflare) | Never charge for passthrough; charge for proven savings and the firewall; Free plan includes the gateway |
| Providers cut prices and cache implicitly, shrinking savings | Savings-share aligns incentives; the levers diversify (routing, caching fixes, batch, semantic cache); value shifts to firewall and budgets |
| Routing hurts quality | Shadow eval before adoption, canary after, auto-revert, per-route thresholds, customer approval by default |
| Hot-path reliability and latency | Go, in-memory snapshots, no DB on the hot path, multi-zone, fallbacks, bench in CI, documented fail-open client pattern |
| Trust: a proxy sees all prompts | Payload-free logging by default, vault-encrypted keys, minimal dependencies, signed images, DPA, EU region and BYOC data plane (S7), no certification claims; contrast with self-run proxies after the Mar 2026 LiteLLM supply-chain incident |
| Savings-share disputes | Transparent per-request ledger, monthly approval with a dispute window, conservative counterfactual (list prices, provider-reported usage) |
| API format translation complexity | MVP routes only within a format family; translation is post-launch with golden tests |

## 11. Decisions needed
- [x] Name and domain: **Promptdam**, promptdam.dev + promptdam.com (decided 2026-10-03; register the domains).
- [ ] `DECISION NEEDED:` savings-share percentage and minimum (recommendation: 15%, $500/mo minimum, offered only above $5k/mo LLM spend).
- [ ] `DECISION NEEDED:` default judge model for experiments (recommendation: `claude-sonnet-5-5` pairwise judge, run on the customer's Anthropic key when present; otherwise billed as AI credits).
- [ ] `DECISION NEEDED:` firewall fail-closed default for `block` policies (recommendation: fail closed for secrets, fail open for everything else).
- [ ] `DECISION NEEDED:` include OpenAI Responses API routing at MVP (recommendation: passthrough only; routing later).

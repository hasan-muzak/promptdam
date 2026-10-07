# Promptdam: cut the LLM bill, block the leaks

> **Status:** building (S0 done 2026-10-07; S1 and S2 backends done 2026-10-07, exits pending design partners; S3 model routers in progress since 2026-10-08) · **Owner:** Hasan · **Written:** 2026-10-03
> **MVP build:** Mar 8 – Apr 23, 2027 · **Beta:** Apr 26, 2027 · **Public launch:** Tue May 18, 2027 (moved 5 weeks later to make room for Fixwire for Mobile)
> **Catalog:** #10 "AI Cost Optimization API" + #17 "AI Firewall". Revenue potential: High.
> **Location:** `hasan-muzak/promptdam` (moves to the product's organization once the name is final), a submodule of the keel workspace at `keel/products/promptdam/promptdam/`. Shared Go packages are the core module (`keel/core/pkg/`, `github.com/hasan-muzak/keel-core`), shared Nuxt layers are in `keel/core/packages/`.
> **Built on:** Keel (`../../project.md`). Read it first for the stack, conventions, billing and shared modules.
> **Name (final):** **Promptdam**. A dam holds back spend and stops leaks. Owned by Badele LLC.
> - Domains: **promptdam.dev** (product) and **promptdam.com** (redirect). Both were unregistered on 2026-10-03 (RDAP/WHOIS check); register them now and run a trademark search before launch.
> - "Sluice" was dropped because sluice.dev and sluice.ai are taken.

---

## 1. One-liner and pitch

**Change one line (`base_url`) and Promptdam cuts your LLM bill 30–60% with proven, quality-guarded routing and caching, while blocking secrets and PII before they leave your company. Your provider bills you as before; Promptdam is a flat subscription.**

LLM spend is now a top-3 cost line for many software companies. Most of it is waste:
- the most expensive model is used for every request, including trivial ones
- prompt caching is missing or broken because a timestamp sits in the system prompt
- identical requests are paid for twice
- non-urgent jobs run at full price instead of batch price.

At the same time, apps and coding agents send `.env` files, customer emails and card numbers to third-party providers.

**Promptdam is an OpenAI- and Anthropic-compatible gateway (BYOK) that:**
1. Sends every request to the cheapest model that does it as well:
   - **model routers:** tiers the customer describes in plain words ("fast", "reasoning", "expert"…), across any providers, with each request placed in a tier by rules, a classifier learned from the customer's own traffic, and a decision model when unsure
   - every tier verified against the strongest model on real requests before and after it serves them
   - caching fixes (response cache, prompt caching, the cache doctor)
2. Proves every saved dollar in a per-request ledger.
3. Enforces a firewall: secrets and PII detection, redaction and blocking, CEL policies, budgets.

Basic routing and logging are free elsewhere, so Promptdam's plans are priced on what they add (budgets and attribution, the firewall, fallbacks, proven savings), not on passing traffic through. It never resells tokens: customers keep their own provider keys and bills.

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
2. **Per-request routing that is measured, not guessed.** A router places each request in one of the customer's tiers, across providers and API formats. Tiers serve traffic only after **shadow evaluation** on the customer's own requests shows they hold up against the strongest model. The evaluation keeps sampling afterwards, and a tier that drifts is demoted on its own. Others route by price, availability or a vendor's generic difficulty model; nobody sells "cheaper and proven not worse, on your traffic."
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
- **Routing across API formats (moved into the MVP, decided 2026-10-08):** an OpenAI Chat Completions request can be served by an Anthropic model and an Anthropic Messages request by any OpenAI-compatible one. The gateway translates requests, answers and streams, tools and images included, so fallbacks, rules and router tiers can name any provider. The Responses API stays passthrough to OpenAI-format providers.
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
  - **Model routers (§6.2):** tiers the project describes, called like a model (`"model": "auto"`) or attached to a route. Each call gets a tier from: the client's explicit choice, the models' capabilities, rules, a classifier learned from judged samples of the project's traffic, an opt-in decision model when the classifier is unsure, else the "unsure" tier. Shadow mode first (decide and evaluate, serve the strong tier), then active, with a canary that demotes a tier whose quality drifts.
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
- **Platform (Keel):** orgs, members, keys, billing, usage, audit, webhooks (`budget.exceeded`, `firewall.blocked`, `router.ready`, `router.tier_demoted`).

### 4.2 Post-launch (May – Jul 2027)
- **Batch deferral:** `x-promptdam-defer: batch` sends requests to Anthropic Message Batches or the OpenAI Batch API (about 50% cheaper). Results arrive by webhook or polling.
- **Semantic cache:** embeddings, opt-in per route, similarity threshold, and an eval-guarded hit policy.
- **Router extras:** the Responses API through routers (with translation), a classifier that also learns from the decision model's choices (fewer decision calls over time), bandit exploration between neighbouring tiers, and a Promptdam-paid judge billed at what the provider charges us (§5).
- (The learned per-request router and cross-format translation moved into the MVP on 2026-10-08.)
- **Fixwire integration:** every gateway request appears as an LLM span in the customer's Fixwire project.
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
| Free | $0 | 50k (hard) | 7 days | Gateway, fallbacks, exact cache, **secrets blocking**, savings report (read-only), one router in shadow mode (the report shows what routing would save) |
| Pro | $49 | 500k | 30 days | Auto prompt caching + cache doctor, routing rules, active routers with explicit tiers and rules, budgets and alerts, PII redaction, ledger export |
| Team | $199 | 3M | 90 days | Learned routing (evaluation, classifier, decision model) + auto-demotion, CEL firewall policies, per-developer keys, audit log |
| Business | $499+ | 10M | 1 year | Custom policies, payload-free mode guarantees, DPA, SLA, SSO, priority support |

- **Overage** (soft on paid plans): $5 per extra 100k requests on Pro, $3 on Team, $2 on Business.
- **Savings plan: deferred (decided 2026-10-07).** Promptdam bills a subscription plus gateway requests only, never tokens and never a share of spend or savings. The savings ledger stays as proof of value ("Promptdam saved you $X this month"), not as a basis for invoices. The design below is kept for a later decision, if large customers ask:
  - a $500/mo base price that covers 15% of the first $3,333 saved
  - a metered `savings_share` (USD cents) Stripe meter, reported monthly from the verified ledger as `max(0, 0.15 × savings − 500)`
  - the customer approves the monthly savings report before invoicing, with a dispute window of 7 days.
- **Router tiers per plan (my proposal, 2026-10-08; change freely):** Free gets one router in shadow mode, so the report shows the savings before any upgrade; Pro serves traffic through routers with explicit tiers and rules; Team adds the learned parts (evaluation, classifier, decision model, auto-demotion).
- **Evaluation cost:** a router's evaluation (replays, judge and decision-model calls) runs on the **customer's own provider keys** (decided 2026-10-08). Every router has a visible monthly budget cap (default $100), and the cost is subtracted in the savings ledger.
  - Later, if customers ask: a Promptdam-paid judge on Promptdam's own keys, charged at what the provider charges us.
- **Path to $10–15k MRR:** for example, 60 Pro ($2.9k) + 30 Team ($6k) + 8 Business ($4k) ≈ $12.9k, before request overage.
- **Meters:** `requests` (soft on paid, hard on Free). `ai_credits` comes only with a Promptdam-paid judge; seats are a plan limit, not a meter.

---

## 6. Architecture

```
 customer app / coding agent / internal tool  (OpenAI SDK, Anthropic SDK, Vercel AI SDK, LangChain, Claude Code …)
   │  base_url = https://gateway.<domain>/v1     Authorization: Bearer pd_sk_…
   ▼
 ┌──────── gateway container(s) on the Promptdam VPS (raw http.Handler hot path) ─────────┐
 │ key check (checksum → in-memory → Valkey) → budget/rate (local bucket + Valkey)         │
 │ → parse request (OpenAI | Anthropic) → family id (hash of normalised system+tools)      │
 │ → firewall: redact scan + CEL policy → block | redact | tokenize | alert                 │
 │ → route: router (tier cascade) | rules (CEL) → served model (+ fallback chain)           │
 │ → cache lookup (exact) → translate when the target speaks the other API format           │
 │ → prompt-cache injection (Anthropic) → provider adapter (BYOK key from vault cache)      │
 │ → stream passthrough (SSE tee: usage + response-side scan) → detokenize → client         │
 │ → async: ledger row → events.Writer (ClickHouse) · metering · evaluation queue (memory)  │
 │   evaluator: replays on other tiers + judge, on the project's keys → router_evals       │
 └──────────────────────────────────────────────────────────────────────────────────────────┘
 ┌──── worker (River) ─────────────────────────┐   ┌──── api.<domain> (muzak + keel) ─────────┐
 │ classifier training + calibration · canary   │   │ providers, keys, routes, routers, rules, │
 │ checks · auto-demotion · savings reports ·   │   │ policies, budgets, analytics, ledger     │
 │ cache doctor · alerts                        │   └──────────────────────────────────────────┘
 └──────────────────────────────────────────────┘    ▲ app.<domain>: Promptdam's own dashboard (built from @keel/app-kit)
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
  - On an internal error, Promptdam fails open to direct upstream for routing and caching, but **never** for firewall decisions. A firewall-scanner error under a `block` policy fails closed for secrets and open for everything else, configurable per project (decided 2026-10-08).
  - A provider outage triggers the fallback chain, then a clear error with a `x-promptdam-trace` id.
- **Headers returned:** `x-promptdam-trace`, `x-promptdam-served-model`, `x-promptdam-cache` (hit/miss/prompt), `x-promptdam-savings-micros`.

### 6.2 Model routers: tiers, the cascade, evaluation and learning (`features/routers`, `features/evaluate`, `features/classifier`, `features/translate`)

Model routing is one of Promptdam's core features (decided 2026-10-08, replacing per-family experiments; ADR `docs/adr/0001-model-routers.md`). It must be flexible (any tiers, any providers), fast (no extra call for most requests), safe (mistakes lean to the stronger model and roll back on their own) and measured (every claim about quality comes from judged answers on the customer's own traffic).

**The principles (agreed 2026-10-07, kept on 2026-10-08).**
- **We don't guess difficulty from surface features.** A two-line question ("is this contract clause enforceable?") can need a strong reasoning model; fifty paragraphs with "how many times is word x used?" may not. Length alone never moves a request to a weaker model.
- **The customer says what each tier is for; outcomes say where the boundaries are.** Tier descriptions and rules are the customer's words. Which requests a tier really handles well is learned from judged answers on their traffic, never assumed.
- **The baseline is the customer's own choice:** the model a call asked for, else the router's strongest tier. The claim is "not worse than the baseline, on your traffic", never "the best model".
- **Safety is asymmetric.** A hard request sent to a weak model costs quality; an easy one sent to a strong model only costs money. When unsure, every step picks the stronger tier, failures fall back upwards, and a tier that drifts is demoted on its own.
- **Limits, said plainly:**
  - No router is 100% accurate. What we promise is measured quality per tier (with its confidence interval), mistakes leaning up, and rollback.
  - The judge can be wrong. Deterministic checks come first, and a sample is judged in both orders to catch position bias.
  - A tier needs judged samples before the classifier sends it anything (200 by default). Until then, only explicit choices, rules and the decision model place requests there.
  - Customers can keep critical routes out of any router.

**What a project sets up: routers.**
- A router has a name and is called like a model (`"model": "auto"`), or a route sends all its calls through it (`router` on the route).
- **Tiers,** ordered from cheapest to strongest, any number up to 8. Each has a name, a description in plain words of the work it is for, a model with up to 5 fallbacks (any provider: the gateway translates between API formats), and up to 20 example requests.
- **Rules:** CEL conditions over the request (as routing rules see it), in order, each sending matching calls to a tier.
- **The unsure tier** serves calls nothing places with confidence: the strongest tier by default. When a route sends a call for a named model through a router, the unsure choice is that model.
- **The quality floor** (default 95%): the share of a tier's answers that must hold up against the baseline's (judged as good or better).
- **Evaluation settings:** sample rate (default 2%; 10% in shadow mode), monthly evaluation budget (default $100), the judge model (the project's choice on one of its providers; `claude-sonnet-5-5` by default when it has an Anthropic provider) and an optional decision model.
- **Modes:** `shadow` (decide and evaluate every sampled call, but serve the unsure tier: the dry-run on live traffic), `active`, `disabled`.

| Tier | Model | For (the customer's words) |
|---|---|---|
| fast | claude-haiku-4-5 | greetings, formatting, extraction, yes/no classification |
| standard | a Gemini Flash model (OpenAI format) | summaries, rewriting, simple questions |
| reasoning | claude-sonnet-5-5 | multi-step reasoning, code, analysis |
| expert | claude-opus-5-5 | legal or financial judgment, long planning |

**How a call gets its tier: a cascade.** The cheapest check runs first, and each step runs only when the one before it didn't decide. The tier, the step that chose it and its confidence are logged with the call (`tier`, `tier_source`, `tier_confidence`).
1. **Explicit:** `"model": "auto:expert"` or `X-Promptdam-Tier: expert`. Developers often know.
2. **Requirements (a filter, not a guess):** tools, images, a JSON schema, and the context needed (estimated input plus `max_tokens`) against each tier's model, from the price list's capabilities and context windows. A tier whose model can't serve the call is skipped upwards. Unknown capabilities count as present; the fallback chain covers a provider that refuses.
3. **Rules,** in order.
4. **The classifier,** inside the gateway: microseconds, no call. It estimates, for each tier below the baseline, the probability that the tier's answer holds up, and picks the cheapest tier whose probability clears that tier's calibrated threshold.
5. **The decision model,** opt-in, only when the classifier is unsure or not trained yet. It reads the tier descriptions and examples and an excerpt of the request, and answers a tier (structured output).
   - A hard timeout (default 150 ms) falls through to step 6. Answers are cached per router, family and last message for 10 minutes.
   - It runs on the project's own key, and its cost counts against the call. Any model works: OpenRouter's decision models (on 2026-10-07 the cheapest were Respan span-01 at $0.02 per 1M input tokens; Jev, Liquid d1 and Perplexity's decider about $0.04; Inception's mercury-decide free) or a small chat model.
   - Target: under 10% of a router's calls get here once the classifier is trained. Measure it in beta.
6. **The unsure tier.**
- Then: a tier's own fallbacks, then the next tier up, on rate limits, overload, server errors and timeouts.

**Evaluation (in the gateway, off the call's path; `features/evaluate`).**
- A sample of a router's calls goes to an in-memory queue (bounded; samples are dropped when it is full). Requests are never stored: they live in the gateway's memory until judged.
- Each sample is replayed, not streamed, on every tier cheaper than the baseline, and on the baseline when another tier served the call (a served baseline answer is reused).
- **Judging:** deterministic checks first (valid JSON, the schema, tool names and arguments, exact match, refusals). Otherwise a pairwise judge with a rubric (correctness, completeness, following the instructions and format) and the answers in random order; 10% are judged in both orders, and a disagreement counts as a tie.
- Every verdict is a row of `router_evals`: the tiers, the verdict, the costs, and the request's features as hashes (never its text).
- Replays, judge and decision-model calls run on the project's own provider keys (decided 2026-10-08), within the router's monthly budget, and count as the project's spend. Their cost is subtracted from the router's savings.

**Learning (in the worker; `features/classifier`).**
- **Features** cost nothing to compute: hashed words and word pairs of the last user message and of the instructions, estimated input tokens, messages, tools, `max_tokens`, a JSON answer, code or JSON in the last message, the family, tags. So the classifier learns splits like "count / list / extract" vs "why / prove / is it legal" within one task.
- **Label:** for each judged sample and tier, whether that tier's answer held up against the baseline.
- **Model:** a logistic regression per tier below the baseline (sparse weights on hashed features), retrained daily and when enough new samples arrive, shipped to gateways with the project's configuration.
- **Calibration:** on held-out samples (cross-validation), each tier's threshold is the lowest at which the calls it would route meet the quality floor. A tier with fewer than 200 samples gets no threshold.

**Shadow report, activation and the canary.**
- **The report** (shadow or active): the share of calls per tier and per cascade step, each tier's quality against the baseline with a 95% interval (Wilson), the classifier's held-out accuracy, and projected monthly cost and savings at the project's prices.
- **Activating** needs the quality interval's lower bound at or above the floor for every tier the router would use, or an explicit override (as with routing rules). `router.ready` fires when a shadow router first qualifies.
- **The canary:** sampling goes on while active. A tier whose quality interval's upper bound falls below the floor (at least 50 samples in 7 days) is **demoted**: its calls go one tier up, `router.tier_demoted` fires and the project's channels are alerted. The customer restores it.

**Families stay** (`features/families`): a hash of the normalised system prompt (numbers, dates, UUIDs and whitespace normalised) + tool names + response format, or `X-Promptdam-Family`. They feed spend breakdowns, the cache doctor, the classifier (as a feature) and a per-family view of a router's report ("summarise ticket: fast holds up 98%, n = 240").

**Ledger:** per request, `counterfactual_cost = list price(baseline model, tokens, no cache)` and `actual = provider-reported usage × list price` (cache read and write pricing included). Savings are attributed to their reason (`routing` for routers and rules). Evaluation and decision costs are recorded as negative savings.

**Not chosen:**
- A hosted decision model on every request: 70–500 ms added, a per-request bill, every prompt sent to one more vendor.
- Routing by length or other surface features alone.
- Embedding the request for a nearest-neighbour router: an embedding call per request (latency, cost, one more vendor). Revisit if the classifier plateaus.

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
| `routes` | name, fallbacks, retries, cache_ttl_seconds, prompt_cache, router |
| `family_labels` | family, label (human) |
| `routing_rules` | route, condition (CEL), to_model, priority, status (draft/active/disabled), activated_at |
| `model_routers` | name, status (shadow/active/disabled), tiers jsonb (name, description, targets, examples), rules jsonb (condition → tier), unsure_tier, quality_floor, sample_rate, eval_budget_micros, judge, decision (model, timeout), demoted jsonb, activated_at |
| `router_classifiers` | router_id, version, trained_at, samples, metrics jsonb (per tier: threshold, held-out quality), weights (compact binary) |
| `policy_sets` | name, rules jsonb (CEL), version, default_effect |
| `budgets` | scope (org/project/key/developer/route), scope_id, window (day/month), limit_micros, action (alert/block), thresholds[] |
| `savings_reports` | period, verified_savings_micros, share_micros, status (draft/approved/disputed/invoiced), approved_by |
| `alert_rules` | kind (spend_spike, error_rate, provider_down, budget, firewall), params, channel_ids[] |

**ClickHouse:**
- `requests(org_id, project_id, key_id, route, family_id, provider, requested_model, served_model, api_format, stream, status, http_status, ttfb_ms, latency_ms, upstream_ms, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_micros, counterfactual_micros, savings_micros, savings_reason, response_cache_hit, router, tier, tier_source, tier_confidence, router_micros, firewall_action, findings_kinds Array(LowCardinality(String)), developer, tags Array(String), error_code, payload_ref, ts, retention_days)`
- `firewall_events(…, finding_kind, location, action, policy_rule, excerpt_masked, ts)`
- `router_evals(project_id, ts, router, trace, family_id, served_tier, tier_source, baseline_model, candidate_tier, candidate_model, verdict, check, judge_model, candidate_micros, baseline_micros, judge_micros, features Array(UInt32), retention_days)`

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
│   │   ├── translate/      OpenAI Chat ⇄ Anthropic Messages: requests, answers, streams, errors
│   │   ├── routers/        tiers, rules, the cascade, reports, activation, canary and demotion
│   │   ├── evaluate/       in-gateway sampling, replays, deterministic checks, the pairwise judge
│   │   ├── classifier/     features, training, calibration, inference
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
├── web/{app,site,api}/             Promptdam's OWN dashboard and landing site (built from @keel/app-kit and
│                                   @keel/site-kit), plus the generated API client
│   └── app/pages: index (spend + savings), requests/, requests/[id], families/, families/[id], routers/,
│                  routers/[id] (tiers, report), routes/, firewall/, budgets/, providers/, savings/ (ledger +
│                  reports), settings/* (keel)
├── bench/                         latency + throughput harness (vegeta/k6 scripts, fake upstream)
├── deploy/                        compose.yaml (api, gateway, worker + shared fragments), Caddyfile, env.*.sops;
│                                  later a second gateway VPS behind Cloudflare Load Balancing
└── docs/{adr,interviews}/
```

---

## 7. Timeline and phases (detailed todos)

Prerequisites: core milestones `core-v0.3.0` (policy, vault) and `pkg/llm` from v0.2. Keel K7 (streaming redact, hybrid limiter, budget counters) is built inside S1–S3 and tagged `core-v0.4.0`. Fixwire and Plugport post-launch work takes about 20% of time during this build.

### S0: Scaffold and spikes (Mon Mar 8 – Wed Mar 10)
- [x] S0.1 `keelctl new-product promptdam --prefix pd --domain <domain>` scaffolds `products/promptdam/`; CI path filter and the deploy workflow are green.
  - **Done 2026-10-07, by hand** (keelctl arrives with K4), on Fixwire's layout: `server/{brand,core,catalog,routers,features,cmd/{api,gateway,worker,migrate}}`, `deploy/Dockerfile`, Taskfile, `.env.example`; CI runs Go when `products/promptdam/{server,bench}` changes. The deploy workflow comes with the first VPS (S4.6).
  - Every outward-facing name (product key, key prefix `pd`, domain, `X-Promptdam-*` headers) lives in `server/brand`, because the product will be renamed (decided 2026-10-07).
  - Locally Promptdam runs on **its own stack** (`infra/compose/product.yaml` with `stack.env`, ports 2xxxx: `task promptdam:dev:stack`), never a database server shared with another product (decided 2026-10-07).
- [x] S0.2 Spike: streaming passthrough + usage tee for OpenAI Chat Completions, OpenAI Responses, Anthropic Messages and Gemini OpenAI-compat. Measure the added latency against direct calls.
  - **Done 2026-10-07:** not a throwaway spike but the gateway's core (`features/gateway`) on `pkg/llm/wire` (request inspection, byte-preserving field splice, SSE scanner, usage meter incl. DeepSeek cache hits, Gemini thinking in `total_tokens`, Anthropic 1-hour cache writes) and `safehttp.NewTransport` (SSRF-safe, no whole-request timeout). Chat streams that don't ask for usage get it asked for, and the extra chunk is hidden from the client.
- [x] S0.3 `bench/` harness with a fake upstream (configurable latency and streaming) for repeatable overhead measurement in CI.
  - **Done 2026-10-07:** `bench/fakeupstream` (all four formats) and `task promptdam:bench`. The timing gate runs on demand (`PROMPTDAM_BENCH=1`), not in CI: shared CI runners are too noisy for p99s. The functional tests run in CI.
- [ ] S0.4 Catalog (`plans.go`), Stripe sync (test), real landing page; recruit 10 design partners with ≥ $2k/mo LLM spend (waitlist, interviews, Fixwire customers with high cost-per-run).
  - **Done 2026-10-07, catalog:** `server/catalog/plans.go` per §5: Free, Pro, Team and Business with request overage. The savings plan is deferred (decided 2026-10-07). No `ai_credits`: the judge runs on the customer's keys (decided 2026-10-08). Seats are 3 on Free and 10 on Pro (my numbers, change freely).
  - **Done 2026-10-07, Stripe (test mode):** `api stripe sync` created the portal, the `promptdam_requests` meter, and Pro, Team and Business with monthly and yearly prices plus request overage. The webhook events are those of `docs/billing-policy.md`; others are acknowledged and ignored.
  - **Needs you:** the landing page and design partners.
- **Exit (met 2026-10-07):** proxy overhead p99 < 40 ms on the bench with streaming; OpenAI and Anthropic SDKs work unmodified with only `base_url` changed.
  - Bench on a laptop running client, fake provider and gateway at once: p99 overhead +2.4 ms (chat), +13.7 ms (chat stream), +19.1 ms (Anthropic stream). To re-measure on the VPS in S4.4.
  - `openai-go` (Chat stream and not, Responses stream and not, error codes) and `anthropic-sdk-go` (Messages stream and not, count_tokens) pass through the gateway in `features/gateway/sdk_test.go`.

### S1: Gateway core (Thu Mar 11 – Mon Mar 22)
- [x] S1.1 Provider registry + BYOK credentials in `pkg/vault`, provider key validation call, per-provider settings.
  - **Done 2026-10-07:** `providers` table and API. Kinds: OpenAI, Anthropic, Gemini, Mistral, DeepSeek, Groq, Together, xAI, Fireworks, OpenRouter, or a custom endpoint speaking either API.
  - Keys are sealed and never returned (hint only). They stay bound to their endpoint: a new base URL needs the key again. Custom URLs must be public https addresses.
  - The check lists the provider's models. The purge covers the table.
- [x] S1.2 Endpoints: chat/completions, responses (passthrough), messages, embeddings, models; request parsing for both formats; error normalisation.
  - **Done 2026-10-07:**
    - `count_tokens` too.
    - `GET /v1/models` answers in OpenAI's shape, or Anthropic's when the client sends `anthropic-version`.
    - The gateway's own errors take the client's API shape, with a stable code in `X-Promptdam-Error-Code`.
  - Still open: a `/v1/decisions` passthrough (OpenAI's Decisions API) once its usage field is known.
- [x] S1.3 Promptdam keys (key_settings: allowed models, tags, developer), attribution headers.
  - **Done 2026-10-07:**
    - Keys are the platform's API keys with the `gateway` scope (`pd_sk_…`).
    - `key_settings`: allowed models (`prefix*` wildcards), tags, developer, rpm and default route. A rotated key's replacement keeps them (`apikeys.OnRotated`).
    - `X-Promptdam-User` and `X-Promptdam-Tags` are recorded with each call.
- [x] S1.4 Config snapshots in memory (keys, routes, rules, policies, providers) with Valkey pub/sub refresh; no DB on the hot path.
  - **Done 2026-10-07 (`features/snapshot`, `features/resolver`):**
    - Lookup order: memory, then Valkey (keys still sealed), then Postgres once.
    - Changes publish the project ID; gateways drop their copy at once, and every copy expires after 30 s.
    - API keys are cached in process for 5 s in front of keel's Valkey cache (`pkg/memcache`, shared now).
    - Rules and policies join the snapshot with S2 and S3.
- [x] S1.5 Retries, fallback chains, circuit breakers; provider error taxonomy → fallback decisions.
  - **Done 2026-10-07:**
    - Routes have up to 5 fallbacks (`model` or `provider/model`) and 0–3 retries with full jitter.
    - Fallback triggers: 429, 529, 5xx, unreachable or a timeout, while nothing has reached the client.
    - Each project's provider and model has a breaker: 5 failures open it for 30 s, then one probe. The last target is always tried.
- [x] S1.6 Keel K7.2: hybrid local/Valkey limiter + budget counters (micro-USD), budgets CRUD and enforcement, alerts.
  - **Done 2026-10-07:**
    - `pkg/counter`: in-memory counts, flushed to Valkey every 250 ms; about 0.4 µs per check.
    - Budgets per project, key, developer or route, per day or month: alert, or block with 429 `budget_exceeded`.
    - A job checks every minute and alerts once per threshold (webhooks `budget.threshold_reached` and `budget.exceeded`).
    - Per-key rpm limits.
    - Budgets need the `budgets` plan feature (Pro and up).
- [ ] S1.7 Cost calculation via the `pkg/llm` catalog (incl. cache read/write pricing); ClickHouse `requests` writer; metering `requests`.
  - **Done 2026-10-07:**
    - Every call is costed (cache reads and writes; Anthropic's 1-hour writes at 2× input) and counted against budgets.
    - Every call is logged to ClickHouse `requests` (metadata only, plan retention). Rollups by provider/model/key/route (with latency percentiles), developer and tag.
    - The plan's `requests` meter is counted (hard on Free).
  - **Done 2026-10-07, prices (decided with the owner):** prices are reference data for spend and dollar budgets, never a bill; customers pay their providers.
    - The worker syncs OpenRouter's public model lists daily (text, embedding and decision models; `api prices sync` does it now). Each model is listed under OpenRouter's ID, and under its vendor's own name for OpenAI, Anthropic, Google, Mistral, DeepSeek and xAI.
    - The list includes cache prices, Anthropic's one-hour writes and long-prompt tiers.
    - Guards: a much shorter list is refused; a price moving more than tenfold, or to or from free, keeps its old value until checked.
    - Projects can set their own prices (`PUT …/prices`) for discounts and models the list lacks. The order is the project's price for that provider, then for any provider, then the list.
  - **Known gaps:**
    - Groq, Together and Fireworks name and price open models their own way: tokens only, unless a project sets prices.
    - DeepSeek shows OpenRouter's resold price.
    - Embedding and decision models are priced by input alone, as listed.
- [ ] S1.8 Dashboard: providers, keys, requests log (metadata), spend analytics (by model/provider/key/developer/tag), budgets.
  - **Done 2026-10-07, the backend:**
    - APIs for providers, key settings, routes and budgets.
    - Request log: list, filters, cursor, by trace.
    - Spend by model/provider/key/route/developer/tag with series, and latency percentiles, read from rollups only.
    - Scan-budget tests: a page reads 2 granules, a lookup 1, spend about 40 rollup rows.
  - The dashboard itself is the owner's (frontend phase).
- [ ] S1.9 Quickstarts: OpenAI SDK (Py/TS), Anthropic SDK (Py/TS), Vercel AI SDK, LangChain, Claude Code (`ANTHROPIC_BASE_URL`), curl.
  - **Done 2026-10-07:**
    - `examples/`: curl and the official Python SDKs, run end to end on the local stack.
    - Claude Code's settings are in its README.
    - The Go SDKs are covered by `sdk_test.go`.
    - `task promptdam:dev:fake` and `task promptdam:dev:seed` support local runs.
  - Still open: TypeScript, the Vercel AI SDK and LangChain examples, and the docs pages.
- **Exit:** design partners route production traffic through Promptdam with fallbacks and budgets; spend analytics match provider invoices within ±2%.
  - **Status (2026-10-07):** the backend is in place and tested end to end against a fake provider.
  - **Needs you:** design partners, a VPS, and real price data (S1.7) before the exit can be measured.

### S2: Cost levers (Tue Mar 23 – Wed Mar 31)
- [ ] S2.1 Families: normalisation + hashing, stats rollups, families UI (top families by spend).
  - **Done 2026-10-07, the backend:**
    - A family is the hash of a call's normalised system prompt, tools and answer format. Dates, times, UUIDs, hex IDs and numbers become placeholders first, so prompts that differ only in them share a family.
    - Every call logs its family, the hash of its exact prefix, the changing values found in it, and the prefix length.
    - The `families_hourly` and `prefixes_hourly` rollups feed the families APIs: top families by spend, one family, and labels.
  - The families UI is the owner's (frontend phase).
- [x] S2.2 Exact response cache (opt-in per route; Valkey with size limits; TTL; project-scoped; `x-promptdam-cache` header).
  - **Done 2026-10-07:**
    - Opt-in per route with `cache_ttl_seconds` (up to a week). It covers non-streamed calls only.
    - Two requests are the same when their bodies are equal after sorting keys and setting aside fields that don't change the answer (`user`, `metadata`, `store`, `stream_options`, `service_tier`).
    - Keys are scoped to the project and route. Answers over 1 MiB aren't cached. A hit returns `x-promptdam-cache: hit` and counts as a response-cache saving.
- [x] S2.3 Prompt-caching doctor: prefix divergence analysis, cause classification, dollar impact, fix snippets.
  - **Done 2026-10-07:**
    - The doctor reads only the prefix rollup, never prompts.
    - Findings, each with a fix: the prefix is too short to cache, a date or time in the instructions, IDs, changing numbers, tools in changing order, instructions that vary, and Anthropic calls without breakpoints.
    - Dollar impact: what the cache could save a month if every call after the first read the prefix from it. Results are ranked by that amount.
    - Covers one family, or the 20 with the most input tokens.
- [x] S2.4 Anthropic auto prompt caching (top-level automatic + stable-prefix breakpoint; respect the 4-breakpoint limit and minimum sizes); verify through `cache_read_input_tokens`.
  - **Done 2026-10-07:**
    - Opt-in per route with `prompt_cache`. It applies to Anthropic requests that carry no `cache_control` of their own and whose prefix is long enough to cache (about 1,024 tokens, or 2,048 for Haiku).
    - The gateway adds breakpoints on the last tool and the end of the system prompt: two, within Anthropic's limit of four. Content is never moved.
    - The response says `x-promptdam-cache: prompt`. Cache reads and writes come from the provider's usage, and the savings ledger counts them.
- [ ] S2.5 Routes + manual routing rules (CEL conditions, same-format targets only), rules UI with dry-run on recent traffic ("would have saved $X").
  - A rule is a customer's guess (e.g. `request.input_tokens < 1500`), so it is shadow-tested before it routes anything. The dry-run shows both the savings and the quality on the requests the rule would match (judged as in §6.2's evaluation). Activating a rule whose matched traffic fails the route's quality threshold needs an explicit override (decided 2026-10-07).
  - **Done 2026-10-07, the backend (cost half):**
    - Rules are CEL conditions over the call as the gateway sees it, via `pkg/policy`: `model`, `format`, `input_tokens` (estimated from bytes), `messages`, `max_tokens`, `has_tools`, `json`, `stream`, `family`, `tags`, `user`, `developer` and `route`.
    - The request is a typed object, so a misspelt field or a wrong type is refused on save, with its line and column. A rule can be limited to one route.
    - Pro feature, at most 50 per project. Statuses: draft, active, disabled. Priority sets the order.
    - Dry-run, saved or not, over the most recent 20,000 calls of a range of up to 31 days: calls matched, their cost, and the cost on the rule's model with the same tokens. Project prices come first. Calls the gateway can't price are counted apart.
    - The first active rule that holds puts its model first, and the model asked for stays the first fallback. The rule is logged with the call, and its savings count as routing.
  - **Still open:**
    - The quality half of the dry-run (win/tie/loss) and the activation gate, which needs S3's evaluation. Until then, activating a rule is the customer's call, on cost evidence only. A rule whose quality matters can be a router rule instead: routers in shadow mode measure it (S3.4).
    - The rules UI is the owner's.
- [ ] S2.6 Savings ledger (counterfactual pricing, reasons) and savings dashboard.
  - **Done 2026-10-07, the backend:**
    - Every call logs a counterfactual: what the model asked for would have cost at its price with no cache. Savings are the counterfactual minus the actual cost.
    - Each saving carries a reason: response cache, prompt cache, routing or fallback.
    - The `savings_hourly` rollup feeds `Get Savings`: the total, a breakdown by reason, the 20 families that saved most, and a daily series.
  - The savings dashboard is the owner's.
- **Exit:** at least 3 design partners show ≥ 15% verified savings from caching and manual rules alone.

### S3: Model routers and the firewall (Thu Apr 1 – Mon Apr 12; routers built ahead, from 2026-10-08)
Model routers replace per-family savings experiments, and cross-format translation and the learned router moved here from S6 (decided 2026-10-08, §6.2, ADR 0001). The steps, each committed on its own:
- [x] S3.1 Cross-format translation: OpenAI Chat Completions ⇄ Anthropic Messages requests, answers, streams (tools, images, JSON answers, usage, stop reasons) and errors; golden tests per direction, and the official SDKs through the gateway. Fallbacks and rules may then name a model of the other format.
  - **Done 2026-10-08 (`features/translate`):**
    - Providers of the other API serve Chat Completions and Messages calls after those of the call's own API, by priority. A custom endpoint of the other API serves only the models it lists. `/v1/models` lists both.
    - Requests: system and developer messages, text, images (data URLs and links), PDFs, tool calls and results (images in tool results follow in the user's message), tools and tool choice, parallel tool calls, stop sequences, JSON schemas (Anthropic's `output_config.format`), JSON mode (an instruction), the end user, reasoning effort and thinking when the target model takes one. Chat's temperature is capped at 1 for Anthropic. Answers default to 8,192 tokens when a Chat call sets no limit.
    - Content is never dropped. A call the target can't express (audio, files by ID, several answers, Anthropic's server tools, an assistant prefill) passes that target over for the next, or fails with 400 `untranslatable_request`. Tuning parameters without an equivalent (seed, logit_bias, top_k…) are left out and listed in `X-Promptdam-Dropped`.
    - Answers, errors and streams are translated back, with usage (cache reads included) and stop reasons. Streamed tool calls from OpenAI-format providers reach Anthropic clients whole, once complete, since providers may interleave them.
    - The request log says `translated`. About 1.5 µs per streamed event and 80 µs per 5 KB request on a laptop.
- [x] S3.2 Model capabilities in the price list (tools, images, structured output, reasoning, context window), from OpenRouter's models API (core `pkg/llm`).
  - **Done 2026-10-08:** the core's catalog models carry `features` (tools, images, files, json_schema, reasoning; unknown when the list says nothing) and `max_output`, read from OpenRouter's supported parameters, input modalities and top provider. The daily price list keeps them. Translated calls pass a reasoning effort on only to models that take one, and Messages answers of Chat calls without a limit may run to the model's own (at most 32,768 tokens). Router tiers filter on them next (S3.3).
- [x] S3.3 Routers: tiers, rules, explicit tiers (`model: "auto:tier"`, `X-Promptdam-Tier`), requirement filters, the unsure tier, shadow and active modes; routes may send their calls through a router. API, snapshot, gateway, request log (`router`, `tier`, `tier_source`, `tier_confidence`) and ledger (baseline counterfactual).
  - **Done 2026-10-08 (`features/routers`, `features/routers/tiers`, `resolver/place.go`):**
    - A router has 1 to 8 tiers (name, description, model and up to 5 fallbacks of either API format, up to 20 examples), up to 20 rules (CEL over `request`, each to a tier) and a default tier, the strongest unless set. The default tier is both "when unsure" and the baseline. At most 20 routers a project; a router can't take a model's name.
    - The cascade so far: the client's tier, then the first rule that holds, then the default tier. The classifier and the decision model join in S3.5 and S3.6. A tier whose model lacks what the call needs (tools, images, files, a JSON schema, context; from the price list, unknown counting as present) passes the call to the next stronger tier that has it, a weaker one only when none does.
    - Targets: the tier's model and fallbacks, then the stronger tiers'. Never a weaker tier.
    - Shadow mode (the default for new routers) decides and logs, but serves the default tier; a tier the client names is served anyway.
    - Routes may send every Chat Completions and Messages call through a router (`router` on the route), whatever model the call names. Calls naming a router count their savings against its default tier; calls naming a model, against that model.
    - Responses say `X-Promptdam-Router`, `X-Promptdam-Tier` and `X-Promptdam-Tier-Source` (`shadow:` first when the router only watched). The request log keeps router, tier, decided tier, source, confidence and shadow.
    - API: routers CRUD (409 `router_in_use` while a route uses one), and `POST …/routers/{router}/explain`, which places a request as the gateway would, without calling a provider: the tier and why, what the request needs, which tiers' models can take it, and the targets in order.
    - Plans (my proposal, §5): every plan may run routers in shadow mode; active routers need `routing.routers` (Pro and up); learned routing will need `routing.learned` (Team and up), which replaces the `experiments` feature.
    - Placing a call takes about 10 µs (6 KB request, rules, needs, targets).
- [x] S3.4 Evaluation in the gateway: sampling into a bounded in-memory queue, replays on the cheaper tiers and the baseline on the project's keys, deterministic checks, the pairwise judge (random order, 10% in both orders), the evaluation budget, `router_evals`.
  - **Done 2026-10-08 (`features/evaluate`, `features/classifier/features.go`):**
    - The resolver samples a router's calls (10% in shadow mode, 2% active, or the router's rate) while it has an evaluation budget. A sampled call keeps its request and the answer the client got (up to 1 MiB, streamed or not) in memory; the gateway hands it to the evaluator, which never blocks the call: a queue of 64 per gateway, 4 workers, samples dropped when full.
    - Every tier but the default one answers the call again, not streamed, on its model then its fallbacks, translated when needed, all at once. The default tier answers too, unless its answer is the one the client got. Stronger tiers than the default are evaluated as well: a win there means the call needed them.
    - Checks first: the same words or tool calls (tie), a refusal, an empty answer, prose where JSON was asked, other tools or none (loss). Otherwise the judge: the router's judge model, else `claude-sonnet-5-5` when a provider of the project serves it, with the answers in random order, held to a JSON schema where the model takes one, and 10% judged again the other way round, a disagreement making a tie. Without a judge, those calls are recorded with no verdict.
    - `router_evals` keeps the verdict, how it was reached, the judge, the costs and the call's features: hashed words and word pairs of the last user message and the instructions, the family, tags and shape. Never text. It is purged with the project and kept as long as the request log.
    - Replays and judging cost what the provider bills at the project's prices: counted against the router's monthly evaluation budget (default $100), whose spend stops further sampling, and as the project's spend.
    - Evaluation runs on every plan (it spends the customer's own keys, within their budget); only the learned placement (S3.5, S3.6) needs `routing.learned`.
    - Features take about 28 µs per call.
- [x] S3.5 The classifier: features shared by gateway and worker, training and calibration in the worker, shipping to gateways; the router report (tier shares, quality with intervals, held-out accuracy, projected savings, per family).
  - **Done 2026-10-08 (`features/classifier`, `features/training`, `routers/report.go`):**
    - One sparse logistic regression per tier but the default, over the hashed features (AdaGrad, L2, 5-fold cross-validation). The stored model is the folds' average, so the thresholds found on held-out calls apply to its scores.
    - Labels: a cheaper tier is right for a call when its answer tied or won against the default tier's; a stronger tier when it won (the call needed it).
    - Thresholds: for a cheaper tier, the lowest at which the Wilson lower bound of the right calls' share over it meets the router's quality floor; for a stronger tier, the lowest at which the calls around it needed the tier at least half the time. At least 200 judged calls and 20 over the threshold, else no threshold and the classifier sends nothing there. Training 2,000 calls takes about 12 ms.
    - The cascade now reads: the client's tier, rules, then the classifier (the strongest tier above the default the call needs, else the cheapest below it that holds up), else the default. A classifier is unsure when a cheaper tier may hold up but isn't clear of its threshold, or when nothing is trained yet: the decision model's cue (S3.6).
    - The worker retrains hourly every router with calls judged since its last training, on the newest 20,000 per tier of the last 30 days judged against its current default tier; `POST …/routers/{router}/train` queues one at once. Classifiers (`router_classifiers`, purged with the project) reach gateways with the project's configuration.
    - Active routers use the classifier on plans with `routing.learned` (Team); shadow routers on every plan, since there it only says what it would do.
    - `GET …/routers/{router}/report`: calls per tier served and chosen and by step (from the new `routers_hourly` rollup), their cost and what they'd cost on the chosen tiers (what following the router saves, also per 30 days), evaluation spend, each tier's verdicts and quality with its 95% interval and whether it meets the floor, the classifier's held-out precision, coverage and AUC per tier, the most judged families, and `ready`.
- [x] S3.6 The decision model for unsure calls (opt-in): structured answer, hard timeout, cache, cost on the call.
  - **Done 2026-10-08 (`features/decide`):**
    - An active router with a decision model asks it about the calls its classifier is unsure of (a cheaper tier may hold up but isn't clear of its threshold, or nothing is trained yet), on plans with learned routing. Shadow routers and explanations never ask: a call would wait on an answer that changes nothing.
    - The model reads the tiers, cheapest first, with their descriptions and up to 3 examples each, the call's instructions (1,000 characters) and last user message (4,000), and answers `{"tier": …}`, held to a schema with the tiers' names where the model takes one. Any chat model of either API format on the project's own providers.
    - The router's timeout (default 150 ms) bounds the wait; no answer in time, or one naming no tier, leaves the default tier. Answers are cached per router, family and message for 10 minutes in each gateway.
    - What it cost counts as the call's spend (and against its budgets), is logged as `router_micros` and comes off the call's savings.
    - Explain says `unsure` when a decision model would be asked.
    - Not yet: OpenRouter's typed decision models (`output_modalities=decisions`), whose request shape differs; any chat model works meanwhile.
- [ ] S3.7 Activation gate, `router.ready`, the canary and auto-demotion (`router.tier_demoted`, alerts).
- [ ] S3.8 Keel K7.1: streaming-safe redact scanner with chunk overlap; reversible tokenisation restored in streamed responses.
- [ ] S3.9 Firewall presets (source code, customer data), CEL policy sets, actions (block/redact/tokenize/alert/route_to), fail-closed for secrets and open for the rest by default (decided 2026-10-08).
- [ ] S3.10 Firewall events (masked excerpts, stats), alerts, weekly digest; the firewall UI is the owner's.
- [ ] S3.11 Tag core milestone **`core-v0.4.0`**.
- **Exit:** a router in shadow mode on a design partner's traffic reaches `router.ready`, then serves it active with measured savings; a forced quality drop demotes a tier within one window; secrets in a test prompt are blocked before leaving.

### S4: Billing, launch readiness and lead magnet (Tue Apr 13 – Fri Apr 23)
- [ ] S4.1 Billing: plans and the `requests` overage meter. The savings-plan base, `savings_share` meter and monthly report approval are deferred (2026-10-07).
- [ ] S4.2 "LLM Bill Audit" free tool on the site. The user uploads OpenAI/Anthropic usage exports (CSV), or a 7-day sample through Promptdam's free plan. Output:
  - an estimated savings breakdown (model downgrade potential by family size, caching potential, batch potential)
  - a PDF/email report and a CTA.
- [ ] S4.3 Docs: concepts (families, rules, routers, ledger, firewall), provider setup, migration guides (from OpenRouter, LiteLLM, Portkey, Helicone), security and data handling page, API reference, `llms.txt`.
- [ ] S4.4 Load test at 1k req/s with streaming on the Promptdam VPS (2 gateway replicas behind Caddy); uptime checks; status page; documented client-side fallback to the provider for when the gateway is unreachable.
- [ ] S4.5 Security pass: vault for provider keys, payload-free default, tenant isolation, SSRF guard on custom provider URLs, dependency audit, signed container images.
- [ ] S4.6 Prod deploy; go-live checklist (§16 of the root doc).
- **Exit:** ≥ 5 design partners in production, ≥ 2 willing to pay for Team or Business. → **Beta Mon Apr 26**.

### S5: Beta → launch (Mon Apr 26 – Fri May 14)
- [ ] S5.1 Weekly partner calls; fix the top issues; tune detector precision and judge thresholds.
- [ ] S5.2 Case studies with real numbers ("$X/mo → $Y/mo, quality unchanged, evidence attached").
- [ ] S5.3 Launch assets: demo video ("one line change, 41% cheaper"), Show HN, Product Hunt, comparison pages (vs OpenRouter, LiteLLM, Portkey, Cloudflare AI Gateway, Vercel AI Gateway, Helicone), "Secrets your coding agent sent to the LLM" blog post (with anonymised aggregate data, if partners opt in).
- [ ] S5.4 Stripe live mode; savings-plan contract template (click-through terms with dispute window).

### Launch: Tue May 18, 2027
- [ ] Show HN + Product Hunt + X/LinkedIn; r/LocalLLaMA, r/LLMDevs, r/ExperiencedDevs (cost angle); engineering-manager newsletters.
- [ ] Outbound to Fixwire customers with high cost-per-run ("Fixwire shows the cost; Promptdam cuts it").

### S6: Post-launch (May – Jul 2027; shares time with Fixwire for Games, May 19 – Jun 25)
- [ ] S6.1 Batch deferral (Anthropic Message Batches, OpenAI Batch).
- [ ] S6.2 Semantic cache with eval guard.
- S6.3 and S6.4 (cross-format translation, the learned per-request router) moved to S3 on 2026-10-08.
- [ ] S6.4b Router extras (§4.2): the Responses API through routers, learning from decision-model choices, bandit exploration, a Promptdam-paid judge.
- [ ] S6.5 Fixwire span export; coding-agent pack (per-developer SSO keys); employee-chat guides (Open WebUI, LibreChat).
- [ ] S6.6 SSO/SCIM; Azure OpenAI, Bedrock and Vertex providers.

### S7: Enterprise firewall (Q3 2027)
- [ ] S7.1 EU data-plane region + residency routing.
- [ ] S7.2 Self-hosted data plane (config sync, local logs).
- [ ] S7.3 SIEM export; security page, DPA and an independent pen test. No certifications: BYOC (S7.2) is the answer for buyers who need data to stay in their cloud.

---

## 8. Go-to-market
- **Channels (ranked):**
  1. The "LLM Bill Audit" lead magnet (high intent, shareable numbers).
  2. Cross-sell from Fixwire (cost data already there).
  3. Migration guides for orphaned or acquired-gateway users (Helicone, Portkey-to-enterprise).
  4. A content series: "where your LLM money goes" and caching deep dives.
  5. HN/PH launch.
  6. A coding-agent security angle aimed at platform/security leads.
- **Sales motion:** self-serve on every plan; Business may come with a call. No custom contracts below $2k/mo.
- **Activation:** ≥ 1,000 requests through Promptdam within 7 days **and** the savings or firewall dashboard viewed.
- **Expansion:** request volume, developers (keys), savings share growing with the customer's spend.

## 9. Metrics
| Metric | Beta target | Launch + 90 days |
|---|---|---|
| Orgs routing traffic | 8 | 150 |
| Monthly requests through Promptdam | 5M | 150M |
| Median verified savings (orgs with an active router) | 25% | 30% |
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
| Routing hurts quality | Routers start in shadow mode; activation needs measured quality per tier (or an explicit override); unsure calls go to the strong tier; thresholds calibrated on held-out samples; the canary demotes a drifting tier on its own; critical routes can stay out |
| A wrong judge teaches the router wrong boundaries | Deterministic checks first; random answer order with 10% judged both ways; the customer picks the judge model; the report shows example verdicts; quality intervals, not point estimates, gate activation |
| Evaluation spends the customer's money | Visible monthly budget per router (default $100), sample rates the customer sets, costs shown and subtracted from savings |
| Hot-path reliability and latency (one VPS) | Go, in-memory snapshots, no DB on the hot path, two gateway replicas, fallbacks, bench in CI, documented fail-open client pattern; a second VPS behind Cloudflare Load Balancing before any SLA |
| Gateway is hosted in Europe only (Contabo): US apps add a transatlantic round trip (~80–100 ms) per LLM call | Small relative to LLM latency (seconds) but visible in time-to-first-token. Measure in beta. Add a US gateway VPS only when US revenue justifies it (owner keeps all VPS in Europe for now). Market EU residency to EU customers |
| Trust: a proxy sees all prompts | Payload-free logging by default, vault-encrypted keys, minimal dependencies, signed images, DPA, EU data residency and BYOC data plane (S7), no certification claims; contrast with self-run proxies after the Mar 2026 LiteLLM supply-chain incident |
| Savings-share disputes | Transparent per-request ledger, monthly approval with a dispute window, conservative counterfactual (list prices, provider-reported usage) |
| API format translation complexity (now in the MVP) | Chat Completions ⇄ Messages only (the Responses API stays passthrough); golden tests per direction for requests, answers and streams; the official SDKs run through the gateway in tests; anything the target can't express is refused with a clear error, never dropped silently |

## 11. Decisions needed
- [x] Name and domain: **Promptdam**, promptdam.dev + promptdam.com (decided 2026-10-03; register the domains).
- [x] Savings-share pricing: **deferred** (decided 2026-10-07). Bill a subscription plus requests only. The savings ledger proves value but doesn't set invoices. Revisit if large customers ask.
- [x] Model routing: **routers with customer-described tiers are a core MVP feature** (decided 2026-10-08): the cascade, evaluation and learning of §6.2, with cross-format translation, replacing per-family experiments. ADR 0001.
- [x] Judge: **the customer's own provider keys only** (decided 2026-10-08). The project picks the judge model, `claude-sonnet-5-5` by default when it has an Anthropic provider. Later, maybe: Promptdam's own keys, charging the customer what the provider charges us.
- [x] Firewall default for `block` policies when the scanner itself fails: **closed for secrets, open for everything else**, configurable per project (decided 2026-10-08).
- [ ] `DECISION NEEDED:` which plans get routers (my proposal in §5: one shadow router on Free, active routers with explicit tiers and rules on Pro, learned routing on Team).
- [x] Include OpenAI Responses API routing at MVP: **passthrough only** (auth, budgets, usage, cost, log; no model routing or caching), routing later (decided 2026-10-07).

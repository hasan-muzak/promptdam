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
  - retries with jitter (a provider's `Retry-After` honoured up to 10 s); fallback chains (model → model, provider → provider) on 429, 5xx, timeouts and "prompt too long"
  - a call a provider received whole is never sent to it twice (it may be answering, and billing, still): only on to the next target
  - an answer that breaks off after it began cuts the client's connection rather than ending as if complete
  - circuit breaker per provider and model (error events inside a 200 stream count)
  - per-key rate limits.
- **Cost levers:**
  - **Exact response cache:** opt-in per route; key = normalised request hash, project-scoped, TTL.
  - **Auto prompt caching:** for Anthropic calls without `cache_control`, breakpoints at the end of the tools and the system prompt (the target model's minimum prefix from the price list), and Anthropic's automatic breakpoint on a conversation's tail; each family's lifetime (five minutes or an hour) learned from its calls' gaps. For OpenAI, a conversation's `prompt_cache_key`.
  - **Cache doctor:** detects unstable prefixes across requests in a family and explains the fix.
  - **Routes and manual routing rules:** CEL conditions, e.g. `request.input_tokens < 1500 && !request.has_tools` routes the request to a cheaper model.
  - **Model routers (§6.2):** tiers the project describes (a model and a reasoning effort), called like a model (`"model": "auto"`) or attached to a route. Each conversation turn gets a tier from: the client's explicit choice, the models' capabilities, rules, a classifier learned from judged samples of the project's traffic, an opt-in decision model when the classifier is unsure, else the "unsure" tier; the calls continuing a turn (an agent's tool loop) keep it, and a warm conversation stays on its tier unless switching pays for rebuilding its cache. Shadow mode first (decide and evaluate, serve the strong tier), then active, with a canary that demotes a tier whose quality drifts.
- **Savings ledger:** per request `counterfactual_cost`, `actual_cost`, `savings`, `reason` and how each saving is known (`provider`, `cache`, `replays`, `estimate`); calls a model without a price served save nothing. A monthly verified-savings report, exportable as CSV.
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
- **Alert rules:** spend spikes above the usual hour, provider errors over a window, a provider that looks down, slow answers; evaluated every minute, to the org's channels, webhooks and the project's activity feed.
- **Platform (Keel):** orgs, members, keys, billing, usage, audit (every project change; Team), webhooks (`budget.threshold_reached`, `budget.exceeded`, `firewall.blocked`, `router.ready`, `router.tier_demoted`, `alert.fired`, `alert.resolved`).

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
| Free | $0 | 50k (hard) | 7 days | Gateway, fallbacks, exact cache, **secrets blocking**, savings report (read-only), model routers in shadow mode (the report shows what routing would save) |
| Pro | $49 | 500k | 30 days | Auto prompt caching + cache doctor, routing rules, active routers with explicit tiers and rules, budgets and alert rules, PII redaction, ledger export |
| Team | $199 | 3M | 90 days | Learned routing (evaluation, classifier, decision model) + auto-demotion, CEL firewall policies, per-developer keys, audit log |
| Business | $499+ | 10M | 1 year | Custom policies, payload-free mode guarantees, DPA, SLA, SSO, priority support |

- **Overage** (soft on paid plans): $5 per extra 100k requests on Pro, $3 on Team, $2 on Business.
- **Savings plan: deferred (decided 2026-10-07).** Promptdam bills a subscription plus gateway requests only, never tokens and never a share of spend or savings. The savings ledger stays as proof of value ("Promptdam saved you $X this month"), not as a basis for invoices. The design below is kept for a later decision, if large customers ask:
  - a $500/mo base price that covers 15% of the first $3,333 saved
  - a metered `savings_share` (USD cents) Stripe meter, reported monthly from the verified ledger as `max(0, 0.15 × savings − 500)`
  - the customer approves the monthly savings report before invoicing, with a dispute window of 7 days.
- **Routers per plan (decided 2026-10-08):**
  - Every plan runs routers in shadow mode, evaluation included: the report shows the savings on the customer's own traffic before any upgrade, at no cost to us (the evaluation runs on their keys, within their budget).
  - Pro (`routing.routers`) makes routers active: they serve traffic with the client's tiers, rules and the default tier, and the canary watches them.
  - Team (`routing.learned`) adds the learned placement: the classifier and the decision model.
  - A downgrade below Pro turns active routers into watchers (shadow behaviour) until the plan has routers again; nothing is deleted.
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
- **Headers returned:** `x-promptdam-trace`, `x-promptdam-served-model`, `x-promptdam-cache` (hit/miss/prompt), `x-promptdam-effort` (a router tier's). Savings are known once the provider reports usage, at the end of a stream, after the headers: each call's are in the request log (by trace), with how they are known (`savings_basis`).
- **Answers name the model asked for:** an answer the gateway moved to another model (a router, a rule, a fallback) names the model the client asked for, whole or streamed (agents keep state, thinking included, by the model they called); `x-promptdam-served-model` and the log name the served one, and `x-promptdam-answer-model: served` keeps it in the answer.
- **Conversations (ADR 0002):** a call's turn (its human message and the calls continuing it) is read off the request (core `wire.Turn`), keyed alike on every replica, and kept per replica and in Valkey (`features/conversation`): a turn's placement, its calls' tokens and cache state, and how long after the conversation's previous call each started. Clients may name the conversation (`x-promptdam-session`, or Claude Code's session header).
- **Thinking heal:** a Messages call whose history the firewall rewrote can carry a thinking block bound to the history before it ("bound to a different conversation"); it is sent once more with the provider asked to drop the block, else without the history's thinking (`healed` in the log).

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
- **Tiers,** ordered from cheapest to strongest, any number up to 8. Each has a name, a description in plain words of the work it is for, a model with up to 5 fallbacks (any provider: the gateway translates between API formats), up to 20 example requests, and optionally a reasoning effort (none to max), fitted to each model from the price list and written in its provider's own words. The same model at a lower effort is often the cheapest tier that holds up.
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
   - A hard timeout falls through to step 6. By default it is measured: each gateway waits a quarter more than the 99th percentile of the decision model's latest 128 answers (a question that timed out counting as its whole wait, so a wait too short grows), within 200 ms and 3 s, and 2 s until 20 answers are measured. A router may set its own (20 ms to 3 s). Answers are cached per router, family and last message for 10 minutes.
   - How each question went is logged with the call (`decision`: answered, cached, timeout, error, unreadable; `decision_ms`), and the router report sums it per outcome with the wait's percentiles: a decision model that often times out is visible, not silent.
   - It runs on the project's own key, and its cost counts against the call. Any model works: OpenRouter's decision models (on 2026-10-07 the cheapest were Respan span-01 at $0.02 per 1M input tokens; Jev, Liquid d1 and Perplexity's decider about $0.04; Inception's mercury-decide free) or a small chat model.
   - Target: under 10% of a router's calls get here once the classifier is trained. Measure it in beta.
6. **The unsure tier.**
- Then: a tier's own fallbacks, then the next tier up, on rate limits, overload, server errors, timeouts and "prompt too long".
- **Turns (ADR 0002):** the cascade places a conversation's turn, its human message: every input above is the turn's (the ask and the conversation up to it), never a tool result. The calls that continue a turn (an agent's tool loop) keep its placement on any replica; requirements and rules may move them up, never down, and the decision model is never asked mid-turn. Within the tier, the target that served the turn's last call goes first (its cache is warm there).
- **Switching must pay:** a new turn the cascade puts on a cheaper tier than the conversation's warm one stays there (`tier_source = held`) when the saving over the turns ahead (at most four) is smaller than rebuilding the cache on the new model, priced from the previous turn's calls. Clients' tiers and rules are never held, nor moves up.

**Evaluation (in the gateway, off the call's path; `features/evaluate`).**
- A sample of a router's calls that start a turn goes to an in-memory queue (bounded; samples are dropped when it is full). Requests are never stored: they live in the gateway's memory until judged. Replays run at each tier's effort.
- Each sample is replayed, not streamed, on every tier cheaper than the baseline, and on the baseline when another tier served the call (a served baseline answer is reused).
- **Judging:** deterministic checks first (valid JSON, the schema, tool names and arguments, exact match, refusals). Otherwise a pairwise judge with a rubric (correctness, completeness, following the instructions and format) and the answers in random order; 10% are judged in both orders, and a disagreement counts as a tie. A judge that disagrees with itself on more than 10% of those (at least 30 in 7 days) has every verdict judged both ways until that falls under 5%. The report flags a judge from the same vendor as one side of a cross-vendor comparison.
- Every verdict is a row of `router_evals`: the tiers, the verdict, the costs, and the request's features as hashes (never its text).
- Replays, judge and decision-model calls run on the project's own provider keys (decided 2026-10-08), within the router's monthly budget, and count as the project's spend. Their cost is subtracted from the router's savings.

**Learning (in the worker; `features/classifier`).**
- **Features** cost nothing to compute: hashed words and word pairs of the turn's human message and of the instructions, the turn's estimated input tokens and messages, tools, `max_tokens`, a JSON answer, code or JSON in the ask, the family, tags. So the classifier learns splits like "count / list / extract" vs "why / prove / is it legal" within one task.
- **Label:** for each judged sample and tier, whether that tier's answer held up against the baseline.
- **Model:** a logistic regression per tier below the baseline (sparse weights on hashed features), retrained daily and when enough new samples arrive, shipped to gateways with the project's configuration.
- **Calibration:** on held-out samples (cross-validation), each tier's threshold is the lowest at which the calls it would route meet the quality floor. A tier with fewer than 200 samples gets no threshold.

**Shadow report, activation and the canary.**
- **The report** (shadow or active): the share of calls per tier and per cascade step, each tier's quality against the baseline with a 95% interval (Wilson), the classifier's held-out accuracy, and projected monthly cost and savings at the project's prices.
- **Activating** needs the quality interval's lower bound at or above the floor for every tier the router would use, or an explicit override (as with routing rules). `router.ready` fires when a shadow router first qualifies.
- **The canary:** sampling goes on while active. A tier whose quality interval's upper bound falls below the floor (at least 50 samples in 7 days) is **demoted**: its calls go one tier up, `router.tier_demoted` fires and the project's channels are alerted. The customer restores it.

**Families stay** (`features/families`): a hash of the normalised system prompt (numbers, dates, UUIDs and whitespace normalised) + tool names + response format, or `X-Promptdam-Family`. They feed spend breakdowns, the cache doctor, the classifier (as a feature) and a per-family view of a router's report ("summarise ticket: fast holds up 98%, n = 240").

**Ledger:** per request, `counterfactual_cost = list price(baseline model, tokens, no cache)` and `actual = provider-reported usage × list price` (cache read and write pricing included). Savings are attributed to their reason (`routing` for routers and rules). A router's routed call is priced with the output the default tier writes against the served tier, measured on judged replays (at least 50; `savings_basis = replays`, else `estimate`), so an effort tier shows its saving. Evaluation and decision costs are recorded as negative savings.

**Not chosen:**
- A hosted decision model on every request: 70–500 ms added, a per-request bill, every prompt sent to one more vendor.
- Routing by length or other surface features alone.
- Embedding the request for a nearest-neighbour router: an embedding call per request (latency, cost, one more vendor). Revisit if the classifier plateaus.

### 6.3 Prompt-caching doctor (`features/cachedoctor`)
- For Anthropic-format families:
  - Compare consecutive requests' prefixes (tools → system → messages render order). Find the first divergent byte offset and classify the cause: timestamp, request id, unsorted JSON, tool-list churn, user content too early.
  - Report the expected hit rate vs actual (`cache_read_input_tokens`) and the dollar impact, with concrete fix snippets.
- **Auto-fix (opt-in per route, `prompt_cache`):**
  - When the client set no breakpoint, add one at the end of the tools and of the system prompt when that prefix reaches the target model's minimum cacheable size (512 to 4,096 tokens, from the price list), and Anthropic's automatic breakpoint on the tail of a conversation that goes on (or an agent's tool loop), on Anthropic's own API.
  - Bytes are only added, never moved or re-encoded: reordering changes semantics and what the cache keys on.
  - OpenAI calls of a conversation get its `prompt_cache_key`.
- **Lifetimes, learned (`features/cachettl`, `features/cachepolicy`):** every six hours the worker weighs each family's call gaps over three days, by cached tokens: returns 5 to 60 minutes after the conversation's (or prefix's) previous call, against writes within five minutes and after an hour. An hour (written at twice the input price instead of 1.25 times) is kept where it pays 15% more than it costs, backed by at least 20 returns, until it pays 15% less. The doctor shows each family's lifetimes and what they are worth.
- **Calls written together:** the doctor counts calls that wrote a prefix within 10 seconds of another and read none of it (a cache entry is readable once the first answer begins), with what sending one first would save.
- **OpenAI and Gemini** cache implicitly. There the doctor only reports prefix-stability problems.
- **Economics shown to the user:** cache reads cost about 0.1× base input (lower on some models), writes 1.25× for the 5-min TTL or 2× for the 1-hour TTL.

### 6.4 Firewall (`features/firewall`)
- Every text of a request is scanned before anything is sent anywhere: messages, system, tools and tool results, in every API format. Identifiers (`model`, `role`, `id`…) and inline data (base64 images, files) are skipped. Detectors come from `pkg/redact`, grouped in Promptdam's presets (`GET /v1/firewall/presets`):
  - **"source code"**, the secret class: `.env` assignments, private keys, cloud credentials, connection strings, API tokens
  - **"customer data"**, the pii class: email, phone, card, IBAN, national IDs (IPv4 only when chosen)
  - **custom**: a project's own named RE2 patterns.
- **Actions, per class:**
  - `block`: 400 `firewall_blocked` naming the detectors, never the value. The call goes nowhere and is logged.
  - `redact`: masked (`[REDACTED:email]`).
  - `tokenize`: `⟦email_1⟧`. The mapping lives only in the call's memory and is restored in the answer, whole or streamed, translated or not. A value JSON must escape is redacted instead. Secrets can't be tokenized: a model must never get them back to repeat.
  - `alert`, `off`.
  - Defaults: secrets blocked, PII off, custom patterns alerted, answers scanned, no excerpts.
- **Policies** (CEL, `pkg/policy`) run in order, and the first that holds decides:
  - `block`, `allow` (cancels a class's block), or `route_to` a model, which then serves the call alone, without the fallbacks it keeps the call from.
  - They see `request` (as routing rules do), `findings` (detector → count), `classes`, `locations` (part → count), `target` (provider, kind and model the call would go to), `principal` (key, developer, user) and `org` (plan).
- **Order on a call:** the scan comes before routing, so nothing past it sees what it masks:
  - the decision model, which isn't asked about a blocked call at all;
  - evaluation's replays (calls whose answers get tokens back aren't sampled);
  - the response cache, whose key is the masked request. Calls that differ only in tokenized values share a cached answer, each restored with its own values.
  - Policies run once the target is known.
- **Answers** are scanned per place in their events, for alerts only: what was written can't be taken back.
- **Fail mode**, when a request can't be scanned: refused while secrets are blocked (`secrets_closed`, the default), always refused (`closed`), or let through (`open`).
- **Plans:**
  - secrets on every plan, masking or tokenizing PII on Pro, policies on Team, custom patterns on Business.
  - A downgrade never loosens a firewall: what is saved keeps applying. It may be kept or removed, but not added to.
- **Events:**
  - The request log carries each call's `firewall_action` and `firewall_kinds`.
  - `firewall_events` holds one row per detector and part of a call, or the policy that decided, with masked excerpts only when the project keeps them.
  - API: `GET …/firewall/events`, `GET …/firewall/stats`, and `POST …/firewall/test`, a dry-run that also takes unsaved settings.
- **Alerts (worker):**
  - Every minute, one `firewall.blocked` per project listing the calls it refused (up to 100, the rest counted), and an alert to the organization's channels at most hourly per project.
  - A weekly digest per organization: what was refused, tokenized, redacted and alerted on, and what was found most.

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
| `firewall_settings` | settings jsonb (classes' actions, PII detectors, custom patterns, CEL policies, fail mode, response scan, excerpts); one per project |
| `budgets` | scope (org/project/key/developer/route), scope_id, window (day/month), limit_micros, action (alert/block), thresholds[] |
| `savings_reports` | period, verified_savings_micros, share_micros, status (draft/approved/disputed/invoiced), approved_by |
| `alert_rules` | name, kind (spend_spike, error_rate, provider_down, latency), threshold, window_minutes, min_calls, min_micros, provider, model, channel_ids[], enabled, cooldown_minutes, firing, firing_on[], fired/resolved_at; written only on transitions (each minute's measure is in Valkey) |
| `capture_settings` | enabled, sample_rate, answers (one per project; payload capture) |
| `payload_segments` | key (the object in R2 holding many sealed payloads), bytes, payloads, expires_at; never payload content |
| `cache_policies` | families jsonb (per family, at most 256: prefix and tail lifetimes, the gaps that weighed them, calls written together), learned_at; one per project |

**ClickHouse:**
- `requests(org_id, project_id, key_id, route, family_id, provider, requested_model, served_model, api_format, stream, status, http_status, ttfb_ms, latency_ms, upstream_ms, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_micros, priced, counterfactual_micros, savings_micros, savings_reason, savings_basis, response_cache_hit, conversation, turn_kind, human_turns, chain_gap_ms, chain_state, router, tier, tier_source, tier_confidence, effort, healed, router_micros, decision, decision_ms, firewall_action, firewall_kinds Array(LowCardinality(String)), payload_ref (segment key#offset+length), developer, tags Array(String), error_code, payload_ref, ts, retention_days)`
- `firewall_events(project_id, ts, trace, org_id, key_id, developer, route, requested_model, provider, detector, class, action, location, count, blocked, blocked_by, policy, excerpt, retention_days)`
- `activities(project_id, ts, id, org_id, kind, severity, title, body, subject, url, data)`: the activity feed, TTL 90 days (moved from Postgres: it grows with traffic)
- `router_evals(project_id, ts, router, trace, family_id, served_tier, tier_source, baseline_model, candidate_tier, candidate_model, verdict, check, judge_model, consistent, both_orders, candidate_micros, baseline_micros, judge_micros, candidate_output_tokens, baseline_output_tokens, features Array(UInt32), retention_days)`
- `cache_gaps_hourly` (a family's calls by their gap to the conversation's previous call, in cached tokens) and `prefix_starts` (calls by prefix, 8 days): what the cache lifetime learner weighs

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
    - Content is never dropped. A call the target can't express (audio, files by ID, several answers, Anthropic's server tools, an assistant prefill) passes that target over for the next, or fails with 400 `untranslatable_request`. Tuning parameters without an equivalent (seed, logit_bias, top_k…) are left out and listed in `X-Promptdam-Dropped`, as are the sampling parameters (temperature, top_p, top_k) of a call moved to a model that refuses them (Claude's since Opus 4.7).
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
    - **Suggested routers (2026-10-08):** `GET …/routers/suggest` builds a router to start from out of the project's own providers: each tier the newest model of a known family a provider serves (Claude Haiku, Sonnet, Opus; GPT nano, mini, base; Gemini Flash-Lite, Flash, Pro; Mistral, DeepSeek and Grok likewise), else picked by price; the same tier of the other providers as fallbacks; the model the project's calls used most in the last 7 days (or `current_model`) as the default tier; Claude Sonnet as the judge when a provider serves it. The answer is a router body to create as is, in shadow mode, or to edit; each tier says how it was picked and what it costs. Customers still define tiers entirely their own way.
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
    - The model reads the tiers, cheapest first, with their descriptions and up to 3 examples each, the call's instructions (1,000 characters) and last user message (4,000), and answers `{"tier": …}`, held to a schema with the tiers' names where the model takes one, except Claude models, which answer faster without (0.9 s at the median against 1.4 s on Haiku 5.5, 2026-10-14). It is asked with no temperature (current models refuse one), and a Claude model that thinks unless told not to is told not to; one that always thinks is asked at its least effort, with room to think. Any chat model of either API format on the project's own providers.
    - The router's timeout bounds the wait; no answer in time, or one naming no tier, leaves the default tier. Answers are cached per router, family and message for 10 minutes in each gateway.
    - **2026-10-14:** the default timeout (150 ms, too short for most hosted models: a router whose decision model was Claude Haiku got no answer, silently) became an auto timeout measured per decision model (§6.2, step 5); a router's own is 20 ms to 3 s. Outcomes and waits are logged (`decision`, `decision_ms`), rolled up in `decisions_hourly`, and the router report shows them. Measured live on Haiku 5.5: 32 of 33 questions answered, median 0.9 s, 99th percentile 1.5 s.
    - What it cost counts as the call's spend (and against its budgets), is logged as `router_micros` and comes off the call's savings.
    - Explain says `unsure` when a decision model would be asked.
    - Not yet: OpenRouter's typed decision models (`output_modalities=decisions`), whose request shape differs; any chat model works meanwhile.
- [x] S3.7 Activation gate, `router.ready`, the canary and auto-demotion (`router.tier_demoted`, alerts).
  - **Done 2026-10-08 (`routers/watch.go`):**
    - **The gate:** making a router active needs its last 14 days of shadow evidence to show every cheaper tier it chose meeting the floor (the Wilson lower bound, at least 50 judged calls), else 409 `router_not_ready` naming the tiers that fall short. `override: true` activates it anyway, the customer's call. A new router can't start active without the override; an active router's edits need none.
    - **`router.ready`:** the worker checks shadow routers hourly and announces the first time one qualifies (webhook and the organization's alert channels); a changed router is announced anew.
    - **The canary:** hourly, for each active router's cheaper tier, the calls it served in the last 7 days that were judged. When the upper bound of their quality's 95% interval is under the floor (at least 50 judged), the tier is demoted: the router sends its calls to the next stronger tier, `router.tier_demoted` fires and the channels are alerted. A client may still name a demoted tier. `POST …/routers/{router}/restore` puts it back.
    - Routers, their reports and explanations show demoted tiers and `ready_at`.
    - On the way: ClickHouse compares the millisecond `ts` column wrongly with a whole-second time within that second, so the routers' queries bind times in milliseconds, as the request log's already did.
- [x] S3.8 Keel K7.1: streaming-safe redact scanner with chunk overlap; reversible tokenisation restored in streamed responses.
  - **Done 2026-10-08 (core `pkg/redact`, additive):** `Stream` finds in text that arrives in pieces what one scan of the whole finds, each value once (16 KiB windows, 4 KiB overlap). `Restorer` puts tokens back in pieces, holding back only what may still become a token. `Token` mints one for a value of the caller's choosing.
- [x] S3.9 Firewall presets (source code, customer data), CEL policy sets, actions (block/redact/tokenize/alert/route_to), fail-closed for secrets and open for the rest by default (decided 2026-10-08).
  - **Done 2026-10-08 (`features/firewall`, §6.4):**
    - Settings per project (`firewall_settings`), carried in the gateway's snapshot compiled. `GET`/`PUT …/firewall` set them, with plan gating.
    - The gateway scans every call before routing and runs the policies once the target is known; answers are restored and scanned.
    - End-to-end tests: secrets never reach a provider; personal data reaches it tokenized and comes back restored, in both formats, whole and streamed, translated or not, cached answers included; the three policy actions.
    - A policy set is the project's ordered policies; named sets shared across projects can come later if customers ask.
    - Cost: about 10 µs per KB of request and under 1 µs per streamed event on a laptop.
- [x] S3.10 Firewall events (masked excerpts, stats), alerts, weekly digest; the firewall UI is the owner's.
  - **Done 2026-10-08:**
    - `firewall_action` and `firewall_kinds` on the request log.
    - `firewall_events`, listed (`…/firewall/events`, filtered and paged) and counted (`…/firewall/stats`), purged with their project.
    - `firewall.blocked` webhooks, hourly alerts and the weekly digest from the worker.
    - On the way: the request log now waits for rows in flight before it stops.
- [ ] S3.11 Tag core milestone **`core-v0.4.0`**.
- **Exit:** a router in shadow mode on a design partner's traffic reaches `router.ready`, then serves it active with measured savings; a forced quality drop demotes a tier within one window; secrets in a test prompt are blocked before leaving.

### S4: Billing, launch readiness and lead magnet (Tue Apr 13 – Fri Apr 23)
- [x] S4.1 Billing: plans and the `requests` overage meter. The savings-plan base, `savings_share` meter and monthly report approval are deferred (2026-10-07).
  - **Done 2026-10-09.** keel's billing and metering do the work: checkout, upgrades at once and downgrades at the
    period end, the spend cap, hourly usage reports to Stripe's meter, and the notices. This round proved Promptdam's
    catalog through them end to end, against a fake Stripe (core `keel.WithBillingGateway`, additive) and stripe-mock:
    - Free stops at 50,000 with 429 `quota_exceeded` until the month ends. Calls refused before they go anywhere
      aren't counted; answers from the response cache are.
    - Pro goes on past its 500,000, and Stripe is told every request once. The default cap is the plan's price.
    - The spend cap refuses calls with 429 `spend_cap_reached` (every refusal said "no requests left this month"
      before) and bills no more than the cap. Resetting it lets calls through. Refusals say when calls resume.
    - A cancelled plan keeps its features until its period ends, with its overage billed. Then Free's limits and
      features apply, counted afresh.
    - The catalog's prices, the requests' metered prices included, sync to stripe-mock under the lookup keys
      checkout uses.
  - Noted, for later (the core's, or Stripe's settings):
    - Changing a plan's included requests or overage price needs new lookup keys: `stripe sync` keeps an existing
      metered price.
    - No warning before the spend cap: notices come at 80% and 100% of the included requests, then when the cap
      stops calls.
    - A mid-period upgrade: Stripe bills the old plan's overage up to the upgrade and counts the new plan's included
      requests from then on, while Promptdam's counter keeps the period's total. The dashboard's estimate and the
      invoices can differ for that period.
    - Requests counted between an invoice being made and the renewal reaching the gateway (about a minute) may land
      in the ended period and miss its invoice.
    - A subscription cancelled for an unpaid invoice gets no final invoice: that period's overage isn't billed (by
      policy).
    - Renewals that need 3D Secure: billing doesn't handle `invoice.payment_action_required`, so Stripe's own
      customer emails must ask for it (keel `docs/billing-policy.md`).
    - `billing reconcile --stripe` compares live, stored, reported and Stripe usage when run; nothing runs it on a
      schedule yet.
  - **Needs you:** one checkout in Stripe test mode end to end (`deploy/README.md`), then live keys at S5.4.
- [ ] S4.2 "LLM Bill Audit" free tool on the site. The user uploads OpenAI/Anthropic usage exports (CSV), or a 7-day sample through Promptdam's free plan. Output:
  - an estimated savings breakdown (model downgrade potential by family size, caching potential, batch potential)
  - a PDF/email report and a CTA.
- [x] Dashboard API (pages.md §6), built 2026-10-08 ahead of the dashboard:
  - `GET …/models` for model pickers (shared with the gateway's `/v1/models`), list prices beside the project's own, `GET …/prices/missing`.
  - Request log filters (status or class, error code, firewall action, router, tier, cache), firewall events by request.
  - `GET …/overview` (the Overview page in one call), `GET /v1/orgs/{org}/analytics/projects`, the ledger export (CSV, Pro).
  - The activity feed (`activities`, each budget's, router's and alert rule's history), alert rules (`alert_rules`, Pro) with `alert.fired` and `alert.resolved`.
  - Plan checks: prompt caching on routes and the cache doctor's fixes (Pro), developer keys and developer budgets (Team), the audit log (Team, core `AuditFeature`); routers say `watching` and `learned`.
  - The audit log records every project change. Calls no provider answered are logged with the provider and model they tried.
  - `dev seed -openai-key … -anthropic-key …` seeds a project with the real providers.
  - Payload capture, built the same day (decided with the owner: every plan; secrets and personal data always masked):
    - Off by default; `…/payloads/settings` turns it on per project, with a sample rate and answers on or off.
    - What is kept: the request as the provider got it and the answer the client got (read down to its text and tool
      calls; a failed call's error). Both are masked with every detector and the project's own patterns, gzipped and
      sealed with the vault per organization.
    - Storage: payloads are packed in per-project segments in object storage (R2), written a minute or 4 MiB at a
      time: a write per segment, not per call. Postgres keeps only each segment's key, size and expiry, never payload
      content. The request log's `payload_ref` points at the segment, offset and length.
    - Reading: `GET …/requests/{request}/payload` (developers and up, audited); `DELETE …/payloads` empties them.
    - Retention: an hourly job deletes segments when the plan's retention ends (Free's 7 days included; object
      storage's 30-day tier is only a backstop). Purges take a deleted project's or organization's.
    - Calls the firewall tokenized aren't kept.
- [ ] S4.3 Docs: concepts (families, rules, routers, ledger, firewall), provider setup, migration guides (from OpenRouter, LiteLLM, Portkey, Helicone), security and data handling page, API reference, `llms.txt`.
- [ ] S4.4 Load test at 1k req/s with streaming on the Promptdam VPS (2 gateway replicas behind Caddy); uptime checks; status page; documented client-side fallback to the provider for when the gateway is unreachable.
- [x] S4.5 Security pass: vault for provider keys, payload-free default, tenant isolation, SSRF guard on custom provider URLs, dependency audit, signed container images.
  - **Done 2026-10-08:** an audit of the gateway, the API and what is stored, then a commit per fix.
  - Already sound:
    - govulncheck is clean (the only advisory, `x/crypto/openpgp`, is unreachable and has no fix), and CI runs it
      with gitleaks and golangci-lint.
    - Provider keys are sealed by the vault at rest and in Valkey and opened only in gateway memory; production
      refuses to start without vault keys.
    - Custom provider URLs are https and public when saved, and re-checked at dial time against every private range.
    - Every ClickHouse value is bound; every lookup by ID filters on the caller's project; no handler reaches an
      unscoped query.
  - Fixed:
    - **The gateway:** a provider's answer nested beyond 10,000 levels is relayed unscanned instead of growing the
      scanner's stack. A provider's 401 or 403, which can quote part of the project's key, becomes the gateway's own
      `provider_auth_failed`; other error bodies pass with secrets and personal data masked. Providers' `X-Promptdam-*`
      and account headers (`openai-organization`, `openai-project`, `anthropic-organization-id`) are dropped.
    - **Routing:** the decision model's cache is keyed by project (it was shared by every project with the same
      router name). The firewall's policies run before the decision model is asked, so a blocked or redirected
      prompt never reaches its provider, and calls a policy decided aren't sampled for evaluation. The decision
      model's and the judge's clients don't follow redirects, which would re-send a key to another host.
    - **The firewall:** patterns are at most 1,000 characters and conditions 4,096. Excerpts mask every span any
      detector finds in the whole text, cut ones included.
    - **Smaller:** changing a route's cache or router, or deleting it, clears its cached answers. Tags from
      `X-Promptdam-Tags` keep to a key's tag characters. A key budget's key must be one of the project's keys.
  - **Payload-free mode** (Business, decided with the owner to build now): `GET`/`PUT /v1/orgs/{org}/privacy`, set by
    admins and owners, audited.
    - While it's on, projects can't turn on payload capture, firewall excerpts or a route's response cache (409
      `payload_free`). The gateway keeps nothing either way: saved settings stay, inert.
    - Turning it on deletes what was kept: the organization's payloads and cached answers.
    - A downgrade keeps it on; turning it off needs no plan.
  - **Signed images** (decided with the owner: GHCR, cosign keyless): base images pinned by digest. keel's
    `promptdam-release` workflow builds the four commands on a `promptdam-v*` tag, pushes them to
    `ghcr.io/hasan-muzak/promptdam-<command>`, signs each digest with the workflow's GitHub identity and attests an
    SPDX SBOM. `deploy/README.md` shows how the VPS verifies before it pulls.
  - Noted, not changed:
    - Router evaluation features and the request log's `family_id` and `prefix_hash` are unsalted hashes: someone
      who can read the project's data can confirm a guessed prompt. A per-project salt means retraining classifiers;
      later.
    - The response cache keeps answers in Valkey as plain JSON (opt-in, with a TTL; off in payload-free mode).
    - Core `events`' `{tenant}` counts the `?`s before it to place its arguments: correct today, worth a comment in
      the core.
- [x] Round G (2026-10-14, ADR 0002): what a study of Caveman (a local proxy for coding agents) showed Promptdam
  lacked, fixed our way.
  - **Failures:** "prompt too long" goes on to the next target; a call the provider got whole is never sent to it
    twice; `Retry-After` honoured; a broken answer cuts the connection; stream errors count against the circuit.
  - **Ledger:** unpriced calls save nothing (`priced`), each saving says how it is known (`savings_basis`), and a
    routed call's counterfactual is measured on replays.
  - **Turns:** core `wire.Turn`; `features/conversation` keys and keeps turns on every replica; a turn keeps its
    tier, the decision model is never asked mid-turn, a warm conversation stays put unless switching pays.
  - **Effort:** tiers set a reasoning effort, fitted per model and written in each provider's words.
  - **Caching:** per-target minimums, bytes only added, the conversation's tail, OpenAI's `prompt_cache_key`, and
    lifetimes learned per family; the doctor counts calls written together. Thinking a rewrite invalidated is
    healed once.
  - **Answers** name the model asked for; the judge is asked both ways where it flips.
  - Not now: per-message effort marks, keep-alive pings, holding parallel calls back (measured first).
  - **Found by a customer's conversation (2026-10-14, `server/livecheck`, `task demo:customs`):** the decision model
    worked on no current Claude model (a temperature they refuse, thinking that ate its 64-token answer), the
    judge's 1,024 tokens left thinking models little room, and a call moved to a model that refuses sampling
    parameters failed. The catalog now says how each model thinks and whether it takes
    sampling (`llm.Known` for models a price list doesn't have yet); a moved call loses the sampling its model
    refuses, listed in `X-Promptdam-Dropped`.
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
- [x] Firewall after a downgrade: **it never loosens** (decided 2026-10-08). Saved settings keep applying; changing a part to what the plan lacks needs the plan, keeping or removing it doesn't.
- [x] **Write-heavy data stays out of Postgres** (decided with the owner, 2026-10-08): payloads and other per-call content go to object storage (R2) with only their path kept; event logs go to ClickHouse; live per-minute state goes to Valkey, with Postgres written on transitions. Small configuration columns may stay jsonb.
- [x] A firewall policy's `route_to` model **serves the call alone**, without the route's fallbacks, which may be the very provider the policy keeps the call from (decided 2026-10-08).
- [x] Routers per plan (decided 2026-10-08): shadow mode with evaluation on every plan, active routers on Pro, learned placement (classifier, decision model) on Team (§5).
- [x] Images: **GHCR, signed with cosign keyless**, an SPDX SBOM attested with each (decided 2026-10-08). No signing key to keep; the VPS verifies the release workflow's identity.
- [x] Payload-free mode: **built before launch** (decided 2026-10-08), the Business plan's guarantee that nothing a call says is kept. Turning it on deletes what was kept; a downgrade keeps it on.
- [x] Include OpenAI Responses API routing at MVP: **passthrough only** (auth, budgets, usage, cost, log; no model routing or caching), routing later (decided 2026-10-07).

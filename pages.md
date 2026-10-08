# Promptdam dashboard: pages

> What the dashboard (`web/app`) shows and lets customers manage, page by page: the sidebar, its groups, and what each
> page must contain. Written from the customer's side: what an engineering lead, a platform or security engineer, or a
> small team opening Promptdam wants to see first, and what they come back to change.
> Every page lists the API it reads, so the dashboard is built on what exists; §6 lists what the API still lacks.
> Companion to `project.md` (§4 scope, §5 plans, §6 architecture). Written 2026-10-08.

---

## 1. Who opens the dashboard, and why

| Customer | Comes for | First question on opening |
|---|---|---|
| **Eng lead / CTO** | the bill | "How much did we spend this month, is it going up, and how much did Promptdam save us?" |
| **Platform / security engineer** | the firewall, keys, budgets | "Did anything sensitive try to leave? Is every team on a budget?" |
| **Developer** | setup, debugging | "Is my key working? Why did this call fail, or go to that model?" |
| **Finance / billing member** | invoices, proof | "What do we pay, and what is it worth?" |

Their recurring jobs:
1. Watch spend and catch spikes early (by model, key, developer, feature).
2. See the savings, with proof per request.
3. Make calls cheaper safely: routers, rules, caching fixes.
4. Keep secrets and personal data from reaching providers, and prove it.
5. Cap spend per team, key or developer.
6. Set up providers, keys and routes once, then rarely touch them.

---

## 2. Principles for every page

- **The answer first.** The top of each page answers the page's main question in one line and a few numbers. Tables
  and settings come below.
- **Money is the unit.** The API counts money in micros (millionths of a dollar). Show dollars: two decimals, four
  under a cent, `$1.2k` style above $10k in charts. Tokens are abbreviated (`1.2M`).
- **Everything links.** A trace opens its request; a family opens its family; a tier opens its router's report; a
  firewall event opens its request; a key opens its settings. IDs (`req_…`, `key_…`, `rtr_…`) can be copied in one click.
- **Time ranges** are on every data page, with the API's limits:
  - request log and firewall events: at most 31 days per query (default 24 hours for requests, 7 days for events)
  - analytics and families: up to 400 days (default 7 days, savings 30)
  - firewall stats: up to 90 days; router reports: up to 90.
  - The plan's retention bounds how far back anything goes (Free 7 days, Pro 30, Team 90, Business 1 year). Say so
    when a range reaches past it.
- **Locked, not hidden.** A feature the plan lacks is shown with what it would do for this project, ideally with a
  number from their own traffic ("Routing would have saved $412 last week"), and an upgrade button. The API answers
  402 `plan_required` with an upgrade URL; turn it into that state, never a raw error.
- **Roles decide the buttons.** Viewers see everything of a project but no edit buttons. Developers manage projects
  and keys. The billing role sees billing only. Admins and owners see all (§5.3).
- **Never the content.** The dashboard shows metadata, never prompts or answers: Promptdam doesn't keep them. Firewall
  events show detector names and, only when the project turned excerpts on, masked excerpts. Say this where it
  matters (request detail, firewall); it is part of why customers trust the product.
- **Empty states teach.** An empty page says what will fill it and how: a code snippet, a button, a link to docs.
- **Fresh enough.** Data pages refresh every 30 to 60 seconds while open and show "updated 20 s ago". Gateways apply
  configuration changes within seconds: say "Applied" after a save, not "Saved, deploy to apply".
- **Copy style:** short sentences, no em dashes (use commas, colons or full stops), no jargon the customer didn't
  bring (say "fallback", not "target chain").

---

## 3. The frame

### 3.1 Top bar
- **Organization switcher** (with "Create organization").
- **Project switcher**: every page except organization settings belongs to a project. Projects are often
  environments ("web", "prod", "staging"); show the project's name and a colored dot.
- **Search**: paste a trace (`req_…`, from the `X-Promptdam-Trace` header) to open its request. Later: keys,
  families, routers by name.
- **Plan meter**: requests used this month against the plan (`GET /v1/orgs/{org}/usage`), e.g. "312k / 500k". Amber
  past 80%, red past 100% (hard stop on Free, overage on paid plans). Click: Billing.
- **Gateway URL** with a copy button (`https://gateway.<domain>/v1`), the one thing every developer needs.
- **Help**: docs, API reference (muzak.dev/openapi/ui), status page, contact support.
- **User menu**: account, theme, sign out.

### 3.2 Sidebar

Groups, in order. Items marked with a plan show the lock when the plan lacks the feature, but stay clickable (§2).

| Group | Item | Route | Plan | One-line purpose |
|---|---|---|---|---|
| **Overview** | Get started | `/get-started` | all | Setup checklist; shown until the project is activated (§4.1), then moves into Help |
| | Overview | `/` | all | Spend, savings, risk and what needs attention, at a glance |
| **Traffic** | Requests | `/requests` | all | The request log: every call, its model, cost, savings, firewall action |
| | Analytics | `/analytics` | all | Spend, latency and errors over time, grouped |
| | Families | `/families` | all | Prompt templates by spend: what each feature costs |
| **Savings** | Savings | `/savings` | all (export Pro) | The ledger: what Promptdam saved, how, with proof |
| | Routers | `/routers` | shadow all, active Pro, learned Team | Tiers of models; each call placed in the cheapest that holds up |
| | Routing rules | `/rules` | Pro | "Calls like this go to that model" |
| | Cache doctor | `/cache-doctor` | Pro | Why prompt caches miss, and the fix |
| **Protection** | Firewall | `/firewall` | secrets all, PII Pro, policies Team, custom Business | Secrets and personal data blocked, masked or tokenized |
| | Budgets | `/budgets` | Pro | Spend caps per project, key, developer or route |
| **Setup** | Providers | `/providers` | all | The project's LLM providers and keys (BYOK) |
| | API keys | `/keys` | all (per-developer Team) | Gateway keys and what each may do |
| | Routes | `/routes` | all | Fallbacks, retries, caching per path |
| | Prices | `/prices` | all | The project's own prices (discounts, self-hosted models) |
| | Project settings | `/settings` | all | Name, IDs, delete |
| **Organization** (bottom) | Settings | `/org/*` | all | General, Members, Projects, Billing, Alert channels, Webhooks, Audit log |

**Sidebar badges**: the few states worth a glance, each linking to its page:
- Routers: a dot when a router is ready to activate (`router.ready`) or a tier was demoted.
- Firewall: the number of calls refused in the last 24 hours.
- Budgets: the number of budgets past their first threshold this window.
- Providers: a red dot when a provider's last check failed or a provider is disabled while routes name it.

---

## 4. Project pages

Each page: **Purpose**, **Shows**, **Actions**, **Empty state**, **Plan**, **API**. Paths are under
`/v1/orgs/{org}/projects/{project}` unless they start with `/v1/`.

### 4.1 Get started (`/get-started`)
**Purpose:** take a new project from nothing to its first logged call in five minutes. "Activated" is 1,000 requests
within 7 days (project.md §9).

**Shows:** a checklist, each step ticking itself:
1. **Add a provider**: OpenAI, Anthropic, Gemini, Mistral, DeepSeek… or any OpenAI- or Anthropic-compatible endpoint.
   Inline form; the key is checked against the provider on save (it costs nothing).
2. **Create a gateway key**: shown once, with copy. Explain `pd_sk_…`.
3. **Point your client at Promptdam**: tabs with a ready snippet, the key and gateway URL filled in:
   - OpenAI SDK (Python, Node): `base_url`
   - Anthropic SDK: `base_url`
   - Vercel AI SDK, LangChain
   - Claude Code: `ANTHROPIC_BASE_URL` and `ANTHROPIC_AUTH_TOKEN`
   - curl.
4. **Send a request**: waits for the first call (poll the request log), then shows it: model, tokens, cost, latency.
   "It works" moment.
5. **Next wins** (after the first calls), each a card with a button:
   - "Create a router from your providers" (the suggested router, in shadow mode: free, it only watches).
   - "Turn on prompt caching for the default route" (Anthropic).
   - "Your secrets are already blocked": a link to the firewall, with a one-line test.

**API:** `GET /v1/provider-kinds`, `POST …/providers`, `POST …/providers/{provider}/check`, `POST …/keys` (scope
`gateway`), `GET …/requests?limit=1`, `GET …/routers/suggest`.

### 4.2 Overview (`/`)
**Purpose:** answer "how are we doing?" in one screen: money, savings, risk, and what to do next.

**Shows**, top to bottom:
- **Headline** for the range (default this month): "**$8,412 spent**, **$3,950 saved** by Promptdam (32% of what it
  would have cost)". Against the previous period: "+12% spend".
- **KPI tiles:** requests (and plan use), spend, saved, error rate, p95 latency, cache hit rate (response cache and
  prompt cache), calls refused by the firewall.
- **Spend vs. without Promptdam** per day: two lines (actual cost and counterfactual); the gap is the saving. The
  single most convincing chart in the product.
- **Needs attention** (only what applies, most urgent first):
  - a budget past a threshold or blocking ("Key ci-bot is at 92% of its $200 daily budget");
  - a router ready to activate ("Router auto would save ~$1,240/month; ready since Tuesday");
  - a demoted tier ("Router auto demoted fast: quality fell to 88% under the 95% floor");
  - a provider whose last check failed;
  - firewall refusals in the last 24 hours;
  - the plan's requests above 80%;
  - unpriced models ("3 models have no price: costs and savings miss them. Add prices").
- **Opportunities**, each with a dollar figure from their own traffic:
  - the cache doctor's top fix ("Family 'Support reply' would save ~$310/month: move the date out of the system prompt");
  - a shadow router's projected monthly savings;
  - routing rule candidates (later, §6).
- **Where the money goes:** top 5 models, top 5 families, top 5 keys or developers, each with spend and share.
- **Recent firewall refusals:** the last 5, with detector and key.

**Empty state:** the Get started checklist inline.

**API:** `GET …/analytics/spend` (totals, series, `group_by=model|key|developer`), `GET …/analytics/savings`,
`GET …/analytics/latency`, `GET …/families?limit=5`, `GET …/budgets`, `GET …/routers` + `GET …/routers/{router}/report`,
`GET …/providers`, `GET …/firewall/stats`, `GET …/firewall/events?blocked=true&limit=5`, `GET …/cache-doctor`,
`GET /v1/orgs/{org}/usage`.

### 4.3 Requests (`/requests`)
**Purpose:** the request log. "What happened to this call?" and "show me the failing ones".

**Shows:** a dense table, newest first, cursor-paged (infinite scroll):
| Column | Notes |
|---|---|
| Time | relative, exact on hover |
| Status | 200 green; 4xx amber; 5xx red; the error code as a chip (`budget_exceeded`, `firewall_blocked`, `provider_429`…) |
| Model | requested → served when they differ (routed, fallback), the provider as a small label |
| Tokens | in / out; cache read as a sub-label |
| Cost | the call's cost |
| Saved | the saving and its reason as a chip: routing, response cache, prompt cache, fallback |
| Latency | total, TTFB on hover |
| Router | `auto · fast` with the step that chose it (rule, classifier, decision, default; "shadow" when it only watched) |
| Firewall | the action as a chip (blocked, tokenized, redacted, alerted) with the detectors on hover |
| Who | key name, developer, end user; tags as chips |
| Route, family | the family's label when it has one |

**Filters:** model, provider, route, key, developer, errors only; time range (31 days at most per query). Saved
filters later.

**Actions:** open a row (detail drawer, §4.4); copy trace; export the page as CSV (later).

**Empty state:** "No calls yet" with the client snippet (§4.1, step 3).

**API:** `GET …/requests` (filters, `cursor`, `limit`), `GET …/requests/{request}`.

### 4.4 Request detail (drawer, or `/requests/{trace}`)
**Purpose:** everything Promptdam knows about one call, without its content.

**Shows:**
- **Summary:** status, error code and what it means (a sentence per code, with the fix: "Your key may not call this
  model: allow it in the key's settings"), time, trace (copy), API format, stream, translated (and the parameters
  translation dropped).
- **Where it went:** requested model, served model, provider, attempts, fallback used; for router calls: the router,
  the tier that served, the tier it decided, the step that decided and its confidence, shadow or not; the routing rule
  that matched. A link: "Explain how the router places a request like this".
- **Cost:** input, output, cache read, cache write and reasoning tokens; cost; counterfactual; saved and why. A
  decision model's cost on the call, when one was asked.
- **Timing:** a small bar: gateway time, waiting on providers, TTFB, total.
- **Firewall:** the action and the detectors found (never the values); a link to the call's firewall events.
- **Who:** key (link to its settings), developer, end user, tags, route.
- **Family:** label or ID (link), and the cache doctor's verdict for it.
- A note: "Promptdam keeps no prompts or answers."

**API:** `GET …/requests/{request}`; events by trace need a filter (§6).

### 4.5 Analytics (`/analytics`)
**Purpose:** trends and breakdowns: "what drives our spend, and is it getting worse?"

**Tabs:**
- **Spend:** a stacked area per day or hour, grouped by model, provider, key, route, developer or tag (at most 100
  groups, by spend). Below, a table: group, calls, errors, input and output tokens, spend, share, change against the
  previous period. Range up to 400 days. Totals at the top.
- **Latency and errors:** p50, p95, p99 latency and TTFB per model or provider; error rate per model and provider over
  time. Helps answer "is it the provider or us?"
- **Caching:** response cache hits, prompt cache read share of input tokens, over time (from spend sums:
  `cache_read_tokens`).

**Actions:** change grouping, interval, range; click a group to filter the request log by it.

**API:** `GET …/analytics/spend` (`group_by`, `interval`, range), `GET …/analytics/latency` (`group_by=model|provider`).

### 4.6 Families (`/families`) and family detail (`/families/{family}`)
**Purpose:** "which feature of our product costs what?" A family is one prompt template: calls whose instructions,
tools and answer format match once changing values (dates, IDs, numbers) are set aside, or that name it in
`X-Promptdam-Family`. Explain this in one sentence at the top: customers think in features, not families.

**List shows:** label (editable inline; Promptdam keeps no text, so an unnamed family shows its ID, its top model
and a "Name it" button), calls, spend and share bar, input and output tokens, cache read share, errors, the cache
doctor's flag when it found something.

**Detail shows:**
- name (editable), ID, calls, spend, tokens over the range;
- **model mix:** requested → served models with calls, cost and latency percentiles;
- daily series of calls and spend;
- **cache health:** the cache doctor's diagnosis for this family (§4.10);
- **savings** of the family (from the savings ledger's families);
- **router quality** for the family, when a router judged its calls (from the router report's families).

**API:** `GET …/families`, `GET …/families/{family}`, `PUT …/families/{family}` (label),
`GET …/families/{family}/cache-doctor`, `GET …/analytics/savings` (families), `GET …/routers/{router}/report`.

### 4.7 Savings (`/savings`)
**Purpose:** the proof. "What did Promptdam save us, and can I show my CFO?"

**Shows:**
- **Headline:** saved this range, the counterfactual ("what it would have cost") and the actual cost; savings rate.
- **By reason:** routing, response cache, prompt cache, fallback (which can be negative: a fallback model may cost more
  than the one asked for). Each with calls, counterfactual, cost, saved.
- **Per day:** cost and savings bars.
- **Top families** by savings.
- **How it's counted**, always visible: "For each call, the counterfactual is the model your client asked for, at its
  list price, without any cache. Savings are the difference. Calls that named a router are counted against its
  default tier. Router evaluation costs come off the savings." Link to a request's ledger line.
- **Monthly report:** a printable or PDF view of the month for finance (later).

**Actions:** range; export the ledger as CSV (Pro; §6).

**Plan:** read on every plan; export on Pro.

**API:** `GET …/analytics/savings`.

### 4.8 Routers (`/routers`)
**Purpose:** the core feature. "Send each call to the cheapest model that does it as well, proven on our own traffic."
Customers define their own tiers ("fast", "smart", "expert", or "easy", "decision", "hard", "reasoning"), any models of
any of their providers.

**List shows** one card per router:
- name, and how clients call it: `"model": "auto"`, `auto:fast` for a tier of their choice, or a route attached;
- **status:** shadow (watching: decides every call, serves the default tier, measures), active, disabled; "watching:
  your plan doesn't include active routers" after a downgrade;
- tiers, cheapest first, as chips with their models;
- last 7 days: calls; in shadow mode **what following it would save** (and per month); when active, **saved**;
- **readiness:** "Ready to activate", "Needs 34 more judged calls on fast", "fast below its floor", demoted tiers;
- evaluation spend this month against its budget.

**Actions:** create a router (the wizard, §4.8.1); open one.

**Empty state:** "Routers find cheaper models that hold up on your calls. Start with one built from your providers; it
only watches until you activate it." Button: Create from suggestion.

**Plan:** shadow mode with evaluation on every plan (it runs on the customer's own keys); active on Pro; the
classifier and the decision model on Team.

**API:** `GET …/routers`, `GET …/routers/{router}/report` (per card).

#### 4.8.1 New router (wizard)
1. **Start from a suggestion:** tiers built from the project's providers (Claude Haiku → Sonnet → Opus, GPT nano →
   mini → base, Gemini Flash-Lite → Flash → Pro…), the model used most in the last 7 days as the default tier. Choose
   the number of tiers; or start empty.
2. **Tiers:** for each, a name, a plain-words description ("simple questions, short answers"), a model picker (any
   provider's model; models of the other API format are translated), fallbacks, a few example requests. Reorder from
   cheapest to strongest. The default tier is marked: calls nothing places get it, and the others are judged against it.
3. **Rules** (optional): "calls with tools go to smart", as CEL with autocomplete of `request.*` (model, input_tokens,
   messages, has_tools, json, stream, family, tags, developer, route…) and errors at their line and column.
4. **Evaluation:** quality floor (default 95%), sample rate (default 10% in shadow, 2% active), monthly evaluation
   budget (default $100, on the project's own keys), judge model (default claude-sonnet-5-5 when an Anthropic provider
   serves it), decision model and its timeout (Team).
5. **Create:** starts in shadow mode. Shows how to call it.

**API:** `GET …/routers/suggest?tiers=&current_model=`, `POST …/routers`.

#### 4.8.2 Router detail (`/routers/{router}`)
Tabs:
- **Overview (the report):**
  - a status banner with the next step: "Ready: activating would save ~$1,240/month" with **Activate**; "Not ready:
    fast needs 34 more judged calls"; "Active since 3 Oct"; "Watching: your plan doesn't include active routers".
  - Activate: refused with 409 `router_not_ready` until the evidence holds up; the dialog says which tiers fall short and
    offers "Activate anyway" (override) with a clear warning.
  - money: cost of its calls, what they would have cost on the decided tiers, the monthly projection, ledger savings,
    evaluation cost, unpriced calls;
  - calls per tier: served vs. decided (they differ in shadow mode), as stacked bars;
  - how calls were placed: explicit, rule, classifier, decision, default;
  - range picker (default 7 days, up to 90).
- **Quality:** each tier against the default tier: judged calls, wins, ties, losses, quality with its 95% interval,
  drawn against the floor line; meets floor or not; demoted tiers with **Restore**. Families judged most, with their
  quality. One sentence on method: "A judge model compares a tier's answer with the default tier's on sampled calls,
  in random order."
- **Tiers and rules:** the wizard's editors (§4.8.1, steps 2 and 3), saved with PUT. Changing a tier clears readiness:
  say so before saving.
- **Classifier** (Team): version, trained when, per tier: threshold, held-out precision, coverage, AUC, samples.
  **Train now** (otherwise hourly when new judged calls exist).
- **Explain** (playground): paste a request body (OpenAI or Anthropic format), optionally a route, tier, tags, developer;
  see the tier it would serve and why (step, rule, confidence, unsure), what the call needs of a model (tools, images,
  files, JSON schema, context size), which tiers' models can take it, and the providers and models it would try, in
  order. No provider is called.
- **Settings:** status, default tier, quality floor, sample rate, evaluation budget, judge, decision model and timeout;
  delete (refused with 409 `router_in_use` while a route uses it: name the routes).

**API:** `GET/PUT/DELETE …/routers/{router}`, `GET …/routers/{router}/report`, `POST …/routers/{router}/explain`,
`POST …/routers/{router}/restore`, `POST …/routers/{router}/train`.

### 4.9 Routing rules (`/rules`)
**Purpose:** simple, explicit routing: "short calls without tools go to gpt-5-nano".

**Shows:** rules in evaluation order (priority; drag to reorder): name, route (or all routes), condition (code),
target model, status (draft, active, disabled) as a toggle, activated when. A note on the difference with routers:
rules route by what a request looks like; routers also measure quality.

**Editor:** name, route scope, CEL condition with autocomplete and inline errors, target model picker, priority.
**Dry-run** beside it: on the last 7 days (up to 20,000 calls), how many it would have routed, their cost now and on
the rule's model, the saving. Say plainly that a dry-run measures cost, not quality: "To measure quality, use a router
rule in shadow mode."

**Plan:** Pro to create and edit (402 otherwise); reading and dry-runs on every plan, so the upsell shows a number.

**API:** `GET/POST …/rules`, `GET/PUT/DELETE …/rules/{rule}`, `POST …/rules/dry-run`, `POST …/rules/{rule}/dry-run`.

### 4.10 Cache doctor (`/cache-doctor`)
**Purpose:** "why don't the providers' prompt caches work for us?" Often the fastest win.

**Shows:** the 20 busiest families by what fixing them is worth: model and provider, calls, prompt cache hit rate (a
gauge), how many different prefixes the calls sent, the prefix length, findings with severity, each with a title, the
detail and the fix ("A date changes in the system prompt every day: move it to the last user message"), and the
**potential monthly saving**. Sorted by that saving.

**Actions:** open the family; "Turn on prompt caching for route X" when the fix is the gateway's own (Anthropic
breakpoints), linking to the route.

**Plan:** Pro (show the potential savings on Free as the upsell).

**API:** `GET …/cache-doctor`, `GET …/families/{family}/cache-doctor`.

### 4.11 Firewall (`/firewall`)
**Purpose:** "nothing sensitive leaves without our say, and we can prove it." The page security engineers live on.

Tabs:
- **Overview:**
  - what is protected now, in one line per class: "Secrets: **blocked**. Personal data: **off** (turn on redaction,
    Pro). Your own patterns: none. Answers: scanned."
  - range stats: calls the firewall acted on, calls refused, findings per action and per detector, a daily chart
    (calls and refusals);
  - recent refusals; a hint that `firewall.blocked` webhooks and alerts exist (link to org webhooks and channels).
- **Events:** the table of what was found: time, request (link), key, developer, route, model, detector, class, action,
  where in the request (messages, system, tools…, or the answer), count, refused or not, by what (class, policy),
  masked excerpt when kept. Filters: detector, class, action, location, key, refused only; 31 days per query.
- **Protection** (the settings form):
  - on/off for the whole firewall (with a strong warning);
  - **Secrets** ("source code" preset: `.env` secrets, private keys, cloud credentials, connection strings, API
    tokens): block, redact, alert or off. Tokenize isn't offered for secrets: say why.
  - **Personal data** ("customer data" preset: email, phone, card, IBAN, national IDs; IPv4 optional): off, alert,
    redact, tokenize, block, and which detectors. Tokenize explained with the example: "the provider sees
    `⟦email_1⟧`, your user gets the real address back".
  - **Answers:** scan them (alerts only: what was written can't be taken back).
  - **If a request can't be scanned:** refuse it while secrets are blocked (default), always refuse, or let it through.
  - **Excerpts:** keep the masked text around each finding in its event (off by default; a privacy note).
  - Each detector listed with its description (from presets).
- **Policies** (Team): ordered rules, the first that holds decides: block, allow (cancels a class's block), or send to
  a model (which then serves alone, without fallbacks). A CEL editor with a variables panel: `request`, `findings`,
  `classes`, `locations`, `target`, `principal`, `org`, and ready examples: "no personal data to DeepSeek", "the CI key
  may send secrets to our self-hosted model", "cards go to the EU provider". Compile errors at their line and column.
- **Custom patterns** (Business): name and regular expression (RE2), with a test box; the action for the class.
- **Test** (playground): paste a request (any format), see what each finding gets, the request as the provider would
  get it (masked, tokenized; a diff view), the policy that decides and the target it saw. It tries the form's current,
  unsaved settings: "test before you save".

**Plan:** secrets and alerts on every plan; redacting or tokenizing personal data on Pro; policies on Team; custom
patterns on Business. After a downgrade, saved settings keep applying: show "kept from your previous plan" next to
them, editable only to remove.

**API:** `GET /v1/firewall/presets`, `GET/PUT …/firewall`, `GET …/firewall/events`, `GET …/firewall/stats`,
`POST …/firewall/test`.

### 4.12 Budgets (`/budgets`)
**Purpose:** "no team, key or developer can blow the month."

**Shows:** a card per budget: name, scope (project, a key, a developer, a route), window (day or month, UTC), a
progress bar of spent against limit with the thresholds marked, the window's end ("resets in 9 h"), action (alert, or
block: the gateway refuses the scope's calls once spent), channels it alerts, enabled. Blocking budgets that are spent
show a red banner: "Calls of key ci-bot are refused until 00:00 UTC".

**Editor:** name; scope and target (pick a key or route, type a developer); window; limit in dollars; action; alert
thresholds (default 80% and 100%); channels (default all of the organization's).

**Plan:** Pro to create and edit; listing on every plan.

**API:** `GET/POST …/budgets`, `GET/PUT/DELETE …/budgets/{budget}`, channels from `GET /v1/orgs/{org}/channels`.

### 4.13 Providers (`/providers`)
**Purpose:** the project's own LLM accounts (BYOK). Customers pay their providers directly; Promptdam never resells
tokens. Say this on the page.

**Shows:** name, kind (with logo), API format, base URL (regional or custom), key (set, with its last characters),
models (the catalog's for its kind, or the listed ones), priority among providers serving the same model, enabled, last
check (passed when, or the provider's error).

**Actions:**
- **Add provider:** pick a kind (OpenAI, Anthropic, Gemini, Mistral, DeepSeek, Groq, Together, xAI, Fireworks,
  OpenRouter, or custom for any OpenAI- or Anthropic-compatible endpoint), name, key, base URL (custom or regional), models, priority.
  Checked on save.
- **Edit:** the key is write-only; leaving it empty keeps it. Changing the base URL or kind needs the key again (the
  API answers `api_key_required`): explain why (a key only goes where it was given for).
- **Check now**, enable or disable, delete.

**API:** `GET /v1/provider-kinds`, `GET/POST …/providers`, `GET/PUT/DELETE …/providers/{provider}`,
`POST …/providers/{provider}/check`.

### 4.14 API keys (`/keys`)
**Purpose:** gateway keys (`pd_sk_…`) and what each may do: per app, team, CI job or developer.

**Shows:** name, key hint, created, last used, expires, and its gateway settings: developer, tags, allowed models,
requests per minute, default route. A key without settings may call every model, without a limit of its own.

**Actions:**
- **Create:** name, expiry; shown once with copy and the client snippet.
- **Gateway settings** (per key): allowed models (a trailing `*` matches a prefix, e.g. `claude-*`), developer (for
  attribution and per-developer budgets), tags, rate limit, default route.
- **Rotate:** the replacement keeps the settings; the old key works until it expires. **Revoke.**

**Plan:** per-developer keys on Team (§6: not enforced yet).

**API:** `GET/POST …/keys`, rotate and revoke (keel), `GET/PUT …/keys/{key}/gateway`.

### 4.15 Routes (`/routes`)
**Purpose:** what a call does when its model fails, and per-path caching. A call takes the route it names in
`X-Promptdam-Route`, else its key's, else `default`.

**Shows:** name (`default` first), the fallback chain drawn as model → model → model (any provider, either API format),
retries, response cache TTL, prompt caching (Anthropic breakpoints added), the router its calls go through.

**Editor:** name, fallbacks (ordered model picker), retries (0 to 3), cache TTL (off to a week), prompt caching,
router.

**API:** `GET/POST …/routes`, `GET/PUT/DELETE …/routes/{route}`.

### 4.16 Prices (`/prices`)
**Purpose:** costs and savings are only as right as prices. The project's own prices: negotiated discounts,
self-hosted models, a host's own model names.

**Shows:** model, provider (or any), input, cached input, cache write, output (USD per million tokens), updated. Beside
each, the price list's price for comparison (§6). A warning list of models seen in calls without any price.

**Actions:** set a price (replaces the same model and provider), delete (the price list's applies again).

**API:** `GET/PUT …/prices`, `DELETE …/prices/{price}`.

### 4.17 Project settings (`/settings`)
Name, project ID (copy), the gateway URL, and **Delete project**: everything Promptdam keeps of it goes (configuration
now, logs and caches by a background purge); type the name to confirm.

**API:** `GET/PATCH/DELETE /v1/orgs/{org}/projects/{project}`.

---

## 5. Organization pages (`/org/*`, shared Keel pages)

### 5.1 General
Organization name, ID, data region (read-only after creation), **Transfer ownership**, **Delete organization**.
API: `GET/PATCH/DELETE /v1/orgs/{org}`, `POST …/transfer-ownership`, `GET /v1/regions`.

### 5.2 Projects
The organization's projects with their calls and spend this month; create, rename. API: `GET/POST /v1/orgs/{org}/projects`.

### 5.3 Members
Members with their role, invitations pending (resend, revoke), invite by email with a role. The roles, explained in a
table on the page:
| Role | May |
|---|---|
| Owner | everything, including deleting the organization and transferring it |
| Admin | everything else: members, projects, keys, webhooks, read billing and the audit log |
| Developer | projects (providers, routes, routers, firewall, budgets…), keys, webhooks |
| Billing | billing and invoices only |
| Viewer | read every project and key list, change nothing |

API: `GET …/members`, `PATCH/DELETE …/members/{user}`, `GET/POST …/invitations`, `DELETE …/invitations/{invitation}`.

### 5.4 Billing and usage
- **Plan:** current plan, price, renewal date, a scheduled change; the plan comparison table (from the catalog) with
  what each plan adds for this organization ("Team: learned routing; your router's classifier would place 40% of calls
  on cheaper tiers").
- **Usage:** requests this period against the plan, per day; overage so far and its price (Pro $5, Team $3, Business
  $2 per extra 100k). Past periods.
- **Spend caps:** a cap on overage.
- **Invoices and payment method:** the Stripe portal.
- Actions: change plan (checkout), cancel, resume, preview the next invoice.
- API: `GET /v1/billing/plans`, `GET /v1/orgs/{org}/billing`, `…/billing/checkout`, `…/billing/plan`, `…/billing/cancel`,
  `…/billing/resume`, `…/billing/upcoming`, `…/billing/portal`, `…/billing/spend-caps`, `GET …/usage`,
  `GET …/usage/periods`.

### 5.5 Alert channels
Where alerts go: email, Slack, Discord, webhook URL. Add, test, remove. List which alerts use them: budgets thresholds,
`router.ready`, tier demotions, firewall refusals (hourly at most per project), the weekly firewall digest.
API: `GET/POST /v1/orgs/{org}/channels`, delete, test.

### 5.6 Webhooks
Endpoints (URL, events subscribed, enabled), the signing secret (reveal, rotate), a test message, deliveries with
status and **Replay**. Event types listed with their descriptions, Promptdam's included: `budget.threshold_reached`,
`budget.exceeded`, `router.ready`, `router.tier_demoted`, `firewall.blocked`.
API: `GET /v1/webhooks/events`, `…/webhooks` CRUD, secret, test, deliveries, replay.

### 5.7 Audit log (Team)
Who changed what and when: providers, keys, routers, firewall settings, budgets, members. Filters by actor, action,
time. API: `GET /v1/orgs/{org}/audit`.

### 5.8 Account (user menu)
Name, email, sign-in method; delete account (with a preview of what goes). API: `GET/PATCH/DELETE /v1/me`,
`GET /v1/me/deletion`.

---

## 6. What the API still lacks for these pages

To build or decide before or with the dashboard:
1. **Model picker:** a list of the models the project's providers serve, with prices and capabilities, for routers,
   rules, routes and policies. The gateway's `/v1/models` needs a gateway key; the dashboard needs a control-plane
   endpoint (`GET …/models`).
2. **Price list lookup:** the list price of a model, beside the project's own prices and in pickers.
3. **Ledger export** (Pro): CSV of the savings ledger for a range.
4. **Request log filters:** by firewall action, router, tier, cache, status; firewall events by trace (for the request
   detail).
5. **Activity feed:** one stream of what happened (router ready, tier demoted, budget thresholds, firewall refusals,
   provider checks failing) for the Overview's "Needs attention" and the sidebar badges. Today it must be pieced
   together from several endpoints.
6. **Overview in one call:** fine to compose at first; one endpoint later for speed.
7. **Router "watching" flag:** the router's output should say when the plan keeps an active router watching.
8. **Plan checks the API doesn't enforce yet:** prompt caching on routes (Pro), the cache doctor (Pro), a developer on
   key settings (Team), the ledger export (Pro), the audit log (Team).
9. **Alert rules** (spend spike, error rate, provider down; `alert_rules` in project.md §6.5): no API yet.
10. **Budget alert history:** when each threshold fired.
11. **Payload capture** (opt-in per project, redacted; project.md §4.1): not built; the request detail would show it.
12. **Per-project usage:** requests per project this period, for the Projects page and the plan meter's breakdown.

---

## 7. Flows across pages

- **Onboarding:** sign up → create organization (region) → first project → Get started (§4.1) → first call → Overview.
- **Upgrade:** a locked feature shows its value on the customer's own data → Billing (plan comparison) → checkout →
  back to where they were, now unlocked.
- **Router lifecycle:** suggested router created in shadow mode → the report fills with judged calls → `router.ready`
  (badge, alert) → Activate (Pro) → savings in the ledger → the canary demotes a drifting tier (alert, banner) → fix the
  model or rules → Restore.
- **A firewall refusal:** alert or `firewall.blocked` webhook → Firewall events (filtered) → the request (key,
  developer) → decide: keep blocking, add an allow policy for that key, or tokenize instead.
- **A budget threshold:** alert → Budgets (progress, who spends) → Analytics grouped by key or developer → raise the
  limit or block.
- **A failing provider:** fallback absorbs it (request log shows fallbacks) → Providers (check error) → fix the key.

# Promptdam dashboard: pages

> What the dashboard (`web/app`) shows and lets customers manage, page by page: the sidebar, its groups, and what each
> page must contain. Written from the customer's side: what an engineering lead, a platform or security engineer, or a
> small team opening Promptdam wants to see first, and what they come back to change.
> Every page lists the API it reads, so the dashboard is built on what exists; §6 lists what the API still lacks.
> Companion to `project.md` (§4 scope, §5 plans, §6 architecture). Written 2026-10-08, revised 2026-10-19: the frame
> follows Plugport's dashboard as built (§3), the firewall gains the model's labels (§4.11, round PD), coding agents
> get a page (§4.18), §6 lists what is really still missing and §8 the order to build in.
> The app is `web/app` (Nuxt 4 SPA on `@keel/auth-kit` and `@keel/app-kit`, as Plugport's); product components are
> prefixed `Pd` (`PdFormField`, `PdDangerZone`). `app/utils/pages.ts` builds the sidebar from §3.3 and must stay in step
> with this page; a page not built yet is a placeholder (`[...slug].vue`) showing its purpose from there.

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
- **Never the content, unless asked.** The dashboard shows metadata; prompts and answers only for projects that turn
  payload capture on, always masked. Firewall
  events show detector names and, only when the project turned excerpts on, masked excerpts. Say this where it
  matters (request detail, firewall); it is part of why customers trust the product. An organization in payload-free
  mode (§5.7) keeps no content at all: the settings that would keep some show locked, naming that page.
- **Empty states teach.** An empty page says what will fill it and how: a code snippet, a button, a link to docs.
- **Fresh enough.** Data pages refresh every 30 to 60 seconds while open and show "updated 20 s ago". Gateways apply
  configuration changes within seconds: say "Applied" after a save, not "Saved, deploy to apply".
- **Copy style:** short sentences, no em dashes (use commas, colons or full stops), no jargon the customer didn't
  bring (say "fallback", not "target chain").

---

## 3. The frame

Plugport's shell, as built (`products/plugport/plugport/web/app/layouts/default.vue` on `KeelAppShell`), with
Promptdam's items. One layout, `app/layouts/default.vue`.

### 3.1 Drawer
- 260 px, permanent on desktop, an overlay below `md` (hamburger). It collapses to a rail (remembered in
  `keel.nav.rail`): headings become dividers, items get tooltips.
- **Top (`#switcher`):** the project switcher (`KeelWorkspaceSwitcher`, noun "Project"), with "Create project" for
  roles that may (`PdCreateProjectDialog`: name, and what it is for: production, staging, coding agents, an
  internal tool). Projects are often environments; show the name and a colored mark.
- **Bottom (`#user`):** `KeelSidebarUserMenu`: Account, Sign out. Organizations are switched from Settings › Account,
  as in Plugport, until an org switcher in this menu is built.

### 3.2 App bar (56 px)
- **Left (`#crumbs`):** the logo (links to Overview), a rule, the breadcrumb from the sidebar tree.
- **Right (`#actions`), in order:**
  - `KeelHeaderActions`: the theme toggle; later the notifications bell (the activity feed's unread items), Docs
    (muzak.dev/openapi/ui for the API reference) and Help.
  - **Plan meter:** requests this month against the plan ("312k / 500k" and the plan as a tag), linking to Billing.
    Ink below 80%, amber past it, red past 100% (Free stops, paid plans run into overage).
  - **Connect** (primary): opens the snippet dialog of Get started's step 3 (gateway URL, a key to pick, a tab per
    client). Every developer needs it; hidden for billing-only members.
- **Search** (later): paste a trace (`req_…`, from `X-Promptdam-Trace`) to open its request.
- When the user's organizations fail to load: Plugport's amber banner with Try again, above the page.
- When calls are refused for the whole organization (Free's limit, the spend cap, an unpaid invoice): a red banner on
  every page with when calls resume and the one fix (§5.4).

### 3.3 Sidebar

Built by `navFor()` in `app/utils/pages.ts` with `defineNav` (`NavSection{heading?, rule?, entries}`). Only the
current page's trail is open; one row is lit. Items marked with a plan show a lock when the plan lacks the feature,
and stay clickable (§2).

| Section | Item | Route | Icon (material-symbols) | Plan | Purpose |
|---|---|---|---|---|---|
| (no heading) | Overview | `/` | `dashboard-outline` | all | Spend, savings, risk, what needs attention |
| | Get started | `/get-started` | `rocket-launch-outline` | all | The setup checklist; leaves the sidebar once the project is activated (§4.1) |
| | Activity | `/activity` | `notifications-outline` | all | What happened that was worth telling |
| **Traffic** | Requests | `/requests` | `list-alt-outline` | all | Every call: model, cost, savings, firewall action |
| | Analytics | `/analytics` | `monitoring` | all | Spend, latency, errors over time |
| | Families | `/families` | `account-tree-outline` | all | Prompt templates by spend: what each feature costs |
| **Savings** | Savings | `/savings` | `savings-outline` | all (export Pro) | The ledger, with proof |
| | Routers | `/routers` | `alt-route` | shadow all, active Pro, learned Team | Tiers of models, the cheapest that holds up |
| | Routing rules | `/rules` | `rule` | Pro | "Calls like this go to that model" |
| | Cache doctor | `/cache-doctor` | `cached` | Pro | Why prompt caches miss, and the fix |
| **Protection** | Firewall | `/firewall` | `shield-outline` | secrets all, personal data Pro, policies Team, patterns Business | What may not leave, and the proof |
| | Budgets | `/budgets` | `account-balance-wallet-outline` | Pro | Spend caps per project, key, developer, route |
| | Alert rules | `/alerts` | `notification-important-outline` | Pro | Spikes, provider errors and outages, slow answers |
| **Setup** | Providers | `/providers` | `hub-outline` | all | The project's own LLM accounts |
| | API keys | `/keys` | `key-outline` | all (per developer Team) | Gateway keys and what each may do |
| | Coding agents | `/agents` | `terminal` | all | Claude Code and the other agents through Promptdam (§4.18) |
| | Routes | `/routes` | `call-split` | all | Fallbacks, retries, caching |
| | Prices | `/prices` | `sell-outline` | all | The project's own prices |
| (rule) | Settings | `/settings` | `settings-outline` | all | Tabs (§5): Project, Organization, Members, Alert channels, Webhooks, Privacy, Audit log, Account |
| | Billing | `/billing` | `receipt-long-outline` | all | Tabs (§5.4): Plan and usage, Spend cap, Invoices |

Settings and Billing are drawn as one item each (`tabs: true`); their pages show a tab rail on the left
(`KeelSettingsTabs`, 196 px), each tab labelled with its scope: "This project", "Organization" or "You".

**Roles:** a billing-only member sees the rule section only, and no Connect button. Viewers see everything and no
edit buttons (a `KeelInfoNote` with `lucide:lock`: "You can see everything here, and change nothing").

**Sidebar marks**, each linking to its page (Plugport specifies them and hasn't built them; build them here):
- Routers: a dot when a router is ready to activate or a tier was demoted.
- Firewall: the number of calls refused in the last 24 hours.
- Budgets: the number past their first threshold this window.
- Alert rules: a red dot while one fires.
- Providers: a red dot when a check failed, or a disabled provider is still named by a route.

### 3.4 Page anatomy (Plugport's conventions)
- **Root:** `<div class="flex flex-col gap-6">`; a missing record is `KeelEmptyState` "Not found".
- **Header:** `KeelPageHeader` with a title (no icon) and a description that is the page's answer in a sentence, the
  key number in `font-medium text-ink` ("**$8,412** spent this month, **$3,950** saved"). Actions on the right: the
  main one flat primary, secondary ones tonal or text. The range picker (`KeelDateRangePicker`, presets 24h, 7d, 30d,
  90d, the API's maximum as `max-days`) sits in the actions; the range lives in the URL.
- **Notes:** `KeelInfoNote` for states: retention ("Your plan keeps 30 days of calls, so the range before 19 Sep is
  empty"), payload-free, read-only, "kept from your previous plan".
- **Lists:** `KeelListBar` (status tabs with counts, search, a filter menu, `KeelDataTableColumnsMenu`), then
  `KeelDataTable` in `<v-card border flat>` (page sizes 15, 50, 100), active filters as chips with Clear all.
- **Details:** rich records get a page (a router, a family); log rows open a right-docked panel, as Plugport's calls:
  `v-dialog :scrim="false"`, 34 rem, its state in the URL (`?request=req_…`, `?event=…`), so a link opens it.
- **Figures:** `KeelStatistic` tiles, `KeelMetricCard` for a series with a chart or table view (`v-model:view`),
  `KeelSparkline` in rows; colors from the theme's `chart-1…6`.
- **Forms:** `PdFormField` (label above, "Optional", one line for the hint or the error), outlined fields, never
  compact density or floating labels; `KeelSegmentedControl` for few short choices, `KeelChoiceList` for choices that
  need a sentence; code (CEL, regex, JSON) in `KeelCodeEditor`.
- **Saving:** settings that save explicitly use the floating save bar ("2 changes", Discard, Save, then "Saved");
  gateway settings apply within seconds, so say "Applied", never "deploy".
- **Dialogs:** 440 to 600 px, bordered body, Cancel then the action. A secret is shown once in a persistent dialog with
  `KeelCopyField` and "I've saved it".
- **Danger zone:** `PdDangerZone`, last on the page: what goes with it, then "Type its slug to confirm" (the slug,
  never the name), the button disabled until it matches.
- **Locked, not hidden:** the page stays; the header action becomes "Upgrade to Pro" (to Billing) with a warning note
  saying what the plan adds for this project, ideally with their number. A 402 `plan_required` becomes this state.
- **Empty states:** `KeelEmptyState` in a bordered card: what fills the page and the action (a snippet, a button).
- **Preview:** `?preview=` simulates states while building and reviewing (`new`, `loading`, `free`, `pro`, `team`,
  `viewer`, `billing`, `over-quota`), as Plugport's `useOrg`.

---

## 4. Project pages

Each page: **Purpose**, **Shows**, **Actions**, **Empty state**, **Plan**, **API**. Paths are under
`/v1/orgs/{org}/projects/{project}` unless they start with `/v1/`.

### 4.1 Get started (`/get-started`)
**Purpose:** take a new project from nothing to its first logged call in five minutes. "Activated" is 1,000 requests
within 7 days (project.md §9).

**First run** (before any page): the auth-kit `onboarding` layout (`keel.auth.onboarding: "/get-started"` in
`app.config.ts`), as Plugport's: `KeelSteps` "Your organization" (name, data region with `KeelChoiceList`) then
"Your first project" (name, what it is for). The guard sends a user there while the organization has no project.

**Then the checklist** (this page, and a `KeelLayerCard` "Get started" on the Overview with "2 of 5 done"), each step
ticking itself:
1. **Add a provider:** OpenAI, Anthropic, Gemini, Mistral, DeepSeek, OpenRouter, a cloud account (Bedrock, Vertex,
   Azure), or any OpenAI- or Anthropic-compatible endpoint. Inline form; checked on save with one tiny call.
2. **Create a gateway key:** shown once, with copy. Explain `pd_sk_…`.
3. **Point your client at Promptdam:** tabs with a ready snippet, the key and gateway URL filled in:
   - OpenAI SDK (Python, Node): `base_url` = the gateway's `/v1`
   - Anthropic SDK: `base_url`
   - Vercel AI SDK, LangChain
   - Google's SDK (google-genai): `GOOGLE_GEMINI_BASE_URL` (the gateway's `/gemini`) and `GEMINI_API_KEY`
   - AWS's SDKs and AnthropicBedrock: the endpoint `/bedrock`, the key as a Bedrock API key (bearer token)
   - Claude Code (from `docs/coding-agents.md`): `ANTHROPIC_BASE_URL` = the gateway's root, **without** `/v1`;
     `ANTHROPIC_AUTH_TOKEN` = the key; `ANTHROPIC_API_KEY` unset (Claude Code sends it first); for a team, the same in
     each developer's `~/.claude/settings.json` (`env`), with `ANTHROPIC_CUSTOM_HEADERS="X-Promptdam-User: <name>"`
     so analytics name the developer. A link to Coding agents (§4.18) for the rest.
   - curl.
   Below the tabs, one line: "If Promptdam can't be reached, your app can call the provider directly" with a link to
   `docs/fallback.md`.
4. **Send a request:** waits for the first call (polls the request log), then shows it: model, tokens, cost,
   latency. The "it works" moment.
5. **Next wins** (after the first calls), each a card with a button:
   - "Create a router from your providers" (the suggested router, in shadow mode: free, it only watches).
   - "Turn on prompt caching for the default route" (every provider: Anthropic's breakpoints, OpenAI's cache key,
     what Promptdam learned for the rest).
   - "Your secrets are already blocked": a link to the firewall, with a one-line test.
   - For a project made for coding agents: "Secrets are blocked: an agent's whole session stops at one. Redact them
     instead" (§4.18).

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

**API:** `GET …/overview` gives the page in one call (default this month): spend, the previous period, the
counterfactual and savings with a daily series, p95, cache use, the firewall's calls and refusals, the top models, keys
and families, "needs attention" most urgent first, and the latest activity. Beside it: `GET /v1/orgs/{org}/usage` (the
plan meter), `GET …/cache-doctor` and `GET …/routers/{router}/report` for the opportunities' dollar figures, and
`GET …/firewall/events?blocked=true&limit=5`.

### 4.2.1 Activity (`/activity`)
**Purpose:** "what happened while I wasn't looking?" One feed of what the organization's channels and webhooks were told.

**Shows:** newest first: time, severity, title and body, a link to what it is about (the budget, router or alert
rule), filters by kind (budget thresholds, router ready, tier demoted, firewall refusals, alert rules firing and
resolving). Kept 90 days.

**API:** `GET …/activity?kind=&subject=` (cursor-paged).

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

**Filters:** model, provider, route, key, developer, errors only, status (`429`, or `4xx`), error code, what the
firewall did (or any), router, tier, response cache hit or miss; time range (31 days at most per query). Saved filters
later.

**Actions:** open a row (detail drawer, §4.4); copy trace; export the page as CSV (later).

**Empty state:** "No calls yet" with the client snippet (§4.1, step 3).

**API:** `GET …/requests` (filters, `cursor`, `limit`), `GET …/requests/{request}`.

### 4.4 Request detail (right-docked panel, `?request=req_…`)
**Purpose:** everything Promptdam knows about one call, without its content.

**Shows:**
- **Summary:** status, error code and what it means (a sentence per code, with the fix: "Your key may not call this
  model: allow it in the key's settings"), time, trace (copy), API format, stream, translated (and the parameters
  translation dropped).
- **Where it went:** requested model, served model, provider, attempts, fallback used; for router calls: the router,
  the tier that served, the tier it decided, the step that decided (`held`: a new turn kept on its conversation's warm
  tier) and its confidence, shadow or not, the reasoning effort sent; the routing rule that matched. A link: "Explain
  how the router places a request like this". A healed call (`healed`, comma-separated) says why it was sent once
  more: drop_block or strip_thinking (the firewall's rewrite invalidated its thinking), unmarked (the provider refused
  the gateway's cache breakpoints; the model gets none from then on), unavoided (no backend was left once those that
  never cache were taken out).
- **Caching:** the strategy the gateway used (`cache_strategy`: markers or auto, `+session` when the conversation was
  kept on one backend; "trial" when the call's conversation
  tried the other one), the backend a router of providers sent it to (`backend`, e.g. OpenRouter's "Google AI
  Studio"), whether its conversation's previous call means it should have read from the cache
  (`cache_expected_tokens`) and what it read; its cost as the provider reported it (`cost_reported`) against the
  price list's (`computed_micros`).
- **Conversation:** whether the call started its turn or continued it (an agent's tool loop), the conversation's human
  turns so far, and how long after its previous call it came (`conversation`, `turn_kind`, `human_turns`,
  `chain_gap_ms`); a link to the conversation's other calls (filter by `conversation`).
- **Cost:** input, output, cache read, cache write and reasoning tokens; cost; counterfactual; saved, why and how it is
  known (`savings_basis`: provider, cache, replays or estimate). A model without a price (`priced: false`): "no price
  for this model: set one under Prices", no saving. A decision model's cost on the call, when one was asked.
- **Timing:** a small bar: gateway time, waiting on providers, TTFB, total.
- **Firewall:** the action and the detectors found (never the values); a link to the call's firewall events.
- **Who:** key (link to its settings), developer, end user, tags, route.
- **Family:** label or ID (link), and the cache doctor's verdict for it.
- **Payload** tab, for projects that keep payloads (developers and up; every read is audited): the request as the
  provider got it and the answer the client got (its text and tool calls; a failed call's error), with secrets,
  personal data and the project's own patterns masked, and what was cut (a request over 1 MiB, an answer over
  256 KiB). Projects that don't keep payloads see "Promptdam keeps no prompts or answers of this project: turn on
  payload capture to see them", with a link to the setting.

**API:** `GET …/requests/{request}`, `GET …/firewall/events?request=req_…` (the call's firewall events),
`GET …/requests/{request}/payload` (404 `payload_gone` once expired or deleted).

**Not returned yet** (§6.1): the request log keeps them, `RequestOut` doesn't carry them: the door, provider kind,
backend, target model, cache strategy and trial, pooled, the provider's reported cost, expected cache tokens, the list
price's cost, the router's and the decision model's cost and wait, the matched rule, the counterfactual, the payload's
presence, request and response bytes. Build the sections that need them with the fields hidden until they arrive.

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
- **How it's known** (`by_basis`): provider (the providers' own cache counts), cache (the response cache answered),
  replays (routing priced with what the default tier writes, measured on judged calls), estimate (the model asked
  for priced on the served call's tokens). Show replays and estimate apart: only the first is measured.
- **Per day:** cost and savings bars.
- **Top families** by savings.
- **How it's counted**, always visible: "For each call, the counterfactual is the model your client asked for, at its
  list price, without any cache. Savings are the difference. Calls that named a router are counted against its
  default tier. Router evaluation costs come off the savings." Link to a request's ledger line.
- **Monthly report:** a printable or PDF view of the month for finance (later).

**Actions:** range; export the ledger as CSV (Pro): a line per call with who, the models, tokens, cost, counterfactual,
saving and reason.

**Plan:** read on every plan; export on Pro.

**API:** `GET …/analytics/savings`, `GET …/analytics/savings/export?from&to` (text/csv, at most 31 days).

### 4.8 Routers (`/routers`)
**Purpose:** the core feature. "Send each call to the cheapest model that does it as well, proven on our own traffic."
Customers define their own tiers ("fast", "smart", "expert", or "easy", "decision", "hard", "reasoning"), any models of
any of their providers.

**List shows** one card per router:
- name, and how clients call it: `"model": "auto"`, `auto:fast` for a tier of their choice, or a route attached;
- **status:** shadow (watching: decides every call, serves the default tier, measures), active, disabled; "watching:
  your plan doesn't include active routers" after a downgrade (`watching` in the API), and whether the classifier and
  decision model place its calls (`learned`);
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
   Model pickers everywhere read `GET …/models`: the models the project's providers serve, with what each takes, its
   limits and prices; providers that take any name (a custom endpoint, OpenRouter) let the user type one.
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
  - per tier: the cost per call and the share of input read from providers' caches (`cost_per_call_micros`,
    `cache_rate`), and its calls by the model that answered them (`models`): the tier's own model first, then each
    fallback that served, with its calls, cost per call and cache rate. A fallback that answers often, or a tier whose
    cache rate is near 0%, is worth a sentence: "Gemini 3.8 Flash read 0% of its cache: see the cache doctor";
  - how calls were placed: explicit, rule, classifier, decision, default, held (new turns kept on a warm tier because
    switching would have cost more in cache than it saved);
  - the decision model (when the router has one): calls asked per outcome (answered, cached, timeout, error,
    unreadable), what it cost and how long calls waited (median, p95, p99). Warn when timeouts pass 5% of the
    questions: "The decision model answered too late for 8% of calls, which went to the default tier. Pick a faster
    model, or set a longer timeout in Settings.";
  - range picker (default 7 days, up to 90).
- **Quality:** each tier against the default tier: judged calls, wins, ties, losses, quality with its 95% interval,
  drawn against the floor line; meets floor or not; demoted tiers with **Restore**. Families judged most, with their
  quality. One sentence on method: "A judge model compares a tier's answer with the default tier's on sampled calls,
  in random order." The judge's flip rate (how often it disagreed with itself when asked both ways), and a warning on a
  tier whose `judge_leans` is set: "The judge is Anthropic's, and this tier's model is OpenAI's: verdicts may lean
  toward the default tier. A judge from a third vendor reads fairer."
- **Tiers and rules:** the wizard's editors (§4.8.1, steps 2 and 3), saved with PUT. Each tier may set a reasoning
  effort (none … max; empty keeps the client's): explain that the same model thinking less is often the cheapest tier
  that holds up, and that the effort is fitted to each model. Changing a tier clears readiness: say so before saving.
- **Classifier** (Team): version, trained when, per tier: threshold, held-out precision, coverage, AUC, samples.
  **Train now** (otherwise hourly when new judged calls exist).
- **Explain** (playground): paste a request body (OpenAI or Anthropic format), optionally a route, tier, tags, developer;
  see the tier it would serve and why (step, rule, confidence, unsure), what the call needs of a model (tools, images,
  files, JSON schema, context size), which tiers' models can take it, and the providers and models it would try, in
  order. No provider is called.
- **Settings:** status, default tier, quality floor, sample rate, evaluation budget, judge, decision model (decision models
  first, from the price list's decisions: "picks a tier, writes nothing, priced by input"; chat models after; decision
  models never offered for tiers, fallbacks or the judge) and timeout
  (Auto by default: "Promptdam measures your decision model and waits as long as its slow answers take, at most 3 s";
  or a fixed 20 to 3,000 ms);
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
**potential monthly saving**. Sorted by that saving. Each family's cache lifetimes (`prefix_ttl`, `tail_ttl`: 5m or
1h, learned from its calls' gaps; `kept_one_hour` says what the hour is worth), and calls written together
(`parallel_cold_writes`: calls that wrote the prefix within 10 seconds of another and read none of it, with the fix:
send one first). What every project's calls showed of the family's model, learned hourly and shown only when three or
more projects contributed (never one other customer's traffic):
- `no_breakpoints` also for OpenRouter models that cache only marked prompts (Gemini's, Anthropic's), when the calls
  carry none;
- `model_never_caches` (warning): across N projects the model read under 5% of what its cache should have served;
  prompt changes won't help, the fix is a model that caches for long conversations;
- `backend_never_caches` (info): OpenRouter backends (by slug) that never cached the model while another did; nothing
  to do, the gateway keeps calls off them unless the calls set their own provider preferences.
A stable prefix the cache never served is no longer shown as "Caching well" but as "The prefix is stable" with why
it may not read.

**Actions:** open the family; "Turn on prompt caching for route X" when the fix is the gateway's own (breakpoints, on
Anthropic or on an OpenRouter model that needs them), linking to the route.

**Plan:** Pro. On Free the API answers with each diagnosis `locked`: the family, hit rate, the findings' titles and the
potential monthly saving, without the detail and the fix. Show it as the upsell: "Fixing this would save ~$310/month".

**API:** `GET …/cache-doctor`, `GET …/families/{family}/cache-doctor`.

### 4.11 Firewall (`/firewall`)
**Purpose:** "nothing sensitive leaves without our say, and we can prove it." The page security engineers live on.
Since round PD (project.md, in progress) two detectors work together: **patterns** (exact shapes: keys, cards, IBANs,
national ID numbers of ~35 countries with their checksums) and **the model** (it reads the text: names, addresses,
dates of birth, health and the other special categories, secrets in formats no pattern knows, in ~45 languages).
The model reports everything it finds with its label; **the project decides per label what happens**, so changing a
setting never needs a retrained model, and applies within seconds.

Tabs:
- **Overview:**
  - what is protected now, one line per group: "Secrets: **blocked**. Personal data: **masked** (12 kinds).
    Special categories: **alert**. Your own patterns: none. Answers: scanned."
  - range stats: calls the firewall acted on, calls refused, findings per action, per detector and per source
    (pattern or model), a daily chart (calls and refusals);
  - recent refusals; a hint that `firewall.blocked` webhooks and alerts exist (link to Settings › Webhooks and Alert
    channels).
- **Events:** the table of findings: time, request (opens its panel), key, developer, route, model, detector, class,
  action, where (messages, system, tools…, the answer), count, refused or not and by what (class, policy, the scan),
  the source (pattern or model), the masked excerpt when kept. Filters: detector, class, action, location, key,
  source, refused only; 31 days per query.
- **Protection** (the settings form, with the save bar):
  - **Preset** at the top (`KeelChoiceList`): the use case sets the defaults below, which stay editable:
    - **Customer support** (default): names, contact details, addresses, IDs, cards and accounts masked; special
      categories alert; dates (except birth dates), cities, companies and amounts kept, since the assistant needs them.
    - **Healthcare:** as support, and health data masked.
    - **HR and finance:** as support, and salaries, performance reviews and financial details masked.
    - **Coding agents:** secrets redacted (never blocked: one block stops an agent's session), personal data in code
      and logs masked.
    - **Strict:** every label masked; a warning that answers lose dates, places and names they may need.
    - **Custom:** what the customer set by hand.
  - **The accordion** (`v-expansion-panels`, one panel per group, its title with how many labels are on: "Contact
    details · 3 of 3 masked"). In each panel, one row per label: its name, a sentence with an example ("Names of
    private people: Ayşe Yılmaz, Dr. Weber"), which detector finds it (pattern, model or both), and its action as a
    `KeelSegmentedControl` (off, alert, mask, tokenize, block; tokenize hidden where it can't apply). Groups:
    - **Identity:** person names, usernames, dates of birth.
    - **Contact:** emails, phone numbers, personal links.
    - **Location:** addresses, cities and regions tied to a person (exact coordinates proposed, §6.2).
    - **IDs and accounts:** national ID numbers (with the countries listed and their checksums), passports, tax and
      licence numbers, bank accounts and IBANs, cards (card security codes, device IDs and licence plates proposed,
      §6.2).
    - **Special categories** (GDPR Art. 9 and 10): health, religion, sexual orientation, political opinion, trade
      union, criminal record, ethnicity, biometric and genetic data. Alert by default: "masking them can change what
      the answer is about; turn masking on where your users share them".
    - **Secrets:** API keys, tokens, private keys, passwords, connection strings: block, redact or alert; tokenize is
      never offered ("a value an agent can write back would leave by another way"). Below it, **Find secrets in
      unknown formats** (the model; Free): on/off.
    - **Context the firewall never masks** (shown for clarity, off and locked): company names, job titles, amounts,
      public figures.
    Each panel has "Turn all on" and "Turn all off". A label's "Learn more" opens examples in several languages.
  - **Never mask** (the allow list): terms the firewall must leave alone, such as the company's own name, product
    names, a support agent's name ("Promptdam", "Ayşe from Support"). Up to 200.
  - **Your own patterns** (Business): name and regular expression (RE2), a test box, the action.
  - **Answers:** scan them (alerts only: what was written can't be taken back).
  - **If a request can't be scanned** (patterns): refuse it while secrets are blocked (default), always, or let it
    through. **If the model can't answer in time:** go on with patterns only (default) or refuse the call. Say what
    the wait is: "calls with new text wait for the model when an action masks or blocks; alert-only labels are checked
    after the call is sent, with no wait".
  - **Excerpts:** keep the masked text around each finding (off by default; a privacy note).
  - The whole firewall on/off, last, with a strong warning.
- **Policies** (Team): ordered rules, the first that holds decides: block, allow (cancels a class's block), or send to a
  model (which then serves alone, without fallbacks). A CEL editor with a variables panel: `request`, `findings`,
  `classes` (secret, pii, sensitive, custom), `locations`, `target`, `principal`, `org`, and ready examples: "no
  personal data to DeepSeek", "health data only to our EU provider", "the CI key may send secrets to our self-hosted
  model". Errors at their line and column.
- **Test** (playground): paste a request (any format) or pick a sample per language; see each finding with its source
  and the action it gets, the request as the provider would get it (a diff view), the policy that decides and the
  target. It tries the form's current, unsaved settings: "test before you save". The same call through
  `POST …/firewall/test` shows what an answer's restore would give back (tool calls included).

**Plan:** secrets (patterns and the model) and alerts on every plan; masking, tokenizing or blocking personal data and
special categories on Pro; policies on Team; your own patterns on Business. After a downgrade, saved settings keep
applying: "kept from your previous plan", editable only to remove.

**API:** today `GET /v1/firewall/presets`, `GET/PUT …/firewall`, `GET …/firewall/events`, `GET …/firewall/stats`,
`POST …/firewall/test`. Round PD adds (§6.2): the presets' label groups and use-case presets; in the settings a
`model` block (secrets, personal data, labels and their actions, the allow list, the model's fail mode) and the
`sensitive` class; `source` on events and stats. Until then, build the accordion from the presets the API serves today
(secret and customer-data detectors) and show the model's labels as "coming".

### 4.12 Budgets (`/budgets`)
**Purpose:** "no team, key or developer can blow the month."

**Shows:** a card per budget: name, scope (project, a key, a developer, a route), window (day or month, UTC), a
progress bar of spent against limit with the thresholds marked, the window's end ("resets in 9 h"), action (alert, or
block: the gateway refuses the scope's calls once spent), channels it alerts, enabled. Blocking budgets that are spent
show a red banner: "Calls of key ci-bot are refused until 00:00 UTC".

**Editor:** name; scope and target (pick a key or route, type a developer); window; limit in dollars; action; alert
thresholds (default 80% and 100%); channels (default all of the organization's).

**History:** each budget's alerts, from its activity (`GET …/activity?subject=bdg_…`).

**Plan:** Pro to create and edit; listing on every plan. A developer's budget needs per-developer keys (Team).

**API:** `GET/POST …/budgets`, `GET/PUT/DELETE …/budgets/{budget}`, channels from `GET /v1/orgs/{org}/channels`.

### 4.12.1 Alert rules (`/alerts`)
**Purpose:** "tell me when something is going wrong", before a budget or a customer notices.

**Shows:** a card per rule: name, kind, its condition in words ("provider errors above 10% over 15 minutes"), whether it
is firing now (and which providers look down), the last measured value against the threshold, last fired and
resolved, channels. Its history from the activity (`subject=alr_…`).

**Editor:** kind, then only the fields that kind uses, with its defaults filled:
- **Spend spike:** the last hour against the usual hour of the week before; how many times more (default 3×) and the
  least it must cost (default $5).
- **Provider errors:** the share of calls failing at the provider (rate limits, server errors, timeouts; default 10%)
  over 5, 15, 30 or 60 minutes, with at least so many calls (default 20).
- **Provider down:** one provider failing most calls (default 50%) over 5 minutes; empty provider watches each.
- **Slow answers:** p95 latency above a threshold (default 10 s).
- Optional provider and model; channels; cooldown before it alerts again while still firing (default an hour);
  enabled.

**Plan:** Pro. Rules are evaluated every minute while the plan includes them.

**API:** `GET/POST …/alerts`, `GET/PUT/DELETE …/alerts/{alert}`; webhooks `alert.fired`, `alert.resolved`.

### 4.13 Providers (`/providers`)
**Purpose:** the project's own LLM accounts (BYOK). Customers pay their providers directly; Promptdam never resells
tokens. Say this on the page.

**Shows:** name, kind (with logo), API format, base URL (regional or custom; a cloud's region, project and
location, or resource), key (set, with its last characters, or a service account's email),
models (the catalog's for its kind, or the listed ones), priority among providers serving the same model, enabled, last
check (passed when, or the provider's error).

**Actions:**
- **Add provider:** pick a kind (OpenAI, Anthropic, Gemini, Mistral, DeepSeek, Groq, Together, xAI, Fireworks,
  OpenRouter, or custom for any OpenAI- or Anthropic-compatible endpoint), name, key, base URL (custom or regional), models, priority.
  Checked on save.
- **A cloud account** (kinds with `cloud: true`: AWS Bedrock, Google Vertex AI, Azure AI Foundry) asks where the
  account is and how it signs in, instead of a base URL and key. One provider serves Claude and the cloud's own
  family, each through the cloud's API for it:
  - **AWS Bedrock:** the region (eu-west-1). Sign-in is a Bedrock API key, or an IAM access key id and secret (and a
    session token for temporary keys). Help: "AWS calls long-term API keys exploration-only; an IAM user with
    `bedrock:InvokeModel` on the models you use is the production choice." Current Claude models are called by their
    plain names (claude-haiku-5-5); Promptdam adds Bedrock's `anthropic.` prefix. Every other model is called by its
    Bedrock id, as the model's page in the AWS console shows it: `amazon.nova-lite-v1:0`,
    `us.meta.llama3-3-70b-instruct-v1:0`, `qwen.qwen3-32b-v1:0`, and older Claude by its region profile
    (`eu.anthropic.claude-sonnet-4-5-20250929-v1:0`). Help: "Some models need a region profile id (us., eu., apac.,
    global.): use the id the console lists for your region." Say which models cache: Claude and Nova do, through
    Bedrock; Llama, Mistral, DeepSeek and the others don't, so Promptdam sends them no cache markers.
  - **Google Vertex AI:** the project (taken from the key file if left empty) and location: global (default), eu,
    us, or a region such as europe-west4. Sign-in: upload the service account's JSON key file; the hint shows its
    email. Help: "Give the service account the Vertex AI User role, and enable the Claude models in Model Garden."
    Open models are called by their Vertex names (`meta/llama-3.3-70b-instruct-maas`,
    `deepseek-ai/deepseek-v3.2-maas`), Mistral's by theirs (`mistral-medium-3`); some live only in certain
    locations, so the help links Google's list.
  - **Azure AI Foundry:** the resource name ({resource}.services.ai.azure.com) and its API key. Models are the
    resource's deployment names: GPT deployments go to Azure OpenAI's API, deployments whose name contains "claude" to
    Foundry's Anthropic API. Help: "Keep the deployment names the models' own (gpt-…, claude-…) so routers and prices
    recognise them."
  - The check makes one tiny call to the first listed model (Claude Haiku if none; Azure lists its models) and says
    what went wrong in the cloud's terms: a missing permission, a model not enabled in that region, a key Google
    refuses. Show it under the form, with a link to the cloud's console page for the fix.
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

**Plan:** naming a key's developer (per-developer keys) needs Team; a developer already named stays after a downgrade.

**API:** `GET/POST …/keys`, rotate and revoke (keel), `GET/PUT …/keys/{key}/gateway`.

### 4.15 Routes (`/routes`)
**Purpose:** what a call does when its model fails, and per-path caching. A call takes the route it names in
`X-Promptdam-Route`, else its key's, else `default`.

**Shows:** name (`default` first), the fallback chain drawn as model → model → model (any provider, either API format),
retries, response cache TTL, prompt caching (on every provider: breakpoints where the provider wants them, OpenAI's
cache key, what Promptdam learned of the model), the router its calls go through.

**Editor:** name, fallbacks (ordered model picker), retries (0 to 3), cache TTL (off to a week), prompt caching,
router.

**API:** `GET/POST …/routes`, `GET/PUT/DELETE …/routes/{route}`.

### 4.16 Prices (`/prices`)
**Purpose:** costs and savings are only as right as prices. The project's own prices: negotiated discounts,
self-hosted models, a host's own model names.

**Shows:** model, provider (or any), input, cached input, cache write, output (USD per million tokens), updated. Beside
each, the price list's price for comparison (`list_price`). A warning list of models seen in calls without any price,
the busiest first, each with a "Set price" button.

**Actions:** set a price (replaces the same model and provider), delete (the price list's applies again).

**API:** `GET/PUT …/prices`, `DELETE …/prices/{price}`, `GET …/prices/missing`, `GET …/models` (list prices).

### 4.17 Settings › Project (`/settings`, scope "This project")
**Payload capture:** off by default. On, the gateway keeps each call's request and answer (a share of calls with the
sample rate; requests only if answers are off), always masked, sealed, and kept as long as the plan keeps logs. Say
plainly what is stored and for how long; "Delete all payloads" empties them now. API: `GET/PUT …/payloads/settings`,
`DELETE …/payloads`. In payload-free mode (§5.7) the switch is locked off; saving it on answers 409 `payload_free`.

Name, project ID (copy), the gateway URL, who has access, and **Delete project** in `PdDangerZone`: everything
Promptdam keeps of it goes (configuration now, logs and caches by a background purge); type its slug to confirm.

**API:** `GET/PATCH/DELETE /v1/orgs/{org}/projects/{project}`.

### 4.18 Coding agents (`/agents`)
**Purpose:** "our developers' coding agents go through Promptdam: their secrets don't leave, and we see what each
agent and developer costs." The page for platform and security leads rolling out Claude Code, Codex and the rest.
The next round (after PD) makes every major agent first-class; this page is built for it and fills as it lands.

**Shows:**
- **Agents**, one card each with its setup and its state in this project (calls in the last 7 days):
  - **Claude Code** (works today, measured: `docs/coding-agents.md`): the settings snippet (gateway root without
    `/v1`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_API_KEY` unset), per developer in `~/.claude/settings.json` with
    `X-Promptdam-User`, and a one-line check (`claude -p "hello"`).
  - **Codex, OpenCode, Gemini CLI, Antigravity, Cursor**: "coming in the next round", each with what will work then.
    Codex speaks the Responses API, which Promptdam passes through today without routing or caching: say so.
- **Recommended settings**, checked against the project: secrets redacted, not blocked (a block stops the agent's
  session); the project used only by agents; no router on agents' calls yet (a turn may switch models mid tool loop
  only where measured safe); prompt caching on. Each with a Fix button that opens the setting.
- **Developers:** calls, spend and secrets caught per developer this range (from the key's developer or
  `X-Promptdam-User`), linking to the request log filtered by them. Per-developer keys and budgets need Team: locked
  state with the upgrade.
- **Secrets caught in agents' traffic:** the last firewall events of this project, by detector ("12 AWS keys, 3
  database passwords this week, none reached a provider").

**Empty state:** the Claude Code snippet with the key picker.

**Plan:** all; per-developer keys and budgets Team.

**API:** `GET …/analytics/spend?group_by=developer`, `GET …/requests?developer=`, `GET …/firewall/events`,
`GET …/firewall`, `GET …/routes`, `GET/PUT …/keys/{key}/gateway`. Missing (§6.1): grouping and filtering by end user
(`X-Promptdam-User`), and which agent made a call (the gateway sees Claude Code's session header; it isn't logged as
an agent yet).

---

## 5. Settings and Billing tabs (shared Keel pages)

Settings (`/settings/*`) and Billing (`/billing/*`) are the sidebar's foot items, each with its tab rail (§3.3):

| Tab | Route | Scope | Section |
|---|---|---|---|
| Project | `/settings` | This project | §4.17 |
| Organization | `/settings/organization` | Organization | §5.1, §5.2 |
| Members | `/settings/members` | Organization | §5.3 |
| Alert channels | `/settings/alert-channels` | Organization | §5.5 |
| Webhooks | `/settings/webhooks` | Organization | §5.6 |
| Privacy | `/settings/privacy` | Organization | §5.7 (Business) |
| Audit log | `/settings/audit-log` | Organization | §5.8 (Team) |
| Account | `/settings/account` | You | §5.9 (and the organizations you belong to, to switch) |
| Plan and usage | `/billing` | This period | §5.4 |
| Spend cap | `/billing/spend-cap` | Overage | §5.4 |
| Invoices | `/billing/invoices` | At Stripe | §5.4 |

### 5.1 General
Organization name, ID, data region (read-only after creation), **Transfer ownership**, **Delete organization**.
API: `GET/PATCH/DELETE /v1/orgs/{org}`, `POST …/transfer-ownership`, `GET /v1/regions`.

### 5.2 Projects
The organization's projects with their calls, spend and savings this month; create, rename.
API: `GET/POST /v1/orgs/{org}/projects`, `GET /v1/orgs/{org}/analytics/projects`.

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
- **Spend caps:** a cap on overage, by default the plan's price; set it in dollars, see the requests it allows, or
  remove it.
- **When calls are refused** (Free's limit, the spend cap, an unpaid invoice past its grace), say it at the top of
  every page with when calls resume and the one action that fixes it (upgrade, raise the cap, pay the invoice): the
  gateway answers 429 `quota_exceeded` or `spend_cap_reached` meanwhile.
- **Invoices and payment method:** the Stripe portal.
- Actions: change plan (checkout), cancel, resume, preview the next invoice.
- API: `GET /v1/billing/plans`, `GET /v1/orgs/{org}/billing`, `…/billing/checkout`, `…/billing/plan`, `DELETE
  …/billing/scheduled-change` (keep the current plan), `…/billing/cancel`, `…/billing/resume`, `…/billing/upcoming`,
  `…/billing/portal`, `GET …/billing/spend-caps`, `PUT/DELETE …/billing/spend-caps/{meter}`, `GET …/usage`,
  `GET …/usage/periods`.

### 5.5 Alert channels
Where alerts go: email, Slack, Discord, webhook URL. Add, test, remove. List which alerts use them: budgets thresholds,
`router.ready`, tier demotions, firewall refusals (hourly at most per project), the weekly firewall digest, alert rules
firing and resolving.
API: `GET/POST /v1/orgs/{org}/channels`, delete, test.

### 5.6 Webhooks
Endpoints (URL, events subscribed, enabled) as an accordion, as Plugport's, the signing secret (reveal, rotate), a
test message, deliveries with status, one delivery's request and response, and **Replay**. Event types listed with their descriptions, Promptdam's included: `budget.threshold_reached`,
`budget.exceeded`, `router.ready`, `router.tier_demoted`, `firewall.blocked`, `alert.fired`, `alert.resolved`.
API: `GET /v1/webhooks/events`, `…/webhooks` CRUD, secret, test, deliveries, replay.

### 5.7 Privacy (Business)
**Payload-free mode:** one switch for the whole organization, for teams whose security review says no prompt or answer
may be stored. While it is on:
- no project keeps payloads, firewall excerpts or cached answers, whatever its settings say (they stay saved, inert);
- turning any of them on answers 409 `payload_free`: the project pages show those settings locked, with a link here.

Turning it on **deletes what was kept**: every project's payloads and cached answers. Say so in the confirmation
("Every project's kept prompts, answers and cached answers are deleted now"), with the organization's slug to type. Show who
turned it on and when. Turning it off needs no plan; a downgrade leaves it on. Admins and owners change it, every
change is in the audit log (`privacy.updated`). Below Business, show what it guarantees and the upgrade (402
`plan_required`).
API: `GET/PUT /v1/orgs/{org}/privacy`.

### 5.8 Audit log (Team)
Who changed what and when, from where: members and keys (Keel), and every change to a project: providers, gateway
key settings, routes, budgets, routing rules, routers (activations, restored tiers), the firewall, prices, family
names, alert rules. Filters by actor and action prefix (`router.`). On plans without it the API answers 402: show the
upsell. API: `GET /v1/orgs/{org}/audit`.

### 5.9 Account (user menu)
Name, email, sign-in method; delete account (with a preview of what goes). API: `GET/PATCH/DELETE /v1/me`,
`GET /v1/me/deletion`.

---

## 6. What the API still lacks for these pages

Built since 2026-10-08: the model picker, list and missing prices, the ledger export, the request log's and firewall
events' filters, the activity feed, the overview in one call, the routers' `watching` and `learned`, the plan checks,
alert rules, per-project usage, the audit log of every project change, payload capture, payload-free mode, cloud
providers, the doors, decision outcomes, tier effort and examples, turns and `healed`, `savings_basis`, the cache
doctor's learned findings.

### 6.1 Open (from the audit of 2026-10-19)
1. **Request fields:** `RequestOut` lacks door, kind, backend, target_model, cache_strategy, cache_trial, pooled,
   cost_reported, cache_expected_tokens, computed_micros, input_micros, router_micros, decision, decision_ms, rule,
   counterfactual_micros, payload_ref (a "payload kept" flag), request_bytes, response_bytes. All are in ClickHouse;
   the handler maps a subset (`handlers/analytics.go`).
2. **Request filters:** conversation, family, end user (`X-Promptdam-User`), tag, door.
3. **Analytics grouping** by end user and by door.
4. **Decision models:** `GET …/models` doesn't mark the models that decide (`llm.Model.Decisions`), so the router
   settings can't list them first.
5. **Learned caching:** no endpoint shows what Promptdam learned per model (strategy, backends kept off) beyond the
   cache doctor's findings.
6. **Ledger export:** no `savings_basis` or `priced` column.
7. **Agents:** which agent made a call isn't logged (§4.18).
8. **Docs drift:** `handlers/routes.go`'s description of `prompt_cache` still says Anthropic only.

### 6.2 Coming with round PD (the firewall model)
- `GET /v1/firewall/presets`: label groups with each label's description, examples and source (pattern, model); the
  use-case presets with their defaults.
- `GET/PUT …/firewall`: `model` {secrets, pii, labels with their actions, allow list, fail_mode} and `sensitive`
  (the special categories' class); plan checks: personal data and special categories Pro, the model's secrets Free.
- `firewall_events.source` and per-source stats; `POST …/firewall/test` returns the model's findings.
- The ~35 countries' ID numbers as detectors in the presets.
- Labels proposed on 2026-10-19 and waiting for the owner's yes: card security codes and expiry, device IDs, licence
  plates, exact coordinates (masked with personal data); immigration status, financial profile, employment and
  education records (alert, like special categories); age, gender, nationality and marital status (taught, never
  masked unless chosen). The accordion shows them only once they exist.

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

## 8. Build order

What to build first, so each evening ends with something that works end to end:
1. **Shell:** `default.vue` on `KeelAppShell`, `app/utils/pages.ts` from §3.3 (every page a placeholder at first),
   the project switcher, the plan meter, Connect, `?preview=`.
2. **First run and Get started** (§4.1): the onboarding steps, the checklist, the snippet dialog (Connect reuses it).
3. **Providers and API keys** (§4.13, §4.14): a project can't send a call without them.
4. **Requests and the request panel** (§4.3, §4.4): the first page developers open every day.
5. **Overview** (§4.2): one call (`GET …/overview`), the KPI tiles, the spend-vs-without chart, Needs attention.
6. **Firewall** (§4.11): Overview, Events and Test today; the accordion on today's presets, the model's labels when
   round PD lands.
7. **Savings and Routers** (§4.7, §4.8): the product's proof and its core feature.
8. **Settings and Billing tabs** (§5): mostly shared with Plugport's pages; copy and adapt.
9. **The rest:** Analytics, Families, Cache doctor, Rules, Budgets, Alert rules, Routes, Prices, Activity, Coding
   agents.

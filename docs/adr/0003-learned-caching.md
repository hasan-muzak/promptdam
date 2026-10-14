# 0003: Learn each model's prompt caching from every project's calls

- **Status:** accepted
- **Date:** 2026-10-14

## Context

Ten users' traffic through OpenRouter (2026-10-14) showed Gemini 3.8 Flash reading 0% of its prompt cache. Its
"mid" tier cost about as much per message as the cached GPT-6.1 Sol tier above it. Probes found two causes, and
neither is specific to Gemini:
- Gemini cached only when the request carried `cache_control` markers. Promptdam marked only Anthropic calls.
- OpenRouter spread calls over two backends. "Google AI Studio" cached every marked call (reads 90% cheaper);
  "Google" (Vertex) never cached. OpenRouter keeps a conversation on one backend only after a cache hit, or from
  its first call when the call carries a session id.

Thousands of models exist, and new ones ship every day. Testing each one, or writing code per model, can't keep up.

Options considered:
- **A hand-kept table of models that need markers:** wrong the day a model ships, and blind to backends.
- **Mark every call:** a write premium on models that cache on their own, and refusals from providers that reject
  the field.
- **Learn per model from traffic,** the way Promptdam already learns cache lifetimes and decision timeouts.

## Decision

- **Every call gets a cache plan:** a strategy (`markers`: the gateway's breakpoints; `auto`: the provider's own
  caching) and, on OpenRouter, the backends to avoid and whether to keep the conversation on one backend. A small
  table per provider kind says which strategies the kind takes and how a conversation stays where its cache is
  (`prompt_cache_key` on OpenAI, `X-Session-Id` on OpenRouter). Kinds are few and fixed; models are never named in
  code.
- **A session only where several backends serve a model.** OpenRouter gives each session a cache of its own on
  OpenAI's models: measured live, sending one from a conversation's first call stopped conversations sharing the
  instructions' cache (GPT-6.1 Sol read 71% of its input from cache instead of 87%). The learner counts each model's
  serving backends (at least 2% of its calls, avoided ones left out), and the gateway sends its session only when
  there are several. A client's own session passes either way.
- **Only providers' own public APIs teach or follow the profiles:** a project may point a known kind at a server of
  its own, and a custom endpoint always is one; their calls could teach every project anything. Calls to a kind's
  base URL are marked `pooled`; the others are planned by their price list alone.
- **Before anything is learned, the price list decides:** a model whose price list charges for cache writes caches
  what a request marks.
- **Every call is measured:** the backend that answered, the provider's own cost, cache writes, the strategy and its
  arm, and what the call should have read (`cache_expected_tokens`). A call should read when its conversation's
  previous call used the same model, kind, backend and strategy, within the cache's lifetime, with a prefix long
  enough to cache.
- **Learning is pooled across customers** (owner decision, 2026-10-14). Only counts and rates per kind, model,
  backend and strategy leave the pooled read, never content or project ids. A pooled figure is shown to a project
  only when three or more projects contributed. One file does the pooled read, and a source test lists every read
  across projects with its reason.
- **Experiments are allowed on up to 10%** (owner decision, 2026-10-14). While the strategy in use has under 300
  calls that expected a read, or reads under half, a tenth of a model's conversations try the other strategy. A hash
  of the conversation and model picks them, so a conversation keeps its arm on every replica with no coordination.
- **The hourly learner keeps a strategy until another is judged cheaper:** both need 300 calls that expected a read,
  and the challenger must cost under 0.9 of the incumbent per input token. Write premiums count, so a strategy that
  writes and never reads loses. A backend that read on under 5% of 100 such calls, while another backend read on
  half, is avoided by slug (`provider.ignore`, never `provider.order`, which would end sticky routing).
- **Errors switch a strategy off at once:** a provider that refuses the markers gets the call again unmarked. When
  that answers, markers go off for the model in every replica for seven days. A request with no backend left goes to
  any.
- **Tier fallbacks are first-class:** stickiness matches the target model (OpenRouter reports a dated name of its
  own), a turn that fell back keeps its warm fallback on plain routes too, fallback models are learned like any other,
  and the router report shows each tier's cost and cache rate with its fallbacks apart.
- **What is learned weighs prices:** a conversation's warm tier is held only for what its model actually reads; the
  cache doctor names models that never cache and backends kept off.

## Consequences

- A model released today is cached by its price list's hint, then corrected by its own calls within days. No code
  change, no test run per model.
- Pooled reads cross tenants by decision; the rollup keeps `project_id`, so deleting a project still erases its
  rows.
- A system string becomes a one-part array when marked. Anthropic and Gemini read both the same; the learner
  measures the rest.
- "Expected" is approximate: parallel calls, edited histories and unknown minimum prefixes blur it. The thresholds
  and the 0.9 margin absorb the noise.
- An avoided backend gets no traffic, so its evidence ages out within the week and it is tried again: a weekly re-check
  at a small cost.
- Costs in the ledger are the provider's own figures where reported (OpenRouter, BYOK included); the price list's
  figure stays in `computed_micros`.

## Addendum (2026-10-14, round D)

- **A conversation's previous breakpoint stays where it was.** Gemini finds a cache entry only at a breakpoint, and
  the gateway marked just the newest message, so each call read only the instructions. The previous call's last
  message now keeps its breakpoint too (three at most, with the instructions'). Measured: each Gemini call read
  everything the previous call sent (89% of a chat's input, against 42%); Claude through OpenRouter reads the same
  and writes only what is new.
- **Whether to keep a conversation on one backend is learned, not a rule.** A session is part of the strategy
  (`markers+session`, `auto+session`), offered where several backends serve a model, which is also where a new model
  starts with one. The learner weighs any number of candidates: each is judged on 300 calls, the cheapest takes over
  below 0.9 of the one in use, and the least tried unjudged candidate is the trial, on a tenth of conversations.
  Trying stops once every candidate is judged, not when the one in use reads well: a session's cost shows in
  conversations' first calls, which no read share sees.

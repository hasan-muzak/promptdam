# 0001: Route each request to a tier the customer describes, learned and verified on their traffic

- **Status:** accepted (replaces the per-family savings experiments of project.md §6.2 as of 2026-10-07)
- **Date:** 2026-10-08

## Context

Promptdam's first plan saved money per prompt family. A family (one task template) qualified after a week of traffic. A cheaper model was tried on a 5% sample and judged against the model in use. If it held up, a routing rule moved the whole family. Per-request routing (a classifier, then a decision model when unsure) and translation between the OpenAI and Anthropic formats came after launch (S6.3, S6.4).

The owner wants routing to be one of Promptdam's core features: organizations pick models by the kind of work ("this model for easy work, this one when a decision is needed, this one for hard tasks, this one when deep reasoning is needed"), across OpenAI, Anthropic, OpenRouter, Gemini and others. It must be flexible, fast, safe and accurate. Nobody can write down in advance exact conditions for "hard" or "needs reasoning".

Options considered:
- **Per-family experiments only** (the old plan): safe, but a family moves as a whole. Free-form traffic never saves anything, and customers can't express tiers.
- **Customer-written rules only:** precise where customers know (tags, routes, tools), but "hard" can't be written as a condition, and rules alone aren't checked for quality.
- **A hosted difficulty model on every request:** 70–500 ms and a bill per request, every prompt sent to one more vendor, and a generic notion of difficulty rather than the customer's.
- **Embedding nearest-neighbour routing:** an embedding call per request (latency, cost, one more vendor).

## Decision

- **Routers:** a project defines routers, each a list of tiers from cheapest to strongest (up to 8). A tier has a name, a plain-words description, a model with fallbacks on any provider, and example requests. Clients call a router like a model (`"model": "auto"`), or a route sends its calls through one.
- **A cascade per request,** cheapest step first: the client's explicit tier → requirement filters (tools, images, schema, context, from the price list's capabilities) → the router's CEL rules → a classifier in the gateway → an opt-in decision model when the classifier is unsure → the unsure tier (the strongest, or the model a call asked for). Failures fall back to the tier's fallbacks, then the next tier up.
- **Evidence, not assumptions:** a sample of each router's calls is replayed, in the gateway's memory and off the call's path, on the cheaper tiers. Each answer is judged against the baseline's: deterministic checks first, then a pairwise judge in random order. Requests are never stored; verdicts are kept with hashed features.
- **Learning:** the worker trains a logistic regression per tier on those verdicts. Thresholds are calibrated on held-out samples so that what each tier receives meets the router's quality floor. Gateways get the model with the project's configuration.
- **Shadow first, then active:** a new router decides and evaluates but serves the unsure tier until its report shows each tier's quality interval at or above the floor (or the customer overrides). While active, sampling continues and a tier that drifts below the floor is demoted on its own.
- **Customer keys only:** replays, the judge and the decision model run on the project's own provider keys, within a monthly evaluation budget per router. A Promptdam-paid judge, charged at provider cost, may come later.
- **Cross-format translation** (OpenAI Chat Completions ⇄ Anthropic Messages, streams included) moves into the MVP, so tiers, fallbacks and rules can name any provider.
- Prompt families stay: they feed spend views, the cache doctor, and the classifier as a feature.

## Consequences

- Customers describe what they want in their own words; boundaries come from judged outcomes on their traffic, with intervals.
- Most calls get their tier in microseconds. Only the classifier's unsure calls may wait on a decision model, and only when the router opts in.
- Evaluation costs the customer money. It is capped, shown, and subtracted from savings.
- The MVP grows: translation, routers, evaluation, the classifier, the decision model and the canary are S3 work (project.md §7). Per-family experiments are gone; routing rules stay for single-model changes.
- New stores: `model_routers` and `router_classifiers` in Postgres, `router_evals` in ClickHouse, router columns in `requests`. All are covered by the purge.
- The judge's quality bounds the router's. Deterministic checks, both-order judging on a sample, the customer's choice of judge model and visible example verdicts are the guards.

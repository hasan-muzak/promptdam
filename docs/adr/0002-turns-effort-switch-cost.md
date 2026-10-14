# 0002: Route conversations a turn at a time, with effort and the cache's cost

- **Status:** accepted
- **Date:** 2026-10-14

## Context

ADR 0001 places each request in a router's tier. Agents (Claude Code, Codex, any tool loop) send one human message and then many requests that continue it, each carrying the previous tool results. Placed one by one, a turn could change model mid-loop: the classifier read the last user message, which in a tool loop is a tool result, and rules over the whole request (`request.messages == 1`, an input size) stop holding as the loop grows. Each switch rebuilds the prompt cache on the new model (caches are per model) and drops the thinking the old model did (thinking blocks are bound to the model and the conversation that produced them).

A study of Caveman (a local proxy for coding agents with a closed router, 2026-10-14) showed three more gaps:
- The measured saving of routing coding agents comes mostly from running a strong model at a lower reasoning effort, not from switching models. Promptdam's tiers were models only.
- A router that ignores the cache can lose money: on a long conversation, rebuilding the cache on a cheaper model can cost more than the model saves.
- Prompt caching kept every entry five minutes. Conversations whose users answer after twenty minutes wrote their whole history again each time.

Options considered:
- **A conversation key from headers only** (session ids): exact where clients send one, absent for most API clients.
- **State keyed by the conversation's first message:** breaks on sliding windows and compaction, and collides on short first messages.
- **Per-message effort marks** (Anthropic's mid-conversation effort): change effort without a cache reset, but the gateway would have to re-insert each mark byte-identically on every later request, or the history counts as edited.
- **Keep-alive pings** (`max_tokens: 0` before a cache expires): spend customers' money on speculation and need stored request bodies.

## Decision

- **Turns.** The core's `wire.Turn` reads, in the request parse it already does, the latest human message (a user message with no tool result) and whether the request continues its turn. It also computes anchors: content hashes of the ask, the message before it, the previous ask and the first message. Anchors ignore what clients move or strip between requests (cache breakpoints, thinking blocks) and skip instructions. A turn's key hashes its anchors within the call's project, API key, end user, route, router and family, so every replica reads it off the request alone. Compaction starts a new chain, as it does at the provider's cache.
- **A turn keeps its tier.** The first call of a turn runs the cascade and stores its placement (per-replica memory, then Valkey; the first placement wins). Calls that continue the turn reuse it: the classifier and the decision model aren't asked, and the decision model is never asked mid-turn. Needs and rules may still move a call up, never down. A router changed mid-turn places the turn anew. Features, rules' `request.turn.*` and evaluation all read the turn, and only turn starts are sampled.
- **Effort is part of a tier.** A tier may set an effort, fitted to each model from the price list. The gateway writes it in the target's own words (Anthropic `output_config.effort`, OpenAI `reasoning_effort`, OpenRouter `reasoning.effort`), skips the model's default, and applies it to the tier's fallbacks and replays.
- **Switching must pay.** At a turn's start, the previous turn's record prices staying on the warm tier against the chosen cheaper one, plus the one-time rebuild. The router holds the warm tier when the saving over the turns ahead (at most four) is smaller than the rebuild. Up moves, clients' tiers and rules are never held. The output each tier writes is measured on judged replays.
- **Lifetimes are learned.** Every six hours the worker weighs each family's call gaps by cached tokens: returns 5 to 60 minutes later (what an hour keeps) against writes within five minutes and after an hour (what an hour makes dearer). It keeps an hour where that pays clearly. Gateways set the lifetimes; the conversation's tail gets Anthropic's automatic breakpoint.
- **Measured counterfactuals.** Routing savings are priced with what the default tier writes against the served one, measured on replays. Each saving says how it is known (`provider`, `cache`, `replays`, `estimate`).
- Not now: per-message effort marks, keep-alive pings, and holding parallel calls back to share one cache write. The cache doctor measures the last; it gets built if the measurement says it matters.

## Consequences

- Agents' turns stay on one model and one effort: their caches and thinking survive the tool loop.
- A Valkey read (8 ms at most, skipped while Valkey fails) on a replica that hasn't seen the turn; the parse costs about 7% more, and a second parse of every routed body goes away.
- Placements, holds, efforts, lifetimes and heals are all in the request log (`turn_kind`, `tier_source = held`, `effort`, `chain_gap_ms`, `healed`), so every decision can be checked against what it cost.
- Answers a gateway moved to another model name the model the client asked for; the served one is in `X-Promptdam-Served-Model` and the log.

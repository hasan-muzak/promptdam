# Performance

*What the gateway adds to a call, and how much it serves, measured on 2026-10-17 on a laptop. The production run on
the VPS is S4.6's first job; until then these are the numbers to quote, with the caveats below.*

## Summary

- **Overhead on ordinary calls: about 1 ms at the median, 5 to 6 ms at p99** on a quiet machine, with the whole path
  on: the key checked, the project's snapshot, quota and rate counters on Valkey, the firewall at its defaults, the
  router and the request log written to ClickHouse. The budget is 40 ms at p99 (project.md §6.1).
- **A coding agent's 600 KB call costs about 18 ms more** (p50; 33 ms at p99), almost all of it the firewall reading
  the body. Before this round's fix it was 113 ms (p50) and 421 ms (p99).
- **1,000 calls a second on one replica**: every call answered 200, the request log has one row per call and dropped
  none, the heap stayed under 280 MiB.
- **Not measured yet**: a 10-minute run behind Caddy, the step up to 4,000 calls a second, and Valkey or ClickHouse
  going down mid-run (scenarios b, d and e below). They need a quiet machine or the VPS.

## How it is measured

Two tools, both against a fake provider (`bench/fakeupstream`) that speaks the providers' APIs, so what is measured
is the gateway and not a provider's mood.

**The overhead gate** (`task bench`, `bench/overhead_test.go`) sends the same calls to the fake directly and through
the gateway, alternating, 32 at a time, and fails if the gateway adds more than 40 ms at p99. It runs twice: through
the gateway alone (a static route, its floor) and through the whole path on the test stack.

**The load test** (`task bench:load -- <scenario>`, `bench/load.sh`) runs production builds: the fake on :8299, two
gateway replicas on :8282 and :8283, Caddy on :8280 with the production Caddyfile (`deploy/Caddyfile`), and an
organization on the Team plan. Its generator (`bench/cmd/load`) is open-loop: calls leave at the rate asked whether or
not earlier ones have answered, so a slow gateway shows up as latency instead of hiding as a lower rate. Each call is
timed from when it was due, not from when it was sent. A fifth of the calls go to the fake directly at the same
moment, and the report gives what the gateway added per call shape: the gap between the two at the same percentile.

The calls are a mix of what a gateway serves most:

| Shape | Share | What |
|---|---|---|
| chat-stream | 60% | A support chat, streamed (OpenAI format) |
| agent | 20% | A Claude Code turn: tools, a cached system prompt, 12 tool results of source code, about 30 KB |
| chat | 15% | The same chat, not streamed |
| agent-large | 5% | A Claude Code turn deep in a session: 60 tool results, about 600 KB |

The fake answers after 200 ms and streams 20 chunks 20 ms apart, so about 640 streams are open at once at 1,000 calls
a second. The gateways serve pprof and counters (goroutines, heap, request-log rows dropped) on a loopback debug port
(`PROMPTDAM_GATEWAY_DEBUG_ADDR`), which the report samples.

| Scenario | What |
|---|---|
| a | One replica, no Caddy, 1,000 calls/s for 2 minutes |
| b | Both replicas behind Caddy, 1,000 calls/s for 10 minutes: memory and goroutines stay flat |
| c | 600 KB agent calls only, 50/s for 2 minutes, to one replica |
| d | Behind Caddy, 1,000, then 2,000, then 4,000 calls/s, a minute each: where it breaks, and what breaks first |
| e | Behind Caddy, 1,000 calls/s for 4 minutes, Valkey then ClickHouse stopped for 30 s each: calls go on |

## Results

The machine: an Apple M1 Pro (10 cores, 16 GB) running the generator, the fake, the gateways, Caddy and Promptdam's
Docker stack (in a VM shared with other products' stacks) at once, plus whatever else was open.

### The overhead gate

On a quiet machine (load average 5). Whole call, gateway minus direct:

| Call | Gateway alone p50 / p99 | Whole path p50 / p99 |
|---|---|---|
| Chat, streamed | +1.7 ms / +6.6 ms | +1.3 ms / +5.4 ms |
| Chat | +0.3 ms / +0.1 ms | +0.4 ms / +1.3 ms |
| Messages (Anthropic), streamed | +2.1 ms / +6.3 ms | +1.4 ms / +6.1 ms |

The whole path costs no more than the gateway alone, within the noise of a run: what it adds happens once per call
and is small next to streaming the answer, and the request log is written after the answer.

### Scenario a: 1,000 calls a second on one replica

On a busy machine (load average 20 to 50). First byte, gateway minus direct:

| Shape | p50 | p99 |
|---|---|---|
| chat-stream | +0.7 ms | +32.3 ms |
| agent | +1.7 ms | +30.6 ms |
| chat | +0.6 ms | +37.2 ms |
| agent-large | +20.4 ms | +103.2 ms |

- 120,000 calls, all answered 200; 96,009 through the gateway and 96,009 request-log rows; 0 dropped.
- Heap at most 279 MiB, 3,452 goroutines at the peak. The gateway used 84% of a core on average (204% at most).
- The tails come in bursts: by 10-second window, the p99 added was 15 to 53 ms, once 146 ms. The same machine was also
  running the generator (64% of a core), the fake (73%) and other work, so how much of the tail is the gateway is
  for the VPS run to say.
- The whole-call time of chat-stream read +21.7 ms at p50 in this run. That was the fake, not the gateway: the gateway
  asks providers for usage, and the fake waited a chunk interval before sending it. It now sends usage right after
  the last chunk, as OpenAI does.

### Scenario c: coding agents' 600 KB calls

50 calls a second to one replica. First byte, gateway minus direct:

| | p50 | p99 |
|---|---|---|
| Before this round | +113.5 ms | +421.6 ms |
| After | +17.7 ms | +32.7 ms |

## What this round fixed

- **The firewall's `secret_assignment` detector** ran its expression over every byte of a body, at 1.6 MB/s on source
  code, which a coding agent's calls are full of. It now searches only from the places its keywords start, and finds
  6.5 times faster with the same findings (checked against the old search on 20,000 generated texts and by fuzzing).
- **Request buffers**: a pool of buffers grown by large bodies handed a megabyte to every small call, which held it
  for its whole stream: 482 MB of a 542 MB heap at 1,000 calls a second. Bodies over 64 KB now get a buffer of their
  own size, and the heap fell to under 280 MiB.
- **The fake provider** kept every request it served, which grew without end under load (`Options.Forget`).
- **Caddy's health check** asks `/healthz`, not `/readyz`: `/readyz` fails when Valkey does, and the replicas share
  one Valkey, so one Valkey outage would have taken every replica out of Caddy at once.

What is left of a 600 KB call's cost is the firewall unescaping JSON strings and lower-casing them. Worth doing when a
customer's agents need it, not before.

## Re-running

```sh
task dev:stack && task migrate     # once
brew install caddy                 # once
task bench                         # the gate, a minute
task bench:load -- a               # or b, c, d, e; task bench:load -- run -rate 200 -duration 30s for a short one
```

The load test needs a quiet machine: at 1,000 calls a second the generator, the fake and the gateway together use
more than 2 cores, and anything else running shows up in the tails. Each run prints its report and where its logs are
(per-process CPU and memory every 2 s in `ps.log`).

On the VPS (S4.6), the generator and the fake run on a second machine, so that the gateway has the VPS to itself.

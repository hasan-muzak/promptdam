# Caveman: how it works, and what Promptdam should take from it

*Studied 2026-10-19 from a read-only clone of `github.com/JuliusBrussee/caveman` (Apache-2.0, about 113k stars), commit `bad48a0` of 2026-10-17, plus docs.caveman.so. Nothing from the clone was run, built or installed. Paths below are relative to the repository root. An earlier, shorter study of v4.0.1 (2026-10-14) led to ADR 0002; this one goes deeper and checks what changed.*

## 0. The short version

Caveman is two products under one name:

- **The open repo:** a local Go proxy (`proxy/`, listens on `127.0.0.1:8787`) that coding agents on one developer's machine are pointed at, plus a compression engine (`engine/`), a recovery store and MCP server (`engine/ccr/`, `mcp/`), memory (`mem/`), a browser bridge (`browse/`), prompt "voice" skills (`skills/`) and an installer for 30+ agents (`installer/`, `packages/cli/`).
- **Caveman Cloud (closed):** a hosted gateway "for the agents you build", plus the Auto router's decision service. `LICENSING.md` says the router, the eval and Autopilot service, the benchmark harness and the corpus exporter are not in the repo.

What actually saves money, by their own numbers:

| Rock | Mechanism | Their number | How solid |
|---|---|---|---|
| Read less | Rule-based compression of tool output in the newest message, originals kept locally | 33.2% fewer input tokens per session | One run, 18 pairs, 6 synthetic needle-in-noise files, harness not public |
| Auto | Cloud picks model and effort per human turn | 0.46x the cost of Opus 5.5 at xhigh | Interim, 85 tasks, one try each, ±7 points; most of the saving is lower effort, not another model |
| Cache warming | Zero-output replay before the cache expires, planned by optimal stopping | Replay simulation only | Code is complete and open |
| Say less | System-prompt style rules | 3% fewer output tokens than "Answer concisely." | n=10, single run |

The parts worth learning from are the engineering around prompt-cache stability (memoized, first-decision-wins compression of only the live zone; per-message effort marks re-inserted at hashed anchors; a harm tripwire), the cache warmer, and the honest evidence labels. The voice itself is nearly worthless for a gateway.

---

## 1. "Reads less": compression of what agents read

### 1.1 The pipeline (`engine/engine.go`)

`Engine.Compress(input, opts)`:

1. **Unwrap presentation layers** (`engine/filewrap.go`, up to 4, restored afterwards): Codex exec JSON lines (`engine/codexexec.go`), Read-tool line-number gutters (`engine/listing.go`; kept lines get their original numbers back), `<file>` wrappers.
2. **Detect** the type (`engine/detect.go`), in this order: JSON (or a pytest/Jest JSON test report), JUnit XML, HTML doctype, terminal (ANSI escapes or 3+ bare CRs), diff (`@@`, `diff --git`, plus 4+ diff-shaped lines), HTML without code signals, tabular (CSV/TSV/Markdown, 8+ rows), code (3+ keywords plus a structural signal), log (3+ level/timestamp lines and at least half the lines), search results (`path:line:` or URLs on a third of lines), YAML/TOML config (12+ lines), else text. Tool schemas, TOON, accessibility trees and repetition are only used when the caller forces the type.
3. **Count tokens** with tiktoken `o200k_base`, embedded and offline (`engine/tokens/tokens.go`), falling back to runes/4. Every count is labelled `inferred`.
4. **Mode gate:** the default mode is `record` (byte pass-through). Only `compress` transforms. Unknown modes fall back to `record`.
5. **Safety class gate** (`engine/safety/safety.go`): S0/S1 byte-safe, S2 reversible, S3 irreversible, S4 lossy with recovery. Every built-in compressor is S4, so a transform runs only if the recovery store is available.
6. **Not-smaller gate:** `if after >= before { pass through }`. There is no minimum savings percentage in the engine; each compressor has its own size floor, and the proxy skips blocks under 512 bytes (`minCompressBlockBytes` in `proxy/providers/{anthropic/splice.go,openai,gemini}/content_compress.go`).
7. **Store the original before returning anything** (`store.Put`). "A caller must never receive transformed bytes without a durable handle." A failed write sends the original.

### 1.2 The shared building blocks

- **Head and tail** windows per compressor (below).
- **Important-line regexes:** errors, warnings, fatal/panic, stack frames (`^\s+at\s`, `File "`, `.go:\d+`), `caused by`, `exit status [1-9]`.
- **BM25 against the latest human turn** (`engine/contextwindow/contextwindow.go`): k1=1.5, b=0.75, `idf = ln(1 + (N-df+0.5)/(df+0.5))`, lowercase tokens of 2+ characters minus 31 stopwords. A unit is kept when `score/maxScore >= 0.30`, up to a per-compressor top-k. The query comes from `proxy/internal/gateway/compression_query.go`: walk back to the latest `role:user` message and take its first non-empty **text** block (never a tool result, which is the thing being compressed), capped at 16 KiB.
- **Redundancy guard** (`engine/compressors/redundancy.go`): not exact dedup. Each unit becomes a token set (digits masked to `#` when the unit has letters). A dropped unit is "represented" if one kept unit contains at least 90% of its tokens (`redundantUnitContainment = 0.9`); otherwise it is kept as the first of its kind. After 128 such promotions the payload is treated as a document and kept whole, which then fails the not-smaller gate.
- **Invariant summaries** (`engine/compressors/invariants.go`) attached to each elision marker: constants (`all name=value`), small enums with counts (`status: ok×15 err×3`), numeric ranges (`range latency=12..940`), identifier coverage. Budget about a quarter of the elided bytes, capped at 160 bytes; values over 40 bytes or under secret-looking field names are refused.
- **Markers:** `… N lines elided (caveman) …` (or `rows`, `sections`, `context lines`, `search result lines`), and, once per payload when 4 KiB+ was dropped, a contract line telling the model the elided units resemble the shown ones and match the invariants, so it should answer from the view and make at most one broad `caveman_retrieve` call. Every marker is recognised on re-entry, so a second pass is a no-op.

### 1.3 Each compressor (`engine/compressors/`)

| Compressor | Floor | Keeps | Drops |
|---|---|---|---|
| `log.go` | 4 lines | head 2, tail 2, important lines, BM25 top 16 | the rest, with logfmt/NDJSON invariants in the marker |
| `terminal.go` | 4 lines | ANSI stripped, `\r` redraws collapsed to the last frame, head 3, tail 3, error lines (incl. `FooError`, `:12:3:`) | the middle; no BM25 |
| `text.go` | 1,600 bytes | head 2 and tail 2 paragraphs, headings, `ERROR|WARNING|IMPORTANT|NOTE|DECISION|…` paragraphs, BM25 top 12; fenced code is one unit; `<script>/<style>/<svg>` removed | other paragraphs |
| `diff.go` | 12 lines | every header, hunk, `+` and `-` line, ±2 context lines | unchanged context only |
| `searchresult.go` | 18 lines | head 8, tail 4, error/secret words, BM25 top 24 | other hits |
| `json.go` | arrays > 8 | all object keys; subtrees under `error/message/stack/trace/reason/detail/warning…` whole; per array: first 3, last 2, elements matching error words, **robust z-score outliers**, **CUSUM change points**, BM25 fill to 50 | other array elements, replaced by `{"__caveman_elided__":N,"__caveman_invariants__":…}` |
| `tabular.go` | 12 rows | header, first 3 and last 2 rows, error rows, the min and max row of every 70%-numeric column, BM25 top 24 | other rows (marker row inside the table) |
| `config.go` | 14 lines | head 3, tail 2, YAML top-level keys, risky words (`SECRET|TOKEN|ROLLBACK|…`), BM25 top 16, plus each kept line's parent chain and up to 8 children; output must still parse as YAML | other lines |
| `code_cgo.go` | must parse with no ERROR nodes | imports, signatures, types; function bodies become `{ /* caveman: body elided */ }`; output must re-parse | bodies |
| `html.go` | 200 bytes of text | Readability-style main container (scored by text length and commas, link-density discount, class/id hints) | everything else, no marker |
| `testreport.go` | | `TEST SUMMARY: N passed, N failed…`, then each failure with its message and 3 head + 2 tail stack frames | passing tests |
| `toolschema.go` (forced) | | names, types, `enum/required/default/const`, an 80-byte lead sentence and every constraint sentence (`must`, `at most`, `format`, `RFC`…) | `examples`, `title`, `$comment`, `$schema`, other prose |
| `toon*.go` (forced) | | lossless tabular re-encoding of uniform arrays of flat objects | nothing |

The **outlier and change-point logic lives only in the JSON compressor** (not in logs):

- Robust z-score: for every numeric leaf path (and a synthetic `__len__` of each element's canonical JSON), with 4+ values, `z = 0.6745·(v − median)/MAD`; flagged when `|z| ≥ 2.0` (`anomalyZ: 2.0`). If MAD is 0 it falls back to mean and `stddev/0.6745`.
- CUSUM: on the highest-variance numeric path present in every element (else element lengths), 8+ points, binary segmentation up to 8 splits, split at `argmax |Σ(y − mean)|`, accepted when the two sides' means differ by more than 2 robust scales (or both sides are flat and differ). Both elements at the seam are kept.

JSON output stays valid but is re-marshalled (keys sorted, whitespace gone). Re-serialisation alone is never counted as a saving.

### 1.4 How originals are stored and recovered (`engine/ccr/`, `mcp/`)

- **Store:** SQLite at `~/.caveman/ccr.db` (WAL, one connection), shared by the proxy, the MCP server and the CLI. Table `recoveries(handle PK, created_at, content_type, compressor, tokens_before, tokens_after, original BLOB, metadata BLOB)` plus `typed_objects` for the native runtime.
- **Handle:** `ccr_` + hex of the first 16 bytes of `sha256(original)`: content-addressed, so the same block always gets the same handle.
- **No TTL, no eviction:** "existing recovery rows are never evicted because an emitted transformed request may still reference any handle." A 512 MiB budget (`DefaultMaxStorageBytes`, enforced by `PRAGMA max_page_count`); once full, new blocks go out uncompressed.
- **What the model sees:** the proxy appends `<<ccr:ccr_…>>` after each replaced block (`appendCCRMarker`, `proxy/internal/gateway/proxy.go`).
- **Two recovery paths:**
  - *Server side* (`proxy/internal/gateway/retrieve_tool.go`): the proxy adds a `caveman_retrieve {handle, query}` tool to the request, answers the model's calls itself (at most 3 per request, 8 MiB cap, only handles minted for this request), and strips them from the answer. Non-streaming only, on OpenAI chat/responses, Anthropic messages and Gemini generateContent. On any other route the proxy does not compress at all.
  - *Agent side* (`mcp/engine_tools.go`): an MCP server with `caveman_retrieve(recovery_handle, query?, verify_only?)`, `caveman_compress`, `caveman_stats`, TOON encode/decode. Streaming and subscription sessions (the normal Claude Code case) depend on it; the CLI checks it is installed before compressing for that agent.
- **Query-narrowed recovery** (`engine/retrieve_query.go`): split the original into whole records (JSON objects, CSV rows with the header, paragraphs), BM25-rank them, return the top 20 in source order with `… [caveman: non-adjacent] …` at every gap. If nothing matches, return the whole original ("over-returning is safe, returning a fragment is not"). The gap marker exists because models attributed fields across records wrongly.
- **Never compress recovered content:** a tool result from any `caveman_retrieve` variant is excluded now and later as history (`proxy/providers/adapter.go IsRecoveryToolName`). The code cites an agent that re-read the same file seven times and got the same truncation.

### 1.5 How they keep prompt caching intact

This is the most transferable part (`proxy/providers/anthropic/frozen.go`, `content_compress.go`, `proxy/internal/store/prefix_cache.go`, `proxy/CLAUDE.md`):

- **Live zone only.** The frozen floor is one past the highest message index carrying a `cache_control`; the newest message is always live. Without any markers, only the latest user message is live. System prompt, tools and assistant messages are never compressed.
- **Replacement memo, first decision wins.** Table `prefix_replacements(original_sha256 PK, handle, replacement BLOB, created_at, last_used_at)`, LRU of 100,000 rows. A block seen again gets exactly the bytes sent last time. A block that once went out raw stays raw forever (a recorded "raw decision"). Frozen blocks may only be swapped for bytes the proxy already emitted for them. A memo eviction just means that block goes out raw from then on (one cache rebuild).
- **Byte splicing, never re-serialising** (`proxy/providers/jsonsplice/jsonsplice.go`): edits are made at byte spans so whitespace, key order and escapes of everything else stay identical.
- **Invariant and monitor:** "if request R repeats P's cached prefix in client bytes, R's forwarded bytes over that range must equal P's" (fuzz-tested in `cache_prefix_invariant_test.go`). `prefix_monitor.go` records each cache bust's cause: `client`, `caveman` (logged as a bug), `raw_retry`, `stream_switch`, `lever_freeze`.
- **Harm tripwire** (`ledger.go`): a transform earns a strike when the next request's `cache_creation_input_tokens` is above 50,000 and above 3x the session's mean; 3 strikes freeze that transform for the session.
- **Safety-net retry:** if a transformed body gets a non-rate-limit error, the original bytes are re-sent and that conversation is pinned raw (`raw_pin.go`).

### 1.6 Other "read less" pieces

- **Tool-output masking in the native runtime** (`proxy/internal/nativeruntime/runtime.go`, `afterTool`): for hosts that let a hook replace tool output (Claude Code `updatedToolOutput`, OpenCode, Pi), large outputs become `[Type] status: current … full: ccr://<id> recover: call caveman_retrieve …`, only when the output is at least 384 bytes longer than the mask and is not JSON, a table, a log or a test report (those go to the elision engine instead). Without the explicit "recover" line, agents made "27-97 flailing recovery calls" in their evals.
- **Tool catalogs** (`shrink/`, `src/mcp-servers/caveman-shrink/`): both compress tool *definitions*, not results. The Go one is the `toolschema` compressor with CCR recovery; the JS one is a stdio MCP wrapper that regex-strips filler words from descriptions (lossy, no recovery) and never touches `tools/call` results. `shrink/CLAUDE.md` says the gateway does not apply this to live traffic, because tool arrays sit in the cached prefix.
- **Browse** (`browse/`): an MCP server over Chrome DevTools that turns the accessibility tree into `[uid] role "name"` lines, focused by a query; raw tree in CCR. 121 tokens versus Playwright's 15,704 on a 200-row table with a query; without a query only 14.9% smaller; larger than Playwright on small forms.
- **Images** (`engine/image/`): a resize-to-768-px / `detail:low` planner gated on the query wording; the package says no production path calls it. **Pixel** (`engine/pixel/`): renders long text as PNG pages when image tokens plus cache-burn cost less than text tokens. Experimental.
- **Lossless tier** (Cloud docs, "optimizations"): where recovery cannot run (streaming, Responses, Gemini), a tool result that is a single JSON value becomes TOON (only if it decodes to the same value) or compact JSON, whichever is shorter. A user's own message is never rewritten.

---

## 2. Compaction and memory

Caveman has **no conversation summariser**. What exists:

- **Host compaction hooks** (`proxy/internal/nativeruntime/runtime.go`): `PreCompact` re-tiers stored session objects (task contract hot; decisions, repo map, evidence warm; the rest cold). `PostCompact` injects the task contract plus one line with counts and "exact recovery remains available through caveman_retrieve". Session end archives everything. The voice hook re-injects its rules after compaction (`src/hooks/caveman-activate.js` fires on `source=compact`).
- **cavemem** (`mem/`): explicit remember/recall only, nothing captured automatically. SQLite `~/.caveman/mem/mem.db`, `memories(id, text, valid_from, valid_until, supersedes, superseded_by)`, `id = mem_ + sha256(text)[:8]`, 256 KiB per memory. Recall is BM25 (k1 1.5, b 0.75) over all current memories, threshold 0.1, top 5, each hit compressed and packed into a 2,000-token budget; an oversized hit returns a head plus a handle. Note the recall is an O(n) scan per query.
- **`/caveman-compress <file>`** (`skills/caveman-compress/`): rewrites memory files (CLAUDE.md and similar prose files, never code or config, never paths that look like secrets) with one model call. Frontmatter is split off and code blocks are masked with `@@CAVEMAN_PRESERVED_CODE_<n>_<sha16>@@` before the call; the prompt forbids touching code, headings, URLs and paths. `validate.py` then checks headings (count, text, order), code blocks byte-identical, URL set, paths and inline code. Errors trigger at most one "fix pass" that sends the errors, the original and the compressed text with "ONLY fix the listed errors". The original is backed up outside the tree (`$XDG_DATA_HOME/caveman-compress/backups/…`, verified byte for byte) and written atomically. The 22.8% to 49.1% claim comes from five committed pairs; the benchmark script only counts tokens on those pairs.
- **`caveman learn`** (`proxy/internal/store/learn*.go`, `recurring.go`, `skills/caveman-learn/SKILL.md`): reads agent transcripts on disk and finds prose blocks of 180+ tokens recurring in 3+ sessions. The skill moves such a block from CLAUDE.md into cavemem and leaves a pointer, but only if pointer tokens plus recall cost per turn is below the block's cost, with a revert otherwise. It also audits duplicate rules, MEMORY.md truncation and broken imports.

---

## 3. "Says less": the voice and how it is installed

- **The rules** (`skills/caveman/SKILL.md`): answer first (`[thing] [action] [reason]. [next step].`), no ceremony or filler words, articles optional but never negations or numbers, ASD-STE100 sentences (20 words max), code/commands/paths/errors verbatim, no chatter between tool calls, the user's language, and plain prose for security warnings, irreversible actions, ordered steps, confused users and anything persisted (commits, docs, comments). Modes: `caveman`, `ultracave` (grammar stripped), `megacave` (Classical Chinese), plus one-shot `commit`, `review`, `compress`.
- **Claude Code** (`.claude-plugin/plugin.json`, `src/hooks/`): `SessionStart` injects the whole SKILL.md (about 1,160 tokens; "the old 2-sentence summary was too weak"), `SubagentStart` hands it to subagents, `UserPromptSubmit` detects mode switches and adds a ~90-token reinforcement **every turn**, `SessionEnd` records stats. Headless `claude -p` and Agent SDK sessions get nothing by default.
- **Other agents** (`installer/install.js`, PROVIDERS array): Codex via skills plus a `SessionStart` hook in `$CODEX_HOME/hooks.json` (re-runs on compact); Gemini CLI as an extension whose `GEMINI.md` imports six skill bodies; OpenCode with a native plugin using `experimental.chat.system.transform` plus an `AGENTS.md` block; Cursor and Copilot CLI with a `sessionStart` hook; Antigravity CLI as a staged plugin with `rules/AGENTS.md`; Antigravity IDE and 2.0 as copied skills; about 25 others via `npx skills add`; per-repo rule files via `src/tools/caveman-init.js`. All writes go through an ownership journal so uninstall removes only its own bytes.
- **Native pack** (`proxy/internal/nativepack/`, `skills/native/native-core.md`): what the CLI actually injects by default is not the voice but a ~556-token "Core" engineering policy (simplest complete system, reuse ladder, smallest proof, stop when done) at session start, plus one of six task overlays (`surgical-patch`, `lean-build`, `investigate-first`…) chosen by a regex on the prompt. Only the task type and a digest reach the proxy ("raw prompt text never crosses the native runtime protocol"). Built to be cache-stable: static Core first, dynamic tail last.
- **Native runtime** (`proxy/internal/nativeruntime/`): a Unix socket (`~/.caveman/run/native.sock`, 0600) receiving host lifecycle events; keeps a task contract, masks tool outputs, gives repo-map hints (`proxy/internal/repointel`, local BM25 over files and symbols, never sent anywhere).
- **Measured effect:** against an "Answer concisely." control, caveman cut output 3% at the median (ultracave 35%), n=10, one run, `evals/snapshots/results.json`. Their own outside citations say output is a tiny share of an agent's bill (LemonCrow: output −44%, total cost +0.06%). A Cursor user measured 4.3M tokens with caveman versus 1M without.

---

## 4. Model routing ("Auto")

- **Open vs closed:** the local proxy has no routing logic ("Cloud decides; this side reports facts and applies the answer", `proxy/internal/gateway/route.go`). It only triggers when the model is `caveman-auto`, on the provider's own API (Anthropic messages, OpenAI chat/responses).
- **What it sends** (`proxy/internal/cloudlink/link.go`, `packages/shared/contracts/schemas/route-ask-v1.schema.json`), `POST {gateway}/v1/route`, body up to 256 KiB:
  - `models` (static pools: `claude-opus-5-5`, `claude-sonnet-5-5`; `gpt-6.1-sol`, `gpt-6-sol`, `gpt-6-astra`, `gpt-6-luna`) and the user's other logins;
  - `signals`: agent, estimated context tokens (bytes times the session's measured tokens-per-byte), tools declared, tool error count, images;
  - `request`: endpoint, 11 raw agent headers (`x-claude-code-request-class`, `-compaction`, `-agent-id`, `x-codex-turn-metadata`, `x-openai-subagent`, `thread-id`…), tool names, requested effort and thinking, cache TTL;
  - `last`: previous served model, effort, age, input/cache-read/cache-write tokens, compacted;
  - **`ask`: raw text** of the latest human turn (128 KiB tail), the turn before (16 KiB), the end of the last assistant reply (16 KiB). SECURITY.md says this includes CLAUDE.md and memory reminders, @-mentioned files and shell output; on the Free plan Caveman may keep it.
- **Budget and fallbacks:** 800 ms (`routeBudget`), not sent with under 500 ms left, overlapped with compression. Failure runs the fallback (`claude-sonnet-5-5` or `gpt-6.1-sol`), except a transient failure mid-session runs the previous served model to keep its cache (`heldModel`). Pauses: 1 min after errors, 10 min after 401/403 or a billing limit, until next month when the free allowance is used. A connection keeper pings `/v1/route` every minute for 15 minutes after the last ask.
- **Stickiness:** decision key `sha256(scope, turn number, latest human text)`, scope = session, agent, endpoint, provider, model; cached 30 minutes sliding, 1,024 entries, concurrent waiters share one ask. A message group containing any tool result belongs to the tool loop, so the whole loop reuses the decision. A repeated "yes" is a new turn (the turn number changes). Compaction and side requests are asked fresh and never touch state.
- **Effort and per-message marks:** Cloud returns an effort and `effort_mode`. Top-level it maps to `output_config.effort`, `reasoning_effort` or `reasoning.effort`, fitted to each model's levels (`proxy/internal/translate/effort.go`). With `effort_mode: message`, the proxy inserts `{"role":"system","content":[],"output_config":{"effort":"X"}}` before the last user turn (Anthropic beta `mid-conversation-output-config-2026-07-01`) or a Responses `configuration_update` item, so effort changes without editing the cached prefix. Each mark is stored per session with an anchor `sha256(salt ‖ previous message without cache_control and thinking)` and re-inserted byte-identically on every later request; a broken anchor drops it and all later marks. Heals: a refused mark retries top-level and latches off; a thinking-signature 400 retries with `drop_block`, then strips thinking. There is no user-typed effort marker.
- **Other details:** a routed model that 4xxs is replayed on the asked model and banned from that scope for 30 minutes; the agent always sees the model name it asked for (rewritten in the stream) because Claude Code drops thinking on another name. Parallel subagents starting on the same new prefix wait up to 5 s behind one leader so only one cache write is paid (`route_cache.go`).
- **Cloud router (docs, guides/model-routing):** baseline per provider (provisional = most-used model of 30 days), price bands `auto:low…max`, task profiles with quality floor and stickiness per conversation or key, and route rules that backtest up to 4 candidates on 7 days of saved traffic (30+ pairs, 2-point margin), then ramp 5% → 25% → 50% → 98% with **2% always on the original model**, pause after 24 h without a verdict, roll back on a breach. Automatic hourly backtests with a proving budget of 10% of 30-day spend up to $250.

---

## 5. Prompt cache handling

- **Cache warming** (`proxy/internal/gateway/cache_warm.go`, `cache_warm_table.go/.json`): after a successful Anthropic call on `api.anthropic.com`, the exact upstream bytes are held **in memory** (LRU 1,024 streams, 256 MiB). A warm replays them with `max_tokens: 0`, `stream: false`. Skipped for `thinking.type: enabled`, forced `tool_choice`, structured output, no `cache_control`, or mixed TTLs.
  - Timing: `delay = min(0.9·ttl, ttl − 10 s)`, so 270 s for 5 minutes, 54 min for 1 hour; at most 60 minutes of warming; 2 warms in flight per process.
  - Cost model: `warm = read·P + input·T` (P = cached prefix tokens, T = uncached tail), `full = (write_ttl − read)·P`, `realized` = share of the miss actually paid by streams that came back.
  - **Optimal stopping:** backward induction over k warms with a survival function S(t) of "time until the next request": `window = (S(kd + ttl − d) − S(kd + ttl))/S(kd)`, `stay = S(kd + d)/S(kd)`, `value = −warm + full·realized·window + stay·v`; warm while value > 0, stop at the first no.
  - The return-time table: 30-second bins to 2 hours, classes `main|subagent × tool|end × 5m|1h` (whether the last answer stopped on `tool_use`), shipped from 364,048 transitions of one user's transcripts (main/tool: 99.4% return within 5 min; main/end: 58%, 11% never; subagent/end: 56% never), blended with the user's own gaps (prior weight 100).
  - Stops on any real request, any error, a warm that reads 0 cached tokens, 429 (honour reset), 5xx (pause 5 min). Warm costs are booked as spend, never as savings. Their replay claims net $1,896.65 saved for $385.31 of warms, versus $1,294.18 for a fixed rule.
- **Breakpoints** (`proxy/providers/anthropic/breakpoint_plan.go`): max 4. Without any client marker (pay-as-you-go only), mark the last tool, the system tail and a "frontier" on the newest message's last block. With client markers, never move or remove them; only fill a gap where markers sit on tools alone. They disabled Anthropic's 20-block lookback guard on purpose: a per-request guard writes somewhere new every turn and pays the 1.25x write each time.
- **OpenAI:** `prompt_cache_key` from a hashed session id when the client set none; the Cloud derives `sha256(model + tools + system + session or user)` and keeps each group under OpenAI's ~15 requests/minute overflow. **OpenRouter:** pin the provider after the first warm answer. **Gemini explicit cache:** retired.

---

## 6. The proxy

- **Pipeline** (`proxy/internal/gateway/proxy.go`, `server.go`): strip `/w/<agent>/` (attribution), assign ids, **authenticate before route matching** (so 401 vs 404 cannot list mounts), match an adapter (unknown path = 404, never blind pass-through), resolve credentials, read up to 32 MiB, strip HMAC-signed session markers that hooks put in user text, then (unless `record`) route ask in parallel, compression rewrite, provider-native transforms, breakpoint planner, effort; sign last over the final bytes; stream back through a usage tee.
- **Providers** (`proxy/providers/`): anthropic, openai, gemini (strict path allowlist), bedrock (Runtime and the Anthropic-wire Mantle), azureopenai, vertex (pass-through), openaicompat mounts. Bedrock re-signs with the proxy's own AWS credentials over the hash of the transformed body (`providers/bedrock/signing.go`), checking the caller's key id and region match. `translate/` converts between Anthropic Messages, OpenAI Chat and Responses in all directions (no Gemini grammar), and namespaces foreign reasoning as signed thinking blocks that are stripped before the real API.
- **Streaming:** passed through as it arrives; usage parsed from a 2 MiB tee buffer after the copy; the only stream rewrites are the model name and cross-grammar translation. A truncated copy aborts the connection so the client sees broken framing.
- **Pointing agents at it** (`agents/profiles/*.json`): Claude Code `ANTHROPIC_BASE_URL=http://127.0.0.1:8787/w/claude` plus `_CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL=1` (keeps the 1M context window behind a custom base URL, their issue #865) and `ENABLE_TOOL_SEARCH` (otherwise MCP schemas go inline, about 48k tokens); Codex via an ephemeral `CODEX_HOME` with `model_provider="caveman"`, `wire_api="responses"`, and a `/chatgpt/` path that forwards ChatGPT-login traffic to `chatgpt.com/backend-api/codex`; Gemini `GOOGLE_GEMINI_BASE_URL`; OpenCode and Kilo through inline config env; Aider `OPENAI_API_BASE`. Claude Pro/Max and ChatGPT **subscription logins pass through** as Bearer tokens. Cursor is not proxied.
- **Blocks** (separate repo `caveman-ai/blocks`, pre-release): repo-kept scripts with a PEP 723-style header (`.blocks/release-smoke.py`); a hook notices an agent retyping a script a block covers and says "use the block". Never denies or rewrites.
- **Middleware SDKs** (`packages/middleware/`, TS and Python): in-process adapters for the AI SDK, OpenAI, Anthropic, LangChain and more that send tool-result text to the local runtime for compression and register their own `caveman_retrieve` tool; where they cannot register it, they only record (`recovery_unbound`). `packages/sdk/` is a thin client for the hosted gateway.
- **Security:** loopback by default; a non-loopback bind needs a token, token map, OIDC or mTLS; SSRF guard on upstreams; secrets never in YAML; `gitsafe` neutralises hostile `.git/config`. With no token, any local process can spend the configured keys.

---

## 7. What leaves the machine

- **CLI telemetry, opt-out** (`packages/cli/src/index.ts`, `supabase/`): commands, agent, versions, OS, token totals, session counts, Blocks counts, a random install id, timezone, locale and the **IP** (nulled after 90 days; Supabase platform logs keep IP and geo longer, SECURITY.md admits). Off with `caveman telemetry off` or `DO_NOT_TRACK=1`.
- **Runtime events when signed in** (`proxy/internal/cloudlink/events.go`): per request, agent, provider, models, token counts, route outcome, trimmed by an org "data level" (off, counts, usage, decisions).
- **Auto asks** (section 4): raw human text plus the reply tail. A request Cloud routes to a Cloud model passes through Cloud whole.
- **Redaction** (`shared/platform/redact/`): `redact.go` scrubs logs and errors (vendor key prefixes, Bearer tokens, PEM, DSNs, labelled assignments). `payload.go` (email, Luhn cards, US SSN) and `residual.go` (high-entropy tokens) are for captured bodies before storage and export, but have **no caller in the open repo**: capture and corpus export are Cloud code. Their own comment: no EU or non-US personal data shape is covered.
- **Guardrails and budgets:** none locally. Cloud docs list hard and soft budget caps and mask/block guardrails; the open client only reacts to `billing_limit` and `allowance` answers.

---

## 8. Tests and benchmarks behind the numbers

- **33.2% input** (`docs/WRAP-BENCHMARK.md`): six synthetic 60-95 KB MCP fixtures (log, JSON, CSV, test output, YAML, HTML), each hiding one record; direct Claude Code vs Caveman vs Headroom, 3 reps (18 pairs), Claude Code 2.1.223 on `claude-sonnet-5`; metric = provider-reported `input + cache_read + cache_creation`, summed unweighted; exact-answer oracle (18/18). 95% interval 14.6% to 48.5% (case-clustered bootstrap). HTML lost 9.9% (no compressor, skill overhead counted). The harness lives in closed Caveman-Cloud; the run predates today's engine. The 98-99% "file saved" figures are the compressor alone, without a model.
- **0.46x cost** (`docs/AUTO-BENCHMARK.md`): 85 of 169 tasks from SWE-Interact and SWE-Together, one try each, simulated user on Sonnet 5.5. Opus 5.5 xhigh 44.3% solved at $7.83 per try; Auto (Claude pool) 43.7% at $3.59; difference −0.5 points [−6.8, +5.7]. Auto ran Opus at medium 87% of the time, Opus high 8%, Sonnet 5%. List prices, 17 tasks excluded without a reason, router closed. Their own caveat: against Opus at medium, expect much less.
- **−65% output:** a fixed ratio old releases applied without measurement; withdrawn. Today: 3% vs "Answer concisely." (n=10).
- **Engine fixtures** (`engine/evals/fixtures/manifest.yaml`): 18 fixtures with ratio bands (build log 0.85-0.95, JSON 0.70-0.90, terminal 0.80-0.97…), key-recall and round-trip checks; the "quality" model in CI is an echo stub.
- **Credit where due:** an honest-numbers page, red rows kept, a publication gate (6+ cases, 3+ reps, interval above zero, no case removed), and evidence rungs (`measured`, `inferred`, `observed`, `verified`) where "verified" exists only for cache markers they placed and for compression with a side-call count of the original (`provider_counted_baseline_delta`), negative values never floored.

---

## 9. What Promptdam should learn or adopt

Ranked by value. "Agents" = coding agents (Claude Code, Codex, OpenCode, Gemini CLI, Antigravity, first-class next round); "assistants" = organisations' customer assistants. Promptdam already has, since ADR 0002 and 0003: turns that keep their tier through the tool loop, effort as part of a tier, switching only when it pays for the cache rebuild, learned TTLs, a conversation-tail breakpoint, answers naming the asked model, thinking and caching heals. Those match or beat Caveman's local plumbing, so they are not repeated here.

**1. Coding-agent setup facts (agents: high; cost: a doc and a test).** Put into `docs/coding-agents.md` and the live checks for each agent:
- Claude Code behind a custom base URL may lose the 1M context window unless `_CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL=1` is set, and `ENABLE_TOOL_SEARCH` keeps MCP schemas out of every call (`packages/subagent-tax` measured 91 tools = about 43k tokens per call on one machine). Both are undocumented environment variables: verify with the real CLI before recommending.
- Codex: `model_provider` with `wire_api="responses"`; Gemini CLI: `GOOGLE_GEMINI_BASE_URL` (our Gemini door fits); OpenCode: `OPENCODE_CONFIG_CONTENT` provider `baseURL`; Antigravity: config paths in `installer/install.js`.
- Session and turn identity per agent: `x-claude-code-session-id`, `x-claude-code-agent-id` / `-parent-agent-id` (subagents), `x-claude-code-request-class` and `-compaction` (side and compaction calls), `x-codex-turn-metadata`, `x-codex-parent-thread-id`, `x-openai-subagent`, `thread-id`. Promptdam's `features/conversation` should read all of them, keep subagents as child conversations, and never let compaction or title calls change a conversation's placement.
- **Subscription logins:** Caveman passes Claude Pro/Max and ChatGPT-login Bearer tokens through. Many developers use those, and Promptdam's key currently takes the `Authorization` header. A key in a custom header (`ANTHROPIC_CUSTOM_HEADERS: x-promptdam-key: …`) with the subscription token forwarded would open Promptdam's firewall and attribution to them. Risk: the providers' terms on routing subscription traffic through a hosted third party must be checked first; ask the owner.

**2. Lossless tool-result re-encoding (agents and assistants: medium-high; risk: low).** Server-side and stateless: a tool result that is one JSON value becomes compact JSON or TOON when that is shorter and decodes to the same value; never a user's message. Apply it only in the live zone and memoise the decision (point 3) so later turns resend the same bytes. Needs no stored originals and no retrieve tool, so it fits payload-free mode. Risk: models read TOON less reliably than JSON on some tasks; start with compact JSON, measure TOON in shadow evaluation as routers are measured.

**3. The memoised live-zone discipline (prerequisite for any transform).** Caveman's best idea: transform only blocks after the last cache marker; keep `sha256(original) → replacement` and resend exactly the same bytes forever after, with "raw" as a remembered decision too; first decision wins across replicas. For Promptdam this is a Valkey hash per project (or per conversation) with a TTL longer than the longest cache lifetime, written only after the reassembled body is valid. Add their two guards: a **harm tripwire** (a transform whose next call writes 50k+ cache tokens and 3x the conversation's mean earns a strike; 3 strikes turn it off for the conversation) and **bust attribution** in the cache doctor (client vs gateway, so Promptdam can prove it never broke a cache). The firewall's redaction already splices bytes deterministically; the tripwire and attribution would cover it too.

**4. Lossy tool-output compression, opt-in (agents: high; assistants: medium; risk: high).** The engine's rules (head/tail, error lines, BM25 against the turn's human message, 90% redundancy guard, JSON z-score and CUSUM, invariant summaries in markers, parse-validated code and YAML) are deterministic, cheap and open; a Go port is days, not weeks. But for a hosted gateway:
- **Recovery** needs originals stored server-side, which contradicts "requests are never stored" and payload-free mode. If built: per-project encrypted store in the project's region, short TTL (hours, matching the conversation, not Caveman's forever), opt-in per route, excluded under payload-free mode.
- **Retrieve** must work while streaming, the normal agent case. Caveman needs a local MCP server for that. Promptdam could offer a remote MCP endpoint authenticated by the project key, or a server-side loop on non-streaming calls only.
- **Quality:** their evidence is needle-in-noise fixtures. Promptdam's own rule applies: shadow it on the customer's traffic, judge against uncompressed, publish per family. Keep their hard-won details: never compress recovered content, put an explicit "how to recover" line in every marker, return whole records with gap markers.
- **Savings proof:** a `count_tokens` side call on the original gives a provider-counted baseline ("verified"), as Caveman Cloud does.

**5. Cache warming with optimal stopping (agents: high; assistants: low to medium; risk: spends money).** ADR 0002 rejected keep-alive pings as speculation needing stored bodies. Caveman's answer to both: bodies held only in the replica's memory (bounded LRU), and warms planned from a measured return-time distribution so a warm is sent only when its expected saving beats its price. Note the arithmetic: at Sonnet 5.5 prices a warm costs about 0.1x input on the prefix while a miss costs 1.25x, so two or three warms beat a 1-hour write (2x) for a developer who comes back after 10 to 15 minutes. Promptdam already measures gaps per family (`features/cachettl`); the same data can drive a per-family warm plan, compared against the 1-hour TTL by the same cost model. Fit: the gateway replica that served the call holds the bytes; cancel on the next call of the conversation (any replica, through Valkey). Book warms as negative savings in the ledger, off by default, per project. Check that `max_tokens: 0` is accepted on each Anthropic surface (Bedrock, Vertex) before extending beyond the first-party API.

**6. Per-message effort marks (agents: medium; risk: medium, beta-dependent).** ADR 0002 deferred them because of re-insertion. Caveman shows a working design: store `{position, anchor, effort}` per conversation with `anchor = sha256(salt ‖ previous message minus cache_control and thinking)`, re-insert byte-identically, drop on a broken anchor, latch off on refusal. Promptdam's turn anchors are the same idea, so the cost is moderate. Value: changing effort between turns without a cache rebuild, which matters most once routers use effort tiers on agents. Depends on an Anthropic beta header and only Anthropic and Responses support it.

**7. Parallel-prefix leader/follower (agents: medium).** Already listed as "not now" in ADR 0002, gated on the doctor's measurement. Caveman built it: siblings with the same new prefix (`hash(parent, upstream, model, tools, system)`) wait up to 5 s for the leader's first content. Build it if the doctor's "calls written together" count is material for Claude Code subagents.

**8. Router rollout details (assistants: medium).** From Caveman Cloud's route rules: a permanent 2% holdout on the original model so live savings stay measured, a staged ramp (5/25/50/98%) with automatic pause and rollback, and a provisional baseline from the most-used model. Promptdam's shadow mode and canary already cover the evaluation; a live holdout would strengthen the ledger's "replays" basis with observed cost.

**9. Request-level control and receipts (both: low cost).** A single `x-promptdam-optimize: off | no-cache | no-route` header and `x-promptdam-optimize-applied` / `-denied` answer headers make every transform auditable per call, as Caveman's safety ladder does.

**Not worth adopting:** the caveman voice (3% over a one-line instruction, adds input every turn, and a gateway rewriting customers' system prompts is a trust breach); the native pack and task overlays (client-side, belongs in the agent); pixel mode and image downscaling (experimental, quality risk); sending raw turn text to a remote decision service (Promptdam's in-gateway classifier already avoids it).

### What Caveman does not do that Promptdam does

- **A central gateway an organisation controls.** Caveman's open part runs on each developer's laptop with the developer's own keys; nothing stops a developer from bypassing it. Promptdam holds provider keys centrally, so developers never see them.
- **A request firewall.** The local proxy blocks nothing; redaction is for logs and Cloud capture, regex-only, with no EU or non-US personal data. Promptdam blocks secrets by default, masks or tokenises PII with restoration in answers, and runs CEL policies.
- **Routing it can show.** Auto's logic is closed and fed raw conversation text; its quality claim is one interim benchmark. Promptdam's cascade runs in the gateway, the decision model is opt-in, and every tier is judged on the customer's own traffic with intervals and auto-demotion.
- **Budgets and enforcement.** None locally; Promptdam enforces per org, project, key, developer and route.
- **Learned caching across models and tenants** (ADR 0003), learned TTLs, and the cache doctor's explanations. Caveman marks a fixed set of breakpoints and retired Gemini's explicit cache.
- **Reliability:** fallback chains across providers, circuit breakers, retries with `Retry-After`. Caveman falls back only to the model the agent asked for.
- **Doors for Google's and AWS's own SDKs, a response cache, an audit log, SSO** in the open product.

### Risks to keep in mind

- **Quality:** every lossy transform changes what the model reads. Caveman's own HTML row and the Cursor 4x token report show transforms can lose money. Ship lossy features only behind shadow evaluation.
- **Prompt-cache breakage:** any transform not memoised per block will bust caches on the next turn; one bust on a 150k-token conversation costs more than many compressions save.
- **Security and privacy:** stored originals and in-memory warm bodies are customer payloads; they must follow payload-free mode, region and retention rules. A retrieve tool is also a new channel a prompt injection could ask for.
- **Spend:** warming spends the customer's money on a forecast; it must be visible per call and stoppable per project.

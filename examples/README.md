# Promptdam quickstarts

Point an OpenAI or Anthropic SDK at the gateway: change the base URL and use a
Promptdam key (`pd_sk_…`, with the gateway scope). Nothing else changes: the
gateway calls your providers with your own keys, so the answers, streams and
errors are theirs.

| SDK | Base URL | Key |
|---|---|---|
| OpenAI (Python, Node, Go, …) | `https://gateway.promptdam.dev/v1` (`OPENAI_BASE_URL`) | `OPENAI_API_KEY=pd_sk_…` |
| Anthropic (Python, Node, Go, …) | `https://gateway.promptdam.dev` (`ANTHROPIC_BASE_URL`) | `ANTHROPIC_API_KEY=pd_sk_…` |
| Claude Code | `ANTHROPIC_BASE_URL=https://gateway.promptdam.dev` | `ANTHROPIC_AUTH_TOKEN=pd_sk_…` |

Optional headers: `X-Promptdam-Route` (a route's name), `X-Promptdam-User` (your
end user, for analytics), `X-Promptdam-Tags` (comma-separated). Every answer
carries `X-Promptdam-Trace` (look the call up in the request log) and
`X-Promptdam-Served-Model` (the model that answered, after any fallback).

## Run them locally

```sh
task promptdam:dev:stack      # Promptdam's own Postgres, Valkey, ClickHouse
task promptdam:migrate
task promptdam:dev:fake       # a fake provider on :8299 (another terminal)
task promptdam:dev:gateway    # the gateway on :8282 (another terminal)
task promptdam:dev:seed       # prints the two export lines below
export OPENAI_BASE_URL=http://localhost:8282/v1 OPENAI_API_KEY=pd_sk_test_…
export ANTHROPIC_BASE_URL=http://localhost:8282 ANTHROPIC_API_KEY=pd_sk_test_…

./examples/curl.sh
cd examples/python && uv run openai_chat.py && uv run anthropic_messages.py
```

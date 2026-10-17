# Promptdam

Cut the LLM bill, block the leaks: Promptdam is a gateway in front of the
model providers, with the customer's own keys. It caches prompts, routes each
request to the cheapest model that does the job and keeps secrets out of the
prompts.

| Path | What |
|---|---|
| `server/` | API, gateway, worker and migrate (Go, module `github.com/hasan-muzak/promptdam`); every wire-visible name is in `server/brand` |
| `bench/` | The gateway's overhead budget and a fake model provider |
| `examples/` | Calling the gateway from curl and Python |
| `web/site/`, `web/app/`, `web/admin/` | The landing site, the client dashboard and the staff admin (to come) |
| `deploy/` | The server image |
| `project.md` | The plan |

## Coding agents (Claude Code)

Claude Code works through Promptdam: set `ANTHROPIC_BASE_URL` to the gateway's root and `ANTHROPIC_AUTH_TOKEN` to a
`pd_sk_…` key, and unset `ANTHROPIC_API_KEY`. Measured with the real Claude Code (`task demo:claudecode`): the same
cost and tokens as calling Anthropic directly, and about 0 ms added before the first byte.

**Know the firewall's limit with agents before relying on it.** An agent that reads a file holding a key sends that
key on every later call:

- `block` (the default) refuses that call and every later one in the session;
- `redact` keeps the key from the provider, but the agent can't edit the lines that hold it;
- secrets are never tokenized, on purpose (a prompt injection could get the real value sent out).

Give coding agents a project of their own with secrets on `redact`, and deny Claude Code reading `.env` files. The
whole story, with the numbers: [docs/coding-agents.md](docs/coding-agents.md).

It lives in the keel workspace at `products/promptdam/promptdam` and builds on
the core module (`github.com/hasan-muzak/keel-core`). The name is provisional:
the repository moves to the product's own organization once it is final.

```sh
task setup && task dev:stack   # .env, then Promptdam's own stack (ports 2xxxx)
task migrate
task dev:api & task dev:gateway & task dev:worker
```

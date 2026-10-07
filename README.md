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

It lives in the keel workspace at `products/promptdam/promptdam` and builds on
the core module (`github.com/hasan-muzak/keel-core`). The name is provisional:
the repository moves to the product's own organization once it is final.

```sh
task setup && task dev:stack   # .env, then Promptdam's own stack (ports 2xxxx)
task migrate
task dev:api & task dev:gateway & task dev:worker
```

# Promptdam plans

> The plans, their prices, limits and features, and what happens at their edges. The source of truth is
> `server/catalog/plans.go` (synced to Stripe with `api stripe sync`, exported to the pricing page with
> `api catalog export`); keep this page and project.md §5 in step with it. Written 2026-10-08.

**How Promptdam bills.** A subscription plus gateway requests. Customers bring their own provider keys (BYOK) and pay
their providers directly, as before: Promptdam never resells tokens and never takes a share of spend or savings. The
savings ledger proves what Promptdam saves; it doesn't set the invoice.

---

## 1. At a glance

| | **Free** | **Pro** | **Team** | **Business** |
|---|---|---|---|---|
| **Monthly** | $0 | $49 | $199 | $499 |
| **Yearly** (2 months free) | $0 | $490 | $1,990 | $4,990 |
| **Gateway requests a month** | 50,000 | 500,000 | 3,000,000 | 10,000,000 |
| **Past the requests** | refused until next month | $5 per 100k | $3 per 100k | $2 per 100k |
| **Request log and events kept** | 7 days | 30 days | 90 days | 1 year |
| **Members (seats)** | 3 | 10 | unlimited | unlimited |
| **Control-plane API calls a minute** (the org, all members and keys) | 600 | 2,400 | 6,000 | 12,000 |
| **In one line** | The gateway with fallbacks, caching and secrets blocking | Prompt caching, routing rules, model routers, budgets, alerts and PII redaction | Learned routing, firewall policies, per-developer keys, the audit log | Custom patterns, payload-free mode, DPA, SLA and SSO |
| **For** | Trying it, side projects | A product with LLM features | Several teams, coding agents | Companies with security reviews |

---

## 2. Features by plan

✓ included · a word says how much · empty: not on the plan

### Gateway
| Feature | Free | Pro | Team | Business |
|---|---|---|---|---|
| OpenAI- and Anthropic-compatible gateway, BYOK (OpenAI, Anthropic, Gemini, Mistral, DeepSeek, Groq, Together, xAI, Fireworks, OpenRouter, any compatible endpoint) | ✓ | ✓ | ✓ | ✓ |
| Calls translated between the OpenAI and Anthropic formats | ✓ | ✓ | ✓ | ✓ |
| Fallbacks and retries on rate limits, outages and timeouts | ✓ | ✓ | ✓ | ✓ |
| Exact response cache (per route) | ✓ | ✓ | ✓ | ✓ |
| Per-key allowed models, tags, rate limits, default route | ✓ | ✓ | ✓ | ✓ |
| Request log, spend, latency and family analytics | ✓ | ✓ | ✓ | ✓ |

### Savings
| Feature | Free | Pro | Team | Business |
|---|---|---|---|---|
| Savings ledger and report | read | ✓ | ✓ | ✓ |
| Ledger export (CSV) | | ✓ | ✓ | ✓ |
| Automatic prompt caching (Anthropic breakpoints) | | ✓ | ✓ | ✓ |
| Cache doctor | findings and their value | ✓ with the fixes | ✓ | ✓ |
| Routing rules (CEL: "calls like this go to that model") | dry-runs | ✓ | ✓ | ✓ |
| Model routers | shadow mode (decide, evaluate, report) | active (explicit tiers, rules, default tier) | learned (classifier, decision model) | learned |
| Router evaluation and quality report | ✓ | ✓ | ✓ | ✓ |
| Canary and auto-demotion of drifting tiers | | ✓ | ✓ | ✓ |

### Protection
| Feature | Free | Pro | Team | Business |
|---|---|---|---|---|
| Secrets blocked before they leave (keys, tokens, private keys, connection strings, `.env` values) | ✓ | ✓ | ✓ | ✓ |
| Personal data found and alerted on | ✓ | ✓ | ✓ | ✓ |
| Personal data redacted or tokenized (restored in answers) | | ✓ | ✓ | ✓ |
| Answers scanned for alerts | ✓ | ✓ | ✓ | ✓ |
| Firewall events, stats, weekly digest | ✓ | ✓ | ✓ | ✓ |
| Firewall policies (CEL: block, allow, send to a model) | | | ✓ | ✓ |
| Custom patterns (the project's own regular expressions) | | | | ✓ |
| Budgets per project, key or route (alert or block) | | ✓ | ✓ | ✓ |
| Budgets per developer | | | ✓ | ✓ |
| Alert rules (spend spikes, provider errors and outages, latency) | | ✓ | ✓ | ✓ |

### Teams and administration
| Feature | Free | Pro | Team | Business |
|---|---|---|---|---|
| Organizations, projects, roles (owner, admin, developer, billing, viewer) | ✓ | ✓ | ✓ | ✓ |
| Alert channels (email, Slack, Discord, webhook URL) and webhooks | ✓ | ✓ | ✓ | ✓ |
| Activity feed | ✓ | ✓ | ✓ | ✓ |
| Per-developer keys (a key's developer, for attribution and budgets) | | | ✓ | ✓ |
| Audit log (who changed what, from where) | | | ✓ | ✓ |
| SSO (SAML) | | | | ✓ |

### Support and compliance
| Feature | Free | Pro | Team | Business |
|---|---|---|---|---|
| Support | standard | standard | standard | priority |
| Payload-free mode guarantees | | | | ✓ |
| DPA | | | | ✓ |
| SLA | | | | ✓ |

---

## 3. Gateway requests: what counts, and past the plan

- **One call is one request**, whatever its size, model or tokens, retries and fallbacks included. Answers from the
  response cache count too: the gateway still served them.
- **Calls refused before they go anywhere don't count:** a bad or revoked key, a key's rate limit, a spent blocking
  budget, the firewall.
- **Free** stops at 50,000: further calls get 429 `quota_exceeded` until the month resets. Nothing is billed.
- **Paid plans** go on past their requests, billed as overage at the end of the period:

| Plan | Included | Overage | Per million past the plan |
|---|---|---|---|
| Pro | 500,000 | $5 per 100k | $50 |
| Team | 3,000,000 | $3 per 100k | $30 |
| Business | 10,000,000 | $2 per 100k | $20 |

- **Spend cap on overage:** by default, overage can cost at most the plan's own price per period (Pro: $49, up to
  980,000 extra requests). Organizations set their own cap (0 allows no overage, -1 removes it). Calls past the cap
  are refused like Free's.

---

## 4. Limits every plan shares

Sized so the gateway holds a project's whole configuration in memory. Raise them when a customer needs more.

| What | Limit |
|---|---|
| Providers per project | 50 |
| Routes per project | 100, each with up to 5 fallbacks |
| Routing rules per project | 50 |
| Model routers per project | 20, each with up to 8 tiers and 20 rules |
| Budgets per project | 100 |
| Alert rules per project | 20 |
| The project's own prices | 500 |
| Firewall policies, custom patterns | 50 each |
| Models a key may be limited to; tags per key | 200; 20 |
| Request body | 32 MiB |
| One request log or firewall events query | 31 days (stats 90, analytics 400) |
| Activity feed kept | 90 days |

---

## 5. Costs that aren't Promptdam's

- **The providers' bills.** Every call, evaluation replay, judge and decision-model call runs on the project's own
  keys and is billed by the provider.
- **Router evaluation** has a visible monthly budget per router (default $100), counted against its savings in the
  ledger. Shadow mode with evaluation runs on every plan for that reason: it costs Promptdam nothing.

---

## 6. Changing plans

- **Upgrades** (a higher plan, or monthly to yearly) apply at once; the prorated difference is charged now.
- **Downgrades** (a lower plan, or yearly to monthly) apply at the end of the period paid for, with nothing credited.
  Until then the plan stays. **Cancelling** keeps the plan to the end of the period, then Free.
- **Nothing is deleted on a downgrade.** What the lower plan lacks:

| Feature | After a downgrade |
|---|---|
| Firewall (PII masking, policies, custom patterns) | **Keeps protecting** as saved: a downgrade never loosens it. Saved parts can be kept or removed, not added to. |
| Budgets | Keep counting, alerting and blocking; new ones and edits need the plan. |
| Active model routers | Only **watch**: they decide and evaluate but serve the default tier, until the plan has routers again. |
| Learned routing (classifier, decision model) | Off for active routers; shadow routers keep it. |
| Routing rules | Keep applying; editing them needs the plan, deleting doesn't. |
| Prompt caching on routes | The setting stays; the gateway stops adding breakpoints. |
| Alert rules | Kept, not evaluated. |
| Per-developer keys | A developer already named stays; naming another needs the plan. |
| Cache doctor | Findings and their value, without the fixes. |
| Ledger export, audit log | 402 `plan_required` (the audit events are still written). |
| Logs and events | Each row keeps the retention of the plan it was written under; new ones get the new plan's. |
| Members past the seat limit | Stay; inviting more needs seats. |

---

## 7. Not in the plans yet

- **A share of savings** (a $500 base covering 15% of the first $3,333 saved, then 15%): deferred on 2026-10-07. The
  ledger stays as proof; revisit if large customers ask (project.md §5).
- **A judge paid by Promptdam** (for teams without an Anthropic or OpenAI key of their own), billed at what the provider
  charges us: later, if customers ask.

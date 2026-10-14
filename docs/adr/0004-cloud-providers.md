# 0004: Cloud accounts as providers: AWS Bedrock, Google Vertex AI, Azure AI Foundry

- **Status:** accepted
- **Date:** 2026-10-15

## Context

The owner moved the three clouds into the MVP (2026-10-15). Customers with cloud credits, enterprise contracts or
data-residency rules call Claude, GPT and Gemini through their own cloud accounts. Promptdam reached them only through
OpenRouter or a custom endpoint, and neither can sign AWS requests or mint Google tokens.

The clouds' current model APIs (checked 2026-10-14/15) mostly speak formats Promptdam already speaks:
- **Claude:**
  - on Bedrock ("mantle", Opus 4.7 and later) and in Microsoft Foundry: Anthropic's Messages API with standard SSE;
  - on Vertex: also Messages, but the model goes in the URL (`:rawPredict` / `:streamRawPredict`) and the body
    carries `anthropic_version`.
- **GPT and Gemini:** Azure OpenAI's v1 API and Vertex's endpoint for Gemini are OpenAI-compatible Chat Completions;
  so is Bedrock's API for its OpenAI-format models.
- **Sign-in differs:**
  - AWS: Bedrock API keys, or IAM keys signed per request (SigV4);
  - Google: service-account keys exchanged for hourly tokens;
  - Azure: an API key.
- **Bedrock's legacy InvokeModel** streams AWS's binary event stream. Only models up to Opus 4.6 need it.

Options considered:
- **A provider per cloud API** (Bedrock-Claude, Vertex-Gemini, …): one account entered several times, and its
  credentials with it.
- **The cloud SDKs:** they bring credential discovery that could pick up the server's own credentials (the AWS SDK
  reads `AWS_BEARER_TOKEN_BEDROCK` from the environment for every client), and none of them streams through a proxy.
- **One provider per cloud account, calling each model family through the cloud's API for it,** with hand-built
  requests and small authorizers.

## Decision

- **Three kinds, each one cloud account:**
  - `bedrock`: a region;
  - `vertex`: a project and a location (global, eu, us, or a region);
  - `azure`: an AI Foundry resource.

  The base URL is built from where the account is (no schema change), so a project can't point a cloud's credentials
  at another server. Development and tests may, with loopback allowed.
- **A model's family picks the API:** Claude goes to the cloud's Anthropic API, the others to its OpenAI-compatible
  one. Each cloud gets the model under its own name: Bedrock's `anthropic.` prefix, Vertex's `google/` for Gemini,
  Azure's deployment names. Calls are translated across formats as for any provider. A cloud serves the models of the
  vendors it hosts and lists them, priced at their vendors' list prices.
- **Credentials:**
  - Sealed as JSON in the provider's secret, one sign-in method at a time.
  - A service-account key must parse as RSA, and its `token_uri` must be Google's own, so a key file can't send its
    signed assertions elsewhere.
  - The hint is a key's last characters or the service account's email.
- **Signing is the last step of every call, over the exact bytes sent** (`cloudauth.Authorizer`), in the proxy (a
  heal's resend is signed again), the decision model, evaluation and the provider check:
  - Bedrock API key: `x-api-key` on mantle, a bearer token on runtime.
  - SigV4: AWS's own signer, with the service and region read from the host.
  - Google tokens: an RS256 JWT signed with the key, exchanged through safehttp, cached per key across snapshot
    rebuilds, refreshed five minutes early, one fetch for all waiting calls.
  - Azure's key: a fixed header.
  - A call its credentials can't sign fails as the provider's (`provider_auth_failed`), never sent unsigned.
- **Caching:**
  - Claude on a cloud gets breakpoints on the tools, the system prompt, the last message and the previous call's last
    message: four, Anthropic's most. Anthropic's top-level automatic breakpoint is first-party only.
  - Azure OpenAI takes `prompt_cache_key`. Gemini on Vertex and Bedrock's OpenAI-format models cache on their own.
  - Bedrock's and Vertex's calls teach the pooled caching profiles (models are named the same for everyone); Azure's
    don't (a model there is a deployment the project named).
- **The provider check:**
  - Azure's key lists its resource's models.
  - Bedrock and Vertex answer one token from the first listed model, so a missing permission or a model not enabled
    in the region shows at setup.
- **Not now:**
  - Bedrock's legacy InvokeModel and its event stream;
  - Azure Entra, AWS role assumption and Google workload identity (keys first);
  - Claude Platform on AWS;
  - the clouds' +10% regional price premium.

## Consequences

- A customer adds one provider per cloud account and calls Claude, GPT or Gemini by their usual names; routers,
  fallbacks, caching, the firewall and the ledger work as for any provider.
- No cloud SDK runs in the gateway; the only new dependency is AWS's SigV4 signer, already in the core.
- Signing adds no I/O to a call except the hourly token fetch, which concurrent calls share.
- The clouds' own model listings and regional prices aren't read: a model a cloud names differently (an Azure
  deployment called "prod-chat") needs the provider's model list, and is priced only if its name is the model's.
- Verified against fake clouds that check paths, bodies and signatures; live checks (`task demo:clouds`) run when
  the owner's test accounts are in `.env`.

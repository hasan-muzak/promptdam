# 0005: Doors: Google's and AWS's own APIs for clients

- **Status:** accepted
- **Date:** 2026-10-15

## Context

A customer moves an app onto Promptdam by changing its SDK's base URL and key. That worked for OpenAI's and
Anthropic's SDKs only. Two large groups had to rewrite their AI calls first:
- **Google's SDKs** (google-genai for Python, JS, Go and Java; Vercel's `@ai-sdk/google`; LangChain's Gemini
  client) speak Gemini's own API: `models/{model}:generateContent`, its stream, `countTokens`, embeddings.
- **AWS's SDKs** (boto3, JS v3, Go v2, Java v2, .NET) and Anthropic's `AnthropicBedrock` speak Bedrock's runtime
  API: Converse and InvokeModel, streaming in AWS's binary event stream.

Every one of these SDKs takes a custom base URL and keeps its path (checked in their sources, 2026-10-15).
- Google's send the key as `x-goog-api-key`.
- AWS's send a Bedrock API key as a bearer token (boto3 1.39, JS 3.840, Go bedrockruntime 1.35, Java 2.31.74,
  .NET 4; AnthropicBedrock Python 0.88, TS 0.27) and don't check its format.

Options considered:
- **Make each a first-class format of the gateway** (parsing, metering, firewall, routers, payloads, evaluation
  for Gemini and Converse). Native, but every component would learn two more formats.
- **Pass them through to the matching provider only,** as the Responses API is. No routing, no translation: a
  Gemini SDK could only reach Gemini.
- **Convert at the edge** into a format the gateway speaks, and every answer back.

## Decision

- **Doors convert at the edge.**
  - `/gemini/` serves Gemini's API, turned into Chat Completions.
  - `/bedrock/` serves Bedrock's runtime API, turned into Messages.

  Routers, fallbacks, prompt caching, the firewall, the ledger, payloads and evaluation work as for any call, and
  any model the project serves is reachable from either SDK.
- **The pivots:**
  - **Chat for Gemini.** Gemini models are called through Google's OpenAI-compatible API, so a Gemini call to Gemini
    is one conversion away. Chat also carries Gemini 3's thought signatures (`extra_content.google`); Messages has
    no place for them.
  - **Messages for Bedrock.** Converse maps onto it almost field for field (the mirror of `features/converse`), and
    InvokeModel's Claude body is Messages.
- **Answers:** a writer around the response converts whatever the gateway writes:
  - whole answers and errors once complete;
  - streams event by event: Gemini's server-sent events or JSON array, AWS's event stream for Converse or
    InvokeModel.

  The request log keeps the internal format (`api_format`) and names the door (`door`).
- **Never silently dropped:** what the pivot can't express is refused before any provider is asked, naming it:
  - Google's server tools, its context cache, files uploaded to Google, video;
  - safety settings stricter than the default (Promptdam can't apply them to other models);
  - Bedrock guardrails, S3 files, Office documents, other vendors' own InvokeModel bodies.

  Tuning without an equivalent is listed in `X-Promptdam-Dropped`.
- **Sign-in:**
  - Gemini: `x-goog-api-key`, or `?key=`.
  - Bedrock: the Promptdam key as a Bedrock API key. SigV4 is refused with a message saying so: verifying it needs
    the key's secret, which Promptdam never keeps (only an HMAC). The docs tell customers to set the key on the
    client, as `AWS_BEARER_TOKEN_BEDROCK` reaches every Bedrock client in a process.
- **Limits:** a Messages call without a limit (Converse needn't name one) gets the target model's own, as
  translated calls do.
- **Counting tokens:**
  - Google counts for Gemini, natively, as the OpenAI-compatible API has no count. The request goes as the client
    sent it, so not when the firewall changed it.
  - Anthropic counts for Claude.
  - Other models are refused.
- **Gemini 3 signatures:** a tool call in the current turn without one gets the value Google documents for
  histories other models wrote. Doors make it common (a router moving a conversation onto Gemini 3), but every
  client gains from it.

## Consequences

- Google's and AWS's SDKs work by changing the base URL and key, against any provider and through routers, with
  Promptdam's caching, firewall and ledger.
- Each door is two pure translators (`features/gemini`, `features/converse`) and one writer; the hot path of the
  gateway's own APIs is unchanged.
- **Google's and Bedrock's own features are refused, not passed through:** Search grounding, code execution,
  guardrails. Passing them untouched to the customer's own Google or Bedrock provider, without routing, is the
  next step if customers need them.
- **Not now:** Google's newer Interactions API, Gemini Live, Bedrock's bidirectional stream, AWS's model listing
  (a control-plane API).
- Verified with Google's genai and AWS's bedrockruntime Go SDKs in the tests, the Python SDKs (google-genai,
  boto3, AnthropicBedrock) against a local gateway, and `task demo:doors` on the owner's Anthropic and OpenRouter
  keys.

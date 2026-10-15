# Promptdam quickstarts

Point your SDK at the gateway: change the base URL and use a Promptdam key
(`pd_sk_…`, with the gateway scope). Nothing else changes: the gateway calls
your providers with your own keys, so the answers, streams and errors are
theirs. Any SDK may call any model the project serves: a Gemini SDK may ask
for Claude, an Anthropic one for Gemini or a router, translated both ways.

| SDK | Base URL | Key |
|---|---|---|
| OpenAI (Python, Node, Go, …) | `https://gateway.promptdam.dev/v1` (`OPENAI_BASE_URL`) | `OPENAI_API_KEY=pd_sk_…` |
| Anthropic (Python, Node, Go, …) | `https://gateway.promptdam.dev` (`ANTHROPIC_BASE_URL`) | `ANTHROPIC_API_KEY=pd_sk_…` |
| Claude Code | `ANTHROPIC_BASE_URL=https://gateway.promptdam.dev` | `ANTHROPIC_AUTH_TOKEN=pd_sk_…` |
| Google's (google-genai for Python, JS, Go, Java) | `https://gateway.promptdam.dev/gemini` (`GOOGLE_GEMINI_BASE_URL`) | `GEMINI_API_KEY=pd_sk_…` |
| AWS's (boto3, JS v3, Go v2, Java v2, .NET) | `https://gateway.promptdam.dev/bedrock` (the client's endpoint) | a Bedrock API key: `pd_sk_…` |
| Anthropic's `AnthropicBedrock` | `https://gateway.promptdam.dev/bedrock` (`base_url`) | `api_key=pd_sk_…` |

Optional headers: `X-Promptdam-Route` (a route's name), `X-Promptdam-User` (your
end user, for analytics), `X-Promptdam-Tags` (comma-separated). Every answer
carries `X-Promptdam-Trace` (look the call up in the request log) and
`X-Promptdam-Served-Model` (the model that answered, after any fallback).

## Google's SDKs (the Gemini door)

The gateway speaks Gemini's own API at `/gemini`: `generateContent`, its stream,
`countTokens`, `embedContent` and the model list. With google-genai, two
variables are enough (the Python SDK, the JS one and the Go one read them):

```sh
export GOOGLE_GEMINI_BASE_URL=https://gateway.promptdam.dev/gemini
export GEMINI_API_KEY=pd_sk_…
```

```python
from google import genai

client = genai.Client()  # nothing else changes
client.models.generate_content(model="gemini-3.5-flash", contents="Say hi")
```

Or in code: `genai.Client(api_key="pd_sk_…", http_options={"base_url": "https://gateway.promptdam.dev/gemini"})`
(Python), `new GoogleGenAI({apiKey, httpOptions: {baseUrl: "https://gateway.promptdam.dev/gemini"}})` (JS).
Vercel's `@ai-sdk/google` takes the version in its URL:
`createGoogleGenerativeAI({baseURL: "https://gateway.promptdam.dev/gemini/v1beta", apiKey: "pd_sk_…"})`.
LangChain's `ChatGoogleGenerativeAI` passes `base_url` on to google-genai.

From Vertex: use the Gemini API mode above. The Vertex project and region are
the Vertex provider's, set up in Promptdam.

Calls Promptdam can't send to every model are refused, naming what: Google's
own tools (Search grounding, code execution, URL context), its context cache
(`cachedContent`), files uploaded to Google (send them inline), video, and
safety settings stricter than the model's default. Tokens are counted by
Google for Gemini on Google's API or Vertex, by Anthropic for Claude.

## AWS's SDKs (the Bedrock door)

The gateway speaks Bedrock's runtime API at `/bedrock`: Converse, its stream,
InvokeModel (Claude's body) and its stream, and count-tokens. Every current
AWS SDK signs in with a Bedrock API key, sent as a bearer token: use your
Promptdam key as one. AWS credentials (SigV4) can't sign in, as Promptdam
keeps no secret of your key.

Set the key on the client, not with `AWS_BEARER_TOKEN_BEDROCK`: that variable
reaches every Bedrock client in the process, and one still pointed at AWS
would send your Promptdam key there.

```python
import boto3
from botocore import UNSIGNED
from botocore.config import Config

client = boto3.client("bedrock-runtime", region_name="us-east-1",
                      endpoint_url="https://gateway.promptdam.dev/bedrock",
                      config=Config(signature_version=UNSIGNED))
client.meta.events.register("before-send.bedrock-runtime.*",
                            lambda request, **_: request.headers.__setitem__("Authorization", "Bearer pd_sk_…"))
client.converse(modelId="claude-haiku-5-5", messages=[{"role": "user", "content": [{"text": "Say hi"}]}])
```

```ts
new BedrockRuntimeClient({ region: "us-east-1", endpoint: "https://gateway.promptdam.dev/bedrock",
  token: { token: "pd_sk_…" }, authSchemePreference: ["httpBearerAuth"] });
```

```go
bedrockruntime.NewFromConfig(cfg, func(o *bedrockruntime.Options) {
	o.BaseEndpoint = aws.String("https://gateway.promptdam.dev/bedrock")
	o.BearerAuthTokenProvider = bearer.StaticTokenProvider{Token: bearer.Token{Value: "pd_sk_…"}}
	o.AuthSchemePreference = []string{"httpBearerAuth"}
})
```

The region is any (the SDKs ask for one): the gateway calls the providers your
project set up. Models go by any name the project serves: Bedrock's ids
(`amazon.nova-lite-v1:0`, `us.anthropic.claude-sonnet-4-5-20250929-v1:0`) for a
Bedrock provider, plain names (`claude-haiku-5-5`, `gemini-3.5-flash`) or a
router for the others. Refused, by name: guardrails, files in S3, video, audio,
Office documents, prompt management, and other vendors' own InvokeModel bodies
(call those models through Converse).

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

# Google's and AWS's SDKs, with the same key
export GOOGLE_GEMINI_BASE_URL=http://localhost:8282/gemini GEMINI_API_KEY=$ANTHROPIC_API_KEY
export PROMPTDAM_BEDROCK_URL=http://localhost:8282/bedrock PROMPTDAM_API_KEY=$ANTHROPIC_API_KEY
export PROMPTDAM_MODEL=claude-haiku-4-5
uv run gemini_genai.py && uv run bedrock_boto3.py && uv run anthropic_bedrock.py
```

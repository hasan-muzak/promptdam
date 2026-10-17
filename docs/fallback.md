# When the gateway can't be reached

The gateway is built to run as several replicas behind a load balancer, and to keep serving when Valkey or ClickHouse
is down: counters, routing state and the request log fail open (project.md §6.1). Still, an app that must answer when
Promptdam itself is unreachable can call its provider directly for that call. This page is how, and what it costs.

## Decide first: should this app fall back at all?

A call that goes around Promptdam skips everything Promptdam does for it:

- **the firewall**: secrets and personal data reach the provider as written, and a call the firewall would block goes
  through;
- **the request log and the ledger**: the call isn't counted, priced or attributed to a user or a team;
- **budgets and rate limits**, routing, caching and the data region the project picked.

And the app has to hold the provider's key, which Promptdam otherwise keeps out of it.

So: an app whose calls must pass the firewall (compliance, customer data) should **not** fall back; it fails the
call, as it would if the provider were down. An app where an answer matters more than those (an internal assistant, a
coding agent) may.

## When to fall back

Only when Promptdam didn't answer. Every answer the gateway sends, errors included, carries an `X-Promptdam-Trace`
header, so:

| What happened | Fall back? |
|---|---|
| The connection failed or timed out before an answer (DNS, refused, TLS, connect timeout) | Yes |
| 502, 503 or 504 **without** `X-Promptdam-Trace`: the load balancer had no gateway to send the call to | Yes |
| Any answer **with** `X-Promptdam-Trace`: a firewall block (400), a spent budget or quota (429), a bad key (401), the provider's own error passed through | No: Promptdam answered, and going around it would undo that answer |
| A stream that started and then broke | No: retrying would repeat what the user already saw. Fail the call |

Keep the connect timeout short (2 s) so a gateway that can't be reached costs little, and keep the read timeout as it
is: a long answer is not an outage.

## Python, OpenAI SDK

```python
import os

import httpx
import openai

gateway = openai.OpenAI(
    base_url="https://gateway.promptdam.dev/v1",
    api_key=os.environ["PROMPTDAM_API_KEY"],  # pd_sk_…
    timeout=httpx.Timeout(600, connect=2),
    max_retries=1,
)
direct = openai.OpenAI(api_key=os.environ["OPENAI_API_KEY"])  # the provider's own key


def unreachable(err: Exception) -> bool:
    """Whether Promptdam didn't answer, so the call may go to the provider."""
    if isinstance(err, openai.APIConnectionError):  # timeouts included
        return True
    return (
        isinstance(err, openai.APIStatusError)
        and err.status_code in (502, 503, 504)
        and "x-promptdam-trace" not in err.response.headers
    )


def create(**params):
    try:
        return gateway.chat.completions.create(**params)
    except Exception as err:
        if not unreachable(err):
            raise
        return direct.chat.completions.create(**params)


answer = create(model="gpt-5-mini", messages=[{"role": "user", "content": "Say hi"}])
```

With `stream=True`, the SDK raises these errors from `create` itself, before the first chunk, so the same `create`
falls back only before anything was streamed.

## Python, Anthropic SDK

```python
import os

import anthropic
import httpx

gateway = anthropic.Anthropic(
    base_url="https://gateway.promptdam.dev",
    api_key=os.environ["PROMPTDAM_API_KEY"],  # pd_sk_…
    timeout=httpx.Timeout(600, connect=2),
    max_retries=1,
)
direct = anthropic.Anthropic(api_key=os.environ["ANTHROPIC_API_KEY"])


def unreachable(err: Exception) -> bool:
    if isinstance(err, anthropic.APIConnectionError):
        return True
    return (
        isinstance(err, anthropic.APIStatusError)
        and err.status_code in (502, 503, 504)
        and "x-promptdam-trace" not in err.response.headers
    )


def create(**params):
    try:
        return gateway.messages.create(**params)
    except Exception as err:
        if not unreachable(err):
            raise
        return direct.messages.create(**params)
```

## Model names

A direct call goes to one provider with that provider's model names. Promptdam's own names (a router such as
`auto`, a model another provider serves) mean nothing there, so map them before falling back, or fall back only for
calls that already name the provider's model.

"""Chat completions through Promptdam with the OpenAI SDK.

The SDK reads OPENAI_BASE_URL (the gateway's /v1) and OPENAI_API_KEY (a
Promptdam key): nothing else changes.
"""

from openai import OpenAI

client = OpenAI()

response = client.chat.completions.with_raw_response.create(
    model="gpt-5-mini",
    messages=[{"role": "user", "content": "Say hi"}],
    extra_headers={"X-Promptdam-Tags": "example,python"},
)
completion = response.parse()
print("answer:", completion.choices[0].message.content)
print("trace:", response.headers.get("x-promptdam-trace"))
print("served by:", response.headers.get("x-promptdam-served-model"))
print("tokens:", completion.usage.prompt_tokens, "in,", completion.usage.completion_tokens, "out")

stream = client.chat.completions.create(
    model="gpt-5-mini",
    messages=[{"role": "user", "content": "Count to five"}],
    stream=True,
)
print(
    "streamed:", "".join(chunk.choices[0].delta.content or "" for chunk in stream if chunk.choices)
)

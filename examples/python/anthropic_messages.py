"""Messages through Promptdam with the Anthropic SDK.

The SDK reads ANTHROPIC_BASE_URL (the gateway, without /v1) and
ANTHROPIC_API_KEY (a Promptdam key): nothing else changes.
"""

from anthropic import Anthropic

client = Anthropic()

message = client.messages.create(
    model="claude-haiku-4-5",
    max_tokens=64,
    messages=[{"role": "user", "content": "Say hi"}],
)
print("answer:", message.content[0].text)
print("tokens:", message.usage.input_tokens, "in,", message.usage.output_tokens, "out")

with client.messages.stream(
    model="claude-haiku-4-5",
    max_tokens=64,
    messages=[{"role": "user", "content": "Count to five"}],
) as stream:
    print("streamed:", "".join(stream.text_stream))

"""Claude through Promptdam with Anthropic's Bedrock client (AnthropicBedrock).

Its base URL is the gateway's Bedrock door
(https://gateway.promptdam.dev/bedrock) and its API key a Promptdam key,
sent as a Bedrock API key; the region is any (the client asks for one).
"""

import os

from anthropic import AnthropicBedrock

client = AnthropicBedrock(
    api_key=os.environ["PROMPTDAM_API_KEY"],
    base_url=os.environ["PROMPTDAM_BEDROCK_URL"],
    aws_region="us-east-1",
)
model = os.environ.get("PROMPTDAM_MODEL", "claude-haiku-5-5")

message = client.messages.create(
    model=model,
    max_tokens=64,
    messages=[{"role": "user", "content": "Say hi"}],
)
print("answer:", message.content[0].text)

with client.messages.stream(
    model=model,
    max_tokens=64,
    messages=[{"role": "user", "content": "Count to five"}],
) as stream:
    print("streamed:", "".join(stream.text_stream))

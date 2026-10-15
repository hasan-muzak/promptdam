"""Bedrock's runtime API through Promptdam with AWS's SDK (boto3).

The client's endpoint is the gateway's Bedrock door
(https://gateway.promptdam.dev/bedrock) and it signs in with a Promptdam key
as a Bedrock API key (a bearer token), not with AWS credentials. The key is
set on this client only: AWS_BEARER_TOKEN_BEDROCK would do it too, but for
every Bedrock client in the process, sending the key to AWS from the others.
"""

import os

import boto3
from botocore import UNSIGNED
from botocore.config import Config

key = os.environ["PROMPTDAM_API_KEY"]
client = boto3.client(
    "bedrock-runtime",
    region_name="us-east-1",  # any: the gateway calls the providers the project set up
    endpoint_url=os.environ["PROMPTDAM_BEDROCK_URL"],
    config=Config(signature_version=UNSIGNED),
)
client.meta.events.register(
    "before-send.bedrock-runtime.*",
    lambda request, **_: request.headers.__setitem__("Authorization", f"Bearer {key}"),
)
model = os.environ.get("PROMPTDAM_MODEL", "claude-haiku-5-5")

response = client.converse(
    modelId=model,
    messages=[{"role": "user", "content": [{"text": "Say hi"}]}],
    inferenceConfig={"maxTokens": 64},
)
print("answer:", response["output"]["message"]["content"][0]["text"])
print("tokens:", response["usage"]["inputTokens"], "in,", response["usage"]["outputTokens"], "out")

stream = client.converse_stream(
    modelId=model,
    messages=[{"role": "user", "content": [{"text": "Count to five"}]}],
)
text = []
for event in stream["stream"]:
    if "contentBlockDelta" in event:
        text.append(event["contentBlockDelta"]["delta"].get("text", ""))
print("streamed:", "".join(text))

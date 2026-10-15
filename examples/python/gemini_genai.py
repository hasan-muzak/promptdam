"""Gemini's API through Promptdam with Google's SDK (google-genai).

The SDK reads GOOGLE_GEMINI_BASE_URL (the gateway's Gemini door,
https://gateway.promptdam.dev/gemini) and GEMINI_API_KEY (a Promptdam key):
nothing else changes. The model may be Gemini, or any other the project
serves (Claude, GPT, a router), translated both ways.
"""

import os

from google import genai
from google.genai import types

client = genai.Client()
model = os.environ.get("PROMPTDAM_MODEL", "gemini-3.5-flash")

response = client.models.generate_content(
    model=model,
    contents="Say hi",
    config=types.GenerateContentConfig(max_output_tokens=64),
)
usage = response.usage_metadata
print("answer:", response.text)
print("tokens:", usage.prompt_token_count, "in,", usage.candidates_token_count, "out")

streamed = client.models.generate_content_stream(model=model, contents="Count to five")
print("streamed:", "".join(chunk.text or "" for chunk in streamed))

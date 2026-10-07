#!/usr/bin/env sh
# Calls the gateway with curl: a chat completion, a streamed one, an
# Anthropic message, and the models the key may call. Needs OPENAI_BASE_URL
# and OPENAI_API_KEY (a pd_sk_… key); see README.md.
set -eu
: "${OPENAI_BASE_URL:?set OPENAI_BASE_URL, e.g. http://localhost:8282/v1}"
: "${OPENAI_API_KEY:?set OPENAI_API_KEY to a Promptdam key}"

echo "== chat completion (see the X-Promptdam-* headers)"
curl -sS -D - "$OPENAI_BASE_URL/chat/completions" \
  -H "Authorization: Bearer $OPENAI_API_KEY" -H "Content-Type: application/json" \
  -H "X-Promptdam-Tags: example" \
  -d '{"model":"gpt-5-mini","messages":[{"role":"user","content":"Say hi"}]}' | grep -i -E '^x-promptdam|"content"'

echo "== streamed"
curl -sSN "$OPENAI_BASE_URL/chat/completions" \
  -H "Authorization: Bearer $OPENAI_API_KEY" -H "Content-Type: application/json" \
  -d '{"model":"gpt-5-mini","stream":true,"messages":[{"role":"user","content":"Count to five"}]}' | tail -n 3

echo "== Anthropic messages"
curl -sS "$OPENAI_BASE_URL/messages" \
  -H "x-api-key: $OPENAI_API_KEY" -H "anthropic-version: 2023-06-01" -H "Content-Type: application/json" \
  -d '{"model":"claude-haiku-4-5","max_tokens":64,"messages":[{"role":"user","content":"Say hi"}]}'
echo

echo "== models"
curl -sS "$OPENAI_BASE_URL/models" -H "Authorization: Bearer $OPENAI_API_KEY" | head -c 300
echo

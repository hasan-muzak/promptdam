package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// A shape is a kind of call the load sends: its path, whether it streams,
// and its body for the i-th call (each differs, as real prompts' tails do).
type shape struct {
	name   string
	path   string
	stream bool
	body   func(i int64) []byte
}

// shapes are the calls a gateway serves most: small chats streamed and
// not, a coding agent's turn (Claude Code: tools, a cached system prompt,
// a tool loop's history) and a long one deep in a session.
var shapes = map[string]shape{
	"chat-stream": {name: "chat-stream", path: "/v1/chat/completions", stream: true, body: chat(true)},
	"chat":        {name: "chat", path: "/v1/chat/completions", body: chat(false)},
	"agent":       {name: "agent", path: "/v1/messages", stream: true, body: agent(12, 1500)},       // about 30 KB
	"agent-large": {name: "agent-large", path: "/v1/messages", stream: true, body: agent(60, 9500)}, // about 600 KB
}

func chat(stream bool) func(int64) []byte {
	system := "You are a support assistant for an online store. Answer in two or three sentences, in the customer's " +
		"language, and never promise refunds the policy doesn't allow. " + strings.Repeat("Policy: returns within 30 days. ", 20)
	return func(i int64) []byte {
		b, _ := json.Marshal(map[string]any{"model": "gpt-5-mini", "stream": stream, "messages": []map[string]any{
			{"role": "system", "content": system},
			{"role": "user", "content": fmt.Sprintf("Order %d arrived damaged. What can I do?", i)},
		}})
		return b
	}
}

// agent is a coding agent's call: a cached system prompt and tools, then
// steps tool calls, each reading about size bytes of source code.
func agent(steps, size int) func(int64) []byte {
	tools := make([]map[string]any, 0, 12)
	for _, name := range []string{"Read", "Edit", "Write", "Glob", "Grep", "Bash", "WebFetch", "TodoWrite", "Task", "NotebookEdit", "LS", "MultiEdit"} {
		tools = append(tools, map[string]any{"name": name, "description": "The " + name + " tool. " + strings.Repeat("It works on the repository's files and reports what it did. ", 6),
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"}}, "required": []string{"path"}}})
	}
	tools[len(tools)-1]["cache_control"] = map[string]any{"type": "ephemeral"}
	system := []map[string]any{{"type": "text", "text": "You are a coding agent working in a Go repository. " +
		strings.Repeat("Read before you edit, run the tests after, and say what you changed. ", 40),
		"cache_control": map[string]any{"type": "ephemeral"}}}
	history := make([]map[string]any, 0, 2*steps+1)
	history = append(history, map[string]any{"role": "user", "content": "The billing tests fail. Find the bug and fix it."})
	for s := range steps {
		id := fmt.Sprintf("toolu_%03d", s)
		history = append(history,
			map[string]any{"role": "assistant", "content": []map[string]any{{"type": "tool_use", "id": id, "name": "Read",
				"input": map[string]any{"path": fmt.Sprintf("billing/file%d.go", s)}}}},
			map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": id,
				"content": source(s, size)}}})
	}
	msgs := append(history, map[string]any{"role": "user", "content": "Run " + runMark + ": now run go test ./... and fix what still fails."})
	b, _ := json.Marshal(map[string]any{"model": "claude-sonnet-5-5", "max_tokens": 8192, "stream": true,
		"system": system, "tools": tools, "messages": msgs,
		"metadata": map[string]any{"user_id": `{"device_id":"load","session_id":"00000000-0000-4000-8000-000000000000"}`}})
	return stamp(b)
}

// runMark is where a body's call number goes: an agent's body is built once
// (600 KB isn't marshalled a thousand times a second), then stamped.
const runMark = "RUN_NUMBER"

func stamp(tmpl []byte) func(int64) []byte {
	at := bytes.Index(tmpl, []byte(runMark))
	return func(i int64) []byte {
		n := strconv.AppendInt(nil, i, 10)
		out := make([]byte, 0, len(tmpl)+len(n))
		return append(append(append(out, tmpl[:at]...), n...), tmpl[at+len(runMark):]...)
	}
}

// source is about size bytes of Go code, the way a file read looks: names
// that sound like secrets (Token, apiKey) and no secret in them.
func source(n, size int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "package billing\n\nimport \"os\"\n\n")
	for i := 0; b.Len() < size; i++ {
		fmt.Fprintf(&b, "// Charge%d_%d prepares a charge of the order.\nfunc Charge%d_%d(req Request, items []Item) Charge {\n"+
			"\ttotal := int64(0)\n\tfor _, it := range items {\n\t\ttotal += it.Cents * int64(it.Qty)\n\t}\n"+
			"\treturn Charge{Customer: req.Customer, Token: req.Token, Cents: total, Auth: os.Getenv(\"BILLING_API_KEY\")}\n}\n\n",
			n, i, n, i)
	}
	return b.String()
}

package gateway

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
)

// Gemini 3 signs its function calls (a thought signature, which carries
// its reasoning to the next step) and refuses a conversation whose current
// turn has a call without one. Calls another model wrote (a router moved
// the conversation to Gemini mid-turn) or a client dropped have none:
// they are sent with the value Google documents for histories from other
// models.

// skipSignature is Google's value for a call with no signature of its own.
const skipSignature = "skip_thought_signature_validator"

// geminiVersion is a Gemini model's major version.
var geminiVersion = regexp.MustCompile(`gemini-(\d+)`)

// signs reports a target that checks thought signatures: Gemini from
// version 3 (and its aliases, which name none) on Google's and Vertex's
// OpenAI-compatible APIs.
func signs(t Target) bool {
	if t.Kind != "gemini" && t.Kind != "vertex" {
		return false
	}
	m := strings.ToLower(t.Model)
	if !strings.Contains(m, "gemini") {
		return false
	}
	if v := geminiVersion.FindStringSubmatch(m); v != nil {
		major, err := strconv.Atoi(v[1])
		return err == nil && major >= 3
	}
	return true
}

// withSignatures gives the first function call of each step of a Chat
// call's current turn (after the last message of the user's) the skip
// value, when none of the step's calls is signed. The other messages keep
// their bytes.
func withSignatures(body []byte) []byte {
	raw, ok, err := wire.Field(body, "messages")
	if err != nil || !ok {
		return body
	}
	var msgs []json.RawMessage
	if json.Unmarshal(raw, &msgs) != nil {
		return body
	}
	type head struct {
		Role      string `json:"role"`
		ToolCalls []struct {
			Extra *struct {
				Google *struct {
					ThoughtSignature string `json:"thought_signature"`
				} `json:"google"`
			} `json:"extra_content"`
		} `json:"tool_calls"`
	}
	heads := make([]head, len(msgs))
	turn := 0
	for i, m := range msgs {
		if json.Unmarshal(m, &heads[i]) != nil {
			return body
		}
		if heads[i].Role == "user" {
			turn = i + 1
		}
	}
	changed := false
	for i := turn; i < len(msgs); i++ {
		h := heads[i]
		if h.Role != "assistant" || len(h.ToolCalls) == 0 {
			continue
		}
		signed := false
		for _, tc := range h.ToolCalls {
			signed = signed || tc.Extra != nil && tc.Extra.Google != nil && tc.Extra.Google.ThoughtSignature != ""
		}
		if signed {
			continue
		}
		if m, ok := signFirst(msgs[i]); ok {
			msgs[i], changed = m, true
		}
	}
	if !changed {
		return body
	}
	var b bytes.Buffer
	b.WriteByte('[')
	for i, m := range msgs {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(m)
	}
	b.WriteByte(']')
	out, err := wire.SetField(body, "messages", b.Bytes())
	if err != nil {
		return body
	}
	return out
}

// signFirst is an assistant message whose first tool call carries the
// skip value.
func signFirst(msg json.RawMessage) (json.RawMessage, bool) {
	var calls []map[string]json.RawMessage
	raw, ok, err := wire.Field(msg, "tool_calls")
	if err != nil || !ok || json.Unmarshal(raw, &calls) != nil || len(calls) == 0 {
		return msg, false
	}
	var extra map[string]json.RawMessage
	_ = json.Unmarshal(calls[0]["extra_content"], &extra)
	if extra == nil {
		extra = map[string]json.RawMessage{}
	}
	extra["google"] = json.RawMessage(`{"thought_signature":"` + skipSignature + `"}`)
	calls[0]["extra_content"], _ = json.Marshal(extra)
	b, err := json.Marshal(calls)
	if err != nil {
		return msg, false
	}
	out, err := wire.SetField(msg, "tool_calls", b)
	return out, err == nil
}

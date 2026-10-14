package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/server/features/promptcache"
)

// cachePrompt turns on a provider's prompt cache for a call, by the
// target's plan: breakpoints for models that cache what a request marks
// (Anthropic Messages, or OpenAI Chat content parts for a router of
// providers such as OpenRouter), on their own API and on calls translated
// to it; for OpenAI a conversation's key, which keeps its calls on the
// machines that cached them. A breakpoint or key the client set wins.
func (c *call) cachePrompt(body []byte, format wire.Format, t Target, req wire.Request) []byte {
	strategy := t.Cache.strategy(format)
	c.rec.CacheStrategy, c.rec.CacheTrial = strategy, t.Cache.Trial
	// The prefix as the target reads it: a translated call's own.
	prefix := func() (wire.Request, bool) {
		if format == c.ep.format {
			return req, true
		}
		translated, err := wire.ParseRequest(format, body)
		return translated, err == nil
	}
	switch {
	case format == wire.AnthropicMessages && strategy == promptcache.Markers:
		prefix, ok := prefix()
		if !ok {
			return body
		}
		plan := promptcache.Plan{Prefix: or(c.promptTTL.Prefix, promptcache.FiveMinutes), MinTokens: t.MinCache}
		// The conversation's tail, with Anthropic's automatic breakpoint,
		// on its own API: a conversation that goes on, or an agent's tool
		// loop (tools offered). A single question's tail is never read
		// again: writing it would only cost. Lifetimes are the family's,
		// learned from its calls' gaps (five minutes until then).
		if t.Kind == "anthropic" && (req.Turn.Humans >= 2 || req.Turn.Continuation || req.Tools) {
			plan.Tail = or(c.promptTTL.Tail, promptcache.FiveMinutes)
		}
		body, c.rec.PromptCached = promptcache.Mark(body, prefix, plan)
	case format == wire.OpenAIChat && strategy == promptcache.Markers:
		prefix, ok := prefix()
		if !ok {
			return body
		}
		plan := promptcache.Plan{Prefix: or(c.promptTTL.Prefix, promptcache.FiveMinutes), MinTokens: t.MinCache}
		if req.Turn.Humans >= 2 || req.Turn.Continuation || req.Tools {
			plan.Tail = or(c.promptTTL.Tail, promptcache.FiveMinutes)
		}
		body, c.rec.PromptCached = promptcache.MarkChat(body, prefix, plan)
	case format == wire.OpenAIChat && t.Kind == "openai" && c.promptKey != "":
		if _, set, err := wire.Field(body, "prompt_cache_key"); err == nil && !set {
			key, _ := json.Marshal(c.promptKey)
			if out, err := wire.SetField(body, "prompt_cache_key", key); err == nil {
				body, c.rec.PromptCached = out, true
			}
		}
	}
	return body
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// thinkingBindingBeta lets a Messages call ask for thinking blocks whose
// conversation changed to be dropped rather than refused.
const thinkingBindingBeta = "thinking-binding-controls-2026-08-01"

// Heals: how a call a provider refused was sent again.
const (
	// HealDropBlock: the provider was asked to drop the blocks
	// (thinking.block_binding.prefix_mismatch_behavior).
	HealDropBlock = "drop_block"
	// HealStripThinking: the history's thinking blocks were taken out.
	HealStripThinking = "strip_thinking"
	// HealUnmarked: the call was sent again without the gateway's cache
	// breakpoints and session, which its provider refused.
	HealUnmarked = "unmarked"
)

// healThinking is a Messages call sent again after the provider refused a
// thinking block "bound to a different conversation" (refusal, its error
// body): with the provider asked to drop such blocks when the call's
// thinking takes it (adaptive or enabled, with the beta header beta), else
// with the history's thinking blocks taken out. The model answers that turn
// without the reasoning the blocks carried; nothing else changes.
func healThinking(body, refusal []byte) (out []byte, how, beta string, ok bool) {
	if !bytes.Contains(bytes.ToLower(refusal), []byte("bound to a different conversation")) {
		return nil, "", "", false
	}
	if raw, found, err := wire.Field(body, "thinking"); err == nil && found {
		var thinking map[string]json.RawMessage
		if json.Unmarshal(raw, &thinking) == nil {
			var kind string
			_ = json.Unmarshal(thinking["type"], &kind)
			if _, bound := thinking["block_binding"]; !bound && (kind == "adaptive" || kind == "enabled") {
				thinking["block_binding"] = json.RawMessage(`{"prefix_mismatch_behavior":"drop_block"}`)
				if v, err := json.Marshal(thinking); err == nil {
					if out, err := wire.SetField(body, "thinking", v); err == nil {
						return out, HealDropBlock, thinkingBindingBeta, true
					}
				}
			}
		}
	}
	if out, ok := stripThinking(body); ok {
		return out, HealStripThinking, "", true
	}
	return nil, "", "", false
}

// stripThinking takes the thinking blocks out of a Messages call's history;
// ok is false when it has none.
func stripThinking(body []byte) ([]byte, bool) {
	raw, found, err := wire.Field(body, "messages")
	if err != nil || !found {
		return nil, false
	}
	var messages []json.RawMessage
	if json.Unmarshal(raw, &messages) != nil {
		return nil, false
	}
	stripped := false
	for i, m := range messages {
		var msg map[string]json.RawMessage
		if json.Unmarshal(m, &msg) != nil {
			continue
		}
		var blocks []json.RawMessage
		if json.Unmarshal(msg["content"], &blocks) != nil {
			continue
		}
		kept := blocks[:0]
		for _, b := range blocks {
			var kind struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(b, &kind) == nil && (kind.Type == "thinking" || kind.Type == "redacted_thinking") {
				continue
			}
			kept = append(kept, b)
		}
		if len(kept) == len(blocks) {
			continue
		}
		content, _ := json.Marshal(kept)
		msg["content"] = content
		if out, err := json.Marshal(msg); err == nil {
			messages[i], stripped = out, true
		}
	}
	if !stripped {
		return nil, false
	}
	all, err := json.Marshal(messages)
	if err != nil {
		return nil, false
	}
	out, err := wire.SetField(body, "messages", all)
	return out, err == nil
}

// addBeta adds an Anthropic beta to a call's header, unless it is there.
func addBeta(h http.Header, beta string) {
	v := h.Get("Anthropic-Beta")
	for _, b := range strings.Split(v, ",") {
		if strings.TrimSpace(b) == beta {
			return
		}
	}
	if v == "" {
		h.Set("Anthropic-Beta", beta)
		return
	}
	h.Set("Anthropic-Beta", v+","+beta)
}

// Package translate lets a call in one API format be answered by a provider
// speaking the other: OpenAI Chat Completions ⇄ Anthropic Messages. It
// translates the request, the answer (whole or streamed) and the provider's
// errors, so fallbacks, routing rules and router tiers may name a model of
// either format.
//
// Content is never dropped: a request the target can't express (audio, a
// file by ID, a provider's server tools, an assistant prefill) is refused
// with an *Error, and the gateway tries the next target. Tuning parameters
// the target has no equivalent for (seed, logit_bias, top_k…) are left out
// and listed, for the X-Promptdam-Dropped header.
package translate

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
)

// DefaultMaxTokens caps the answer of an OpenAI request that sets no limit,
// when it goes to Anthropic, which requires one.
const DefaultMaxTokens = 8192

// Options shape a request's translation.
type Options struct {
	// Model is the target's model.
	Model string
	// MaxTokens is the answer's limit when an OpenAI request sets none
	// (default DefaultMaxTokens).
	MaxTokens int
	// Kind is the target provider's kind ("openai", "gemini", …): OpenAI
	// itself wants max_completion_tokens, the others max_tokens.
	Kind string
	// Reasoning reports that the target model takes a reasoning effort, so
	// one is passed on rather than dropped.
	Reasoning bool
}

// Error is a request the target's format can't express.
type Error struct{ Message string }

func (e *Error) Error() string { return "translate: " + e.Message }

func refuse(msg string) error { return &Error{Message: msg} }

// Supported reports whether calls of format from can be translated to
// format to: Chat Completions and Messages, either way.
func Supported(from, to wire.Format) bool {
	return from == wire.OpenAIChat && to == wire.AnthropicMessages || from == wire.AnthropicMessages && to == wire.OpenAIChat
}

// Request translates a request body of format from into format to, for the
// model in o. dropped lists the parameters left out, sorted.
func Request(from, to wire.Format, body []byte, o Options) (out []byte, dropped []string, err error) {
	switch {
	case from == wire.OpenAIChat && to == wire.AnthropicMessages:
		out, dropped, err = chatToMessages(body, o)
	case from == wire.AnthropicMessages && to == wire.OpenAIChat:
		out, dropped, err = messagesToChat(body, o)
	default:
		return nil, nil, refuse("calls of " + from.String() + " can't be sent as " + to.String())
	}
	slices.Sort(dropped)
	return out, slices.Compact(dropped), err
}

// marshal encodes v without escaping HTML characters, as providers write
// JSON.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// null reports an absent or null JSON value.
func null(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) == 0 || bytes.Equal(raw, []byte("null"))
}

// dataURI splits data:<media type>;base64,<data>.
func dataURI(u string) (mediaType, data string, ok bool) {
	rest, ok := strings.CutPrefix(u, "data:")
	if !ok {
		return "", "", false
	}
	meta, data, ok := strings.Cut(rest, ",")
	if !ok {
		return "", "", false
	}
	mediaType, base64 := strings.CutSuffix(meta, ";base64")
	if !base64 || mediaType == "" {
		return "", "", false
	}
	return mediaType, data, true
}

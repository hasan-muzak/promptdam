package translate

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
)

// What translation adds per streamed event, and per request: each text
// delta of a Messages stream becomes a Chat chunk, and the other way.

func BenchmarkStreamMessagesToChat(b *testing.B) {
	ev := wire.Event{Data: []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"tok42 "}}`)}
	s, _ := NewStream(wire.AnthropicMessages, wire.OpenAIChat, false)
	s.Event(nil, wire.Event{Data: []byte(`{"type":"message_start","message":{"id":"msg_1","model":"claude-haiku-4-5"}}`)})
	var out []byte
	b.ReportAllocs()
	for b.Loop() {
		out = s.Event(out[:0], ev)
	}
}

func BenchmarkStreamChatToMessages(b *testing.B) {
	ev := wire.Event{Data: []byte(`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-5","choices":[{"index":0,"delta":{"content":"tok42 "},"finish_reason":null}]}`)}
	s, _ := NewStream(wire.OpenAIChat, wire.AnthropicMessages, false)
	var out []byte
	b.ReportAllocs()
	for b.Loop() {
		out = s.Event(out[:0], ev)
	}
}

func BenchmarkRequestChatToMessages(b *testing.B) {
	var msgs strings.Builder
	for i := range 20 {
		fmt.Fprintf(&msgs, `,{"role":"user","content":"question %d about the invoice"},{"role":"assistant","content":"answer %d"}`, i, i)
	}
	body := []byte(`{"model":"gpt-5","messages":[{"role":"system","content":"` + strings.Repeat("You extract invoice fields. ", 100) +
		`"}` + msgs.String() + `,{"role":"user","content":"last"}]}`)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := Request(wire.OpenAIChat, wire.AnthropicMessages, body, Options{Model: "claude-haiku-4-5"}); err != nil {
			b.Fatal(err)
		}
	}
}

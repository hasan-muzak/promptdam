package converse

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
)

// Stream turns ConverseStream's answer, AWS's binary event stream, into
// Anthropic's server-sent events, as the reader is read (no goroutine:
// closing it closes the answer). model and id name the message (Converse's
// events carry neither). Converse opens no block for text or reasoning, so
// a block is opened on its first delta; its usage comes after its stop, so
// the stop is held until the usage. An exception ends the stream with an
// error event.
func Stream(body io.ReadCloser, model, id string) io.ReadCloser {
	return &stream{src: body, dec: eventstream.NewDecoder(), model: model, id: id, open: map[int]bool{}}
}

type stream struct {
	src   io.ReadCloser
	dec   *eventstream.Decoder
	model string
	id    string

	out     bytes.Buffer
	payload []byte
	started bool
	open    map[int]bool // blocks opened, not yet closed
	stop    string       // the stop reason, held for the usage
	usage   *cUsage
	done    bool
}

func (s *stream) Read(p []byte) (int, error) {
	for s.out.Len() == 0 && !s.done {
		s.next()
	}
	if s.out.Len() == 0 {
		return 0, io.EOF
	}
	return s.out.Read(p)
}

func (s *stream) Close() error { return s.src.Close() }

// next decodes one message and writes its events.
func (s *stream) next() {
	msg, err := s.dec.Decode(s.src, s.payload[:0])
	if err != nil {
		if errors.Is(err, io.EOF) && s.stop != "" {
			s.end() // stopped, its usage never sent
		} else {
			s.fail("api_error", "Bedrock's stream broke off before its end: "+err.Error())
		}
		return
	}
	s.payload = msg.Payload
	header := func(name string) string {
		if v := msg.Headers.Get(name); v != nil {
			return v.String()
		}
		return ""
	}
	switch header(":message-type") {
	case "exception":
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(msg.Payload, &e)
		kind := header(":exception-type")
		t := "api_error"
		switch kind {
		case "throttlingException", "serviceUnavailableException":
			t = "overloaded_error"
		case "validationException":
			t = "invalid_request_error"
		}
		s.fail(t, kind+": "+e.Message)
		return
	case "error":
		s.fail("api_error", header(":error-code")+": "+header(":error-message"))
		return
	}
	s.event(header(":event-type"), msg.Payload)
}

// event writes the Anthropic events of one Converse event.
func (s *stream) event(name string, payload []byte) {
	var e struct {
		ContentBlockIndex int `json:"contentBlockIndex"`
		Start             struct {
			ToolUse *struct {
				ToolUseID string `json:"toolUseId"`
				Name      string `json:"name"`
			} `json:"toolUse"`
		} `json:"start"`
		Delta struct {
			Text    *string `json:"text"`
			ToolUse *struct {
				Input string `json:"input"`
			} `json:"toolUse"`
			ReasoningContent *struct {
				Text            *string `json:"text"`
				Signature       *string `json:"signature"`
				RedactedContent *string `json:"redactedContent"`
			} `json:"reasoningContent"`
		} `json:"delta"`
		StopReason string  `json:"stopReason"`
		Usage      *cUsage `json:"usage"`
	}
	if err := json.Unmarshal(payload, &e); err != nil {
		s.fail("api_error", "an event of Bedrock's stream doesn't read: "+err.Error())
		return
	}
	i := e.ContentBlockIndex
	switch name {
	case "messageStart":
		s.begin()
	case "contentBlockStart":
		s.begin()
		if tu := e.Start.ToolUse; tu != nil && !s.open[i] {
			s.open[i] = true
			s.emit("content_block_start", map[string]any{"index": i,
				"content_block": map[string]any{"type": "tool_use", "id": tu.ToolUseID, "name": tu.Name, "input": map[string]any{}}})
		}
	case "contentBlockDelta":
		s.begin()
		d := e.Delta
		switch {
		case d.Text != nil:
			s.block(i, map[string]any{"type": "text", "text": ""})
			s.emit("content_block_delta", map[string]any{"index": i, "delta": map[string]any{"type": "text_delta", "text": *d.Text}})
		case d.ToolUse != nil:
			s.block(i, map[string]any{"type": "tool_use", "id": "", "name": "", "input": map[string]any{}})
			s.emit("content_block_delta", map[string]any{"index": i,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": d.ToolUse.Input}})
		case d.ReasoningContent != nil && d.ReasoningContent.RedactedContent != nil:
			s.block(i, map[string]any{"type": "redacted_thinking", "data": *d.ReasoningContent.RedactedContent})
		case d.ReasoningContent != nil && d.ReasoningContent.Text != nil:
			s.block(i, map[string]any{"type": "thinking", "thinking": ""})
			s.emit("content_block_delta", map[string]any{"index": i,
				"delta": map[string]any{"type": "thinking_delta", "thinking": *d.ReasoningContent.Text}})
		case d.ReasoningContent != nil && d.ReasoningContent.Signature != nil:
			s.block(i, map[string]any{"type": "thinking", "thinking": ""})
			s.emit("content_block_delta", map[string]any{"index": i,
				"delta": map[string]any{"type": "signature_delta", "signature": *d.ReasoningContent.Signature}})
		}
	case "contentBlockStop":
		if s.open[i] {
			delete(s.open, i)
			s.emit("content_block_stop", map[string]any{"index": i})
		}
	case "messageStop":
		s.begin()
		s.stop = stopReason(e.StopReason)
		if s.usage != nil {
			s.end()
		}
	case "metadata":
		if e.Usage != nil {
			s.usage = e.Usage
		}
		if s.stop != "" {
			s.end()
		}
	}
}

// begin opens the message, once.
func (s *stream) begin() {
	if s.started {
		return
	}
	s.started = true
	s.emit("message_start", map[string]any{"message": map[string]any{"id": s.id, "type": "message", "role": "assistant",
		"model": s.model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil,
		"usage": map[string]int{"input_tokens": 0, "output_tokens": 0}}})
}

// block opens block i on its first delta.
func (s *stream) block(i int, contentBlock map[string]any) {
	if s.open[i] {
		return
	}
	s.open[i] = true
	s.emit("content_block_start", map[string]any{"index": i, "content_block": contentBlock})
}

// end closes the blocks left open and the message, with its usage.
func (s *stream) end() {
	if s.done {
		return
	}
	s.begin()
	for _, i := range slices.Sorted(maps.Keys(s.open)) {
		s.emit("content_block_stop", map[string]any{"index": i})
	}
	clear(s.open)
	usage := map[string]any{"output_tokens": 0}
	if s.usage != nil {
		usage = s.usage.messagesUsage()
	}
	s.emit("message_delta", map[string]any{"delta": map[string]any{"stop_reason": s.stop, "stop_sequence": nil},
		"usage": usage})
	s.emit("message_stop", map[string]any{})
	s.done = true
}

// fail ends the stream with an error event.
func (s *stream) fail(kind, msg string) {
	s.emit("error", map[string]any{"error": map[string]string{"type": kind, "message": msg}})
	s.done = true
}

// emit writes one server-sent event, its data typed by its name.
func (s *stream) emit(name string, data map[string]any) {
	data["type"] = name
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	s.out.WriteString("event: ")
	s.out.WriteString(name)
	s.out.WriteString("\ndata: ")
	s.out.Write(b)
	s.out.WriteString("\n\n")
}

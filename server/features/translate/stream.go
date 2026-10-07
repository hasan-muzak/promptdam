package translate

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
)

// Stream translates a provider's event stream into the client's, event by
// event, as the events arrive.
type Stream interface {
	// Event appends the client's bytes for one provider event to out.
	Event(out []byte, ev wire.Event) []byte
	// End appends what the client still needs when the provider's stream
	// ended. A stream cut short stays cut: nothing is made up.
	End(out []byte) []byte
}

// NewStream returns a Stream from the provider's format to the client's;
// includeUsage reports that a Chat client asked for the usage chunk.
func NewStream(provider, client wire.Format, includeUsage bool) (Stream, error) {
	switch {
	case provider == wire.AnthropicMessages && client == wire.OpenAIChat:
		return &toChat{includeUsage: includeUsage, tools: map[int]int{}, created: time.Now().Unix()}, nil
	case provider == wire.OpenAIChat && client == wire.AnthropicMessages:
		return &toMessages{byIndex: map[int]*pendingTool{}}, nil
	}
	return nil, refuse("streams of " + provider.String() + " can't be sent as " + client.String())
}

// toChat turns Messages events into Chat Completions chunks.
type toChat struct {
	includeUsage bool
	id, model    string
	created      int64
	tools        map[int]int // content block index → tool call index
	nextTool     int
	finish       string
	usage        msgUsage
	done         bool
}

type chunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
	Usage   *chatUsage    `json:"usage,omitempty"`
}

type chunkChoice struct {
	Index        int        `json:"index"`
	Delta        chunkDelta `json:"delta"`
	FinishReason *string    `json:"finish_reason"`
}

type chunkDelta struct {
	Role      string          `json:"role,omitempty"`
	Content   *string         `json:"content,omitempty"`
	Refusal   *string         `json:"refusal,omitempty"`
	ToolCalls []chunkToolCall `json:"tool_calls,omitempty"`
}

type chunkToolCall struct {
	Index    int           `json:"index"`
	ID       string        `json:"id,omitempty"`
	Type     string        `json:"type,omitempty"`
	Function chunkFunction `json:"function"`
}

type chunkFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

// messagesEvent is a Messages stream event, as far as translation reads it.
type messagesEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		ID    string   `json:"id"`
		Model string   `json:"model"`
		Usage msgUsage `json:"usage"`
	} `json:"message"`
	ContentBlock *msgBlock `json:"content_block"`
	Delta        *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *msgUsage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (s *toChat) Event(out []byte, ev wire.Event) []byte {
	if s.done || ev.Partial || len(ev.Data) == 0 {
		return out
	}
	var e messagesEvent
	if json.Unmarshal(ev.Data, &e) != nil {
		return out
	}
	switch e.Type {
	case "message_start":
		if e.Message != nil {
			s.id, s.model, s.usage = e.Message.ID, e.Message.Model, e.Message.Usage
		}
		return s.chunk(out, chunkDelta{Role: "assistant", Content: text("")}, nil)
	case "content_block_start":
		cb := e.ContentBlock
		switch {
		case cb == nil:
		case cb.Type == "text" && cb.Text != nil && *cb.Text != "":
			return s.chunk(out, chunkDelta{Content: cb.Text}, nil)
		case cb.Type == "tool_use":
			i := s.nextTool
			s.nextTool++
			s.tools[e.Index] = i
			return s.chunk(out, chunkDelta{ToolCalls: []chunkToolCall{{Index: i, ID: cb.ID, Type: "function",
				Function: chunkFunction{Name: cb.Name}}}}, nil)
		}
	case "content_block_delta":
		switch {
		case e.Delta == nil:
		case e.Delta.Type == "text_delta" && e.Delta.Text != "":
			return s.chunk(out, chunkDelta{Content: &e.Delta.Text}, nil)
		case e.Delta.Type == "input_json_delta":
			if i, ok := s.tools[e.Index]; ok && e.Delta.PartialJSON != "" {
				return s.chunk(out, chunkDelta{ToolCalls: []chunkToolCall{{Index: i, Function: chunkFunction{Arguments: e.Delta.PartialJSON}}}}, nil)
			}
		}
	case "message_delta":
		if e.Delta != nil && e.Delta.StopReason != "" {
			s.finish = finishReason(e.Delta.StopReason)
		}
		if u := e.Usage; u != nil {
			s.usage.OutputTokens = u.OutputTokens
			if u.InputTokens > 0 {
				s.usage.InputTokens = u.InputTokens
			}
			if u.CacheReadInputTokens > 0 {
				s.usage.CacheReadInputTokens = u.CacheReadInputTokens
			}
			if u.CacheCreationInputTokens > 0 {
				s.usage.CacheCreationInputTokens = u.CacheCreationInputTokens
			}
		}
	case "message_stop":
		s.done = true
		finish := s.finish
		if finish == "" {
			finish = "stop"
		}
		out = s.chunk(out, chunkDelta{}, &finish)
		if s.includeUsage {
			b, _ := marshal(chunk{ID: s.id, Object: "chat.completion.chunk", Created: s.created, Model: s.model,
				Choices: []chunkChoice{}, Usage: chatUsageOf(s.usage)})
			out = sseData(out, b)
		}
		return append(out, "data: [DONE]\n\n"...)
	case "ping":
		return append(out, ": ping\n\n"...)
	case "error":
		s.done = true
		msg, typ := "the provider's stream failed", "api_error"
		if e.Error != nil {
			msg, typ = e.Error.Message, e.Error.Type
		}
		return sseData(out, openAIError(typ, msg))
	}
	return out
}

func (s *toChat) chunk(out []byte, d chunkDelta, finish *string) []byte {
	b, _ := marshal(chunk{ID: s.id, Object: "chat.completion.chunk", Created: s.created, Model: s.model,
		Choices: []chunkChoice{{Delta: d, FinishReason: finish}}})
	return sseData(out, b)
}

func (s *toChat) End(out []byte) []byte { return out }

// toMessages turns Chat Completions chunks into Messages events. Text is
// passed on as it arrives; a tool call is passed on whole once its
// arguments are complete, since providers may interleave several.
type toMessages struct {
	started, done bool
	id, model     string
	next          int // the next content block's index
	textOpen      bool
	tools         []*pendingTool
	byIndex       map[int]*pendingTool
	stop          string
	refusal       bool
	usage         msgUsage
}

type pendingTool struct {
	id, name string
	args     strings.Builder
}

type chatChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content   *string `json:"content"`
			Refusal   *string `json:"refusal"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (s *toMessages) Event(out []byte, ev wire.Event) []byte {
	if s.done || ev.Partial || len(ev.Data) == 0 {
		return out
	}
	if bytes.Equal(bytes.TrimSpace(ev.Data), []byte("[DONE]")) {
		return s.finish(out)
	}
	var c chatChunk
	if json.Unmarshal(ev.Data, &c) != nil {
		return out
	}
	if c.Error != nil {
		s.done = true
		return sseEvent(out, "error", anthropicError("api_error", c.Error.Message))
	}
	if !s.started {
		s.started, s.id, s.model = true, c.ID, c.Model
		b, _ := marshal(struct {
			Type    string         `json:"type"`
			Message messagesAnswer `json:"message"`
		}{"message_start", messagesAnswer{ID: c.ID, Type: "message", Role: "assistant", Model: c.Model, Content: []msgBlock{}}})
		out = sseEvent(out, "message_start", b)
	}
	for _, ch := range c.Choices {
		if ch.Index != 0 {
			continue
		}
		d := ch.Delta
		if d.Content != nil && *d.Content != "" {
			out = s.text(out, *d.Content)
		}
		if d.Refusal != nil && *d.Refusal != "" {
			s.refusal = true
			out = s.text(out, *d.Refusal)
		}
		for _, tc := range d.ToolCalls {
			t := s.byIndex[tc.Index]
			if t == nil {
				t = &pendingTool{}
				s.byIndex[tc.Index] = t
				s.tools = append(s.tools, t)
			}
			if t.id == "" {
				t.id = tc.ID
			}
			if t.name == "" {
				t.name = tc.Function.Name
			}
			t.args.WriteString(tc.Function.Arguments)
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			s.stop = stopReason(*ch.FinishReason)
		}
	}
	if c.Usage != nil {
		s.usage = msgUsageOf(*c.Usage)
	}
	return out
}

// The Messages stream events written.
type (
	blockStart struct {
		Type         string   `json:"type"`
		Index        int      `json:"index"`
		ContentBlock msgBlock `json:"content_block"`
	}
	blockDelta struct {
		Type  string     `json:"type"`
		Index int        `json:"index"`
		Delta deltaShape `json:"delta"`
	}
	deltaShape struct {
		Type        string `json:"type"`
		Text        string `json:"text,omitempty"`
		PartialJSON string `json:"partial_json,omitempty"`
	}
	blockStop struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
	}
	messageDelta struct {
		Type  string `json:"type"`
		Delta struct {
			StopReason   string  `json:"stop_reason"`
			StopSequence *string `json:"stop_sequence"`
		} `json:"delta"`
		Usage msgUsage `json:"usage"`
	}
)

func (s *toMessages) text(out []byte, t string) []byte {
	if !s.textOpen {
		s.textOpen = true
		b, _ := marshal(blockStart{"content_block_start", s.next, msgBlock{Type: "text", Text: text("")}})
		out = sseEvent(out, "content_block_start", b)
	}
	b, _ := marshal(blockDelta{"content_block_delta", s.next, deltaShape{Type: "text_delta", Text: t}})
	return sseEvent(out, "content_block_delta", b)
}

func (s *toMessages) closeBlock(out []byte) []byte {
	b, _ := marshal(blockStop{"content_block_stop", s.next})
	s.next++
	return sseEvent(out, "content_block_stop", b)
}

// finish closes the open blocks, passes the tool calls on and ends the
// message.
func (s *toMessages) finish(out []byte) []byte {
	if s.done || !s.started {
		return out
	}
	s.done = true
	if s.textOpen {
		out = s.closeBlock(out)
	}
	for _, t := range s.tools {
		b, _ := marshal(blockStart{"content_block_start", s.next,
			msgBlock{Type: "tool_use", ID: toolID(t.id), Name: t.name, Input: json.RawMessage(`{}`)}})
		out = sseEvent(out, "content_block_start", b)
		if args := t.args.String(); strings.TrimSpace(args) != "" {
			b, _ = marshal(blockDelta{"content_block_delta", s.next, deltaShape{Type: "input_json_delta", PartialJSON: string(toolInput(args))}})
			out = sseEvent(out, "content_block_delta", b)
		}
		out = s.closeBlock(out)
	}
	md := messageDelta{Type: "message_delta", Usage: s.usage}
	md.Delta.StopReason = s.stop
	switch {
	case s.refusal:
		md.Delta.StopReason = "refusal"
	case s.stop == "":
		md.Delta.StopReason = "end_turn"
	}
	b, _ := marshal(md)
	out = sseEvent(out, "message_delta", b)
	return sseEvent(out, "message_stop", []byte(`{"type":"message_stop"}`))
}

// End finishes a stream whose provider gave its finish reason but no
// [DONE], as some OpenAI-compatible ones do.
func (s *toMessages) End(out []byte) []byte {
	if s.stop == "" {
		return out
	}
	return s.finish(out)
}

func sseData(out, data []byte) []byte {
	out = append(out, "data: "...)
	out = append(out, data...)
	return append(out, "\n\n"...)
}

func sseEvent(out []byte, name string, data []byte) []byte {
	out = append(out, "event: "...)
	out = append(out, name...)
	out = append(out, '\n')
	return sseData(out, data)
}

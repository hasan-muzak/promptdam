// Package answers reads what a model answered from a response, whole or
// streamed, in the OpenAI Chat Completions or the Anthropic Messages
// format: its text, the tools it called, whether it refused. The router's
// evaluation compares answers; payload capture keeps them.
package answers

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
)

// Answer is what a model answered.
type Answer struct {
	Text  string
	Tools []ToolCall
	// Refused reports the model declined (a refusal, a content filter).
	Refused bool
}

// ToolCall is a tool the model called.
type ToolCall struct {
	Name string
	Args json.RawMessage
}

// Empty reports an answer with nothing in it.
func (a Answer) Empty() bool { return strings.TrimSpace(a.Text) == "" && len(a.Tools) == 0 }

// ErrNoAnswer is a response without any answer in it.
var ErrNoAnswer = errors.New("answers: the response holds no answer")

// Of reads an answer of format f: a whole response, or the events of a
// stream as the client got them.
func Of(f wire.Format, body []byte, stream bool) (Answer, error) {
	switch {
	case stream:
		return streamOf(f, body)
	case f == wire.AnthropicMessages:
		var m struct {
			Content []struct {
				Type  string          `json:"type"`
				Text  string          `json:"text"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"content"`
			StopReason string `json:"stop_reason"`
		}
		if err := json.Unmarshal(body, &m); err != nil {
			return Answer{}, err
		}
		var a Answer
		var text strings.Builder
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				text.WriteString(b.Text)
			case "tool_use":
				a.Tools = append(a.Tools, ToolCall{Name: b.Name, Args: b.Input})
			}
		}
		a.Text, a.Refused = text.String(), m.StopReason == "refusal"
		return a, nil
	case f == wire.OpenAIChat:
		var c struct {
			Choices []struct {
				Message struct {
					Content   *string `json:"content"`
					Refusal   *string `json:"refusal"`
					ToolCalls []struct {
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(body, &c); err != nil {
			return Answer{}, err
		}
		if len(c.Choices) == 0 {
			return Answer{}, ErrNoAnswer
		}
		msg := c.Choices[0].Message
		var a Answer
		if msg.Content != nil {
			a.Text = *msg.Content
		}
		if msg.Refusal != nil && *msg.Refusal != "" {
			a.Text, a.Refused = *msg.Refusal, true
		}
		a.Refused = a.Refused || c.Choices[0].FinishReason == "content_filter"
		for _, tc := range msg.ToolCalls {
			a.Tools = append(a.Tools, ToolCall{Name: tc.Function.Name, Args: json.RawMessage(tc.Function.Arguments)})
		}
		return a, nil
	}
	return Answer{}, errors.New("answers: answers of " + f.String() + " aren't read")
}

// streamAnswer reads the answer a stream of events wrote.
func streamOf(f wire.Format, body []byte) (Answer, error) {
	s := wire.NewScanner(bytes.NewReader(body), 0)
	var a Answer
	var text strings.Builder
	type pending struct {
		name string
		args strings.Builder
	}
	tools := map[int]*pending{}
	for s.Next() {
		ev := s.Event()
		if ev.Partial || len(ev.Data) == 0 || bytes.Equal(ev.Data, []byte("[DONE]")) {
			continue
		}
		switch f {
		case wire.OpenAIChat:
			var c struct {
				Choices []struct {
					Index int `json:"index"`
					Delta struct {
						Content   string `json:"content"`
						Refusal   string `json:"refusal"`
						ToolCalls []struct {
							Index    int `json:"index"`
							Function struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							} `json:"function"`
						} `json:"tool_calls"`
					} `json:"delta"`
					FinishReason string `json:"finish_reason"`
				} `json:"choices"`
			}
			if json.Unmarshal(ev.Data, &c) != nil {
				continue
			}
			for _, ch := range c.Choices {
				if ch.Index != 0 {
					continue
				}
				text.WriteString(ch.Delta.Content)
				if ch.Delta.Refusal != "" {
					text.WriteString(ch.Delta.Refusal)
					a.Refused = true
				}
				for _, tc := range ch.Delta.ToolCalls {
					t := tools[tc.Index]
					if t == nil {
						t = &pending{}
						tools[tc.Index] = t
					}
					if t.name == "" {
						t.name = tc.Function.Name
					}
					t.args.WriteString(tc.Function.Arguments)
				}
				a.Refused = a.Refused || ch.FinishReason == "content_filter"
			}
		case wire.AnthropicMessages:
			var e struct {
				Type         string `json:"type"`
				Index        int    `json:"index"`
				ContentBlock *struct {
					Type string `json:"type"`
					Name string `json:"name"`
				} `json:"content_block"`
				Delta *struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
					StopReason  string `json:"stop_reason"`
				} `json:"delta"`
			}
			if json.Unmarshal(ev.Data, &e) != nil {
				continue
			}
			switch {
			case e.Type == "content_block_start" && e.ContentBlock != nil && e.ContentBlock.Type == "tool_use":
				tools[e.Index] = &pending{name: e.ContentBlock.Name}
			case e.Type == "content_block_delta" && e.Delta != nil && e.Delta.Type == "text_delta":
				text.WriteString(e.Delta.Text)
			case e.Type == "content_block_delta" && e.Delta != nil && e.Delta.Type == "input_json_delta":
				if t := tools[e.Index]; t != nil {
					t.args.WriteString(e.Delta.PartialJSON)
				}
			case e.Type == "message_delta" && e.Delta != nil:
				a.Refused = a.Refused || e.Delta.StopReason == "refusal"
			}
		default:
			return Answer{}, errors.New("answers: streams of " + f.String() + " aren't read")
		}
	}
	a.Text = text.String()
	idx := make([]int, 0, len(tools))
	for i := range tools {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for _, i := range idx {
		args := tools[i].args.String()
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		a.Tools = append(a.Tools, ToolCall{Name: tools[i].name, Args: json.RawMessage(args)})
	}
	return a, nil
}

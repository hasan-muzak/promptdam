package gemini

import (
	"encoding/json"
	"strings"
)

// Stream turns a Chat Completions stream into Gemini's, one Chat event at
// a time: server-sent events (alt=sse, as the SDKs ask), or the JSON array
// Google streams without it. Text goes out as it comes; a function call's
// arguments arrive in pieces and go out whole, as Gemini sends them, on
// the last chunk, which also carries the finish reason and the usage.
type Stream struct {
	sse       bool
	opened    bool // the array's bracket is out
	sent      bool // a chunk is out
	id, model string
	calls     map[int]map[int]*pendingCall // by choice, by the call's index
	finish    map[int]string               // finish reasons, by choice
	usage     *gUsage
	final     bool // the last chunk is out
	failed    bool
}

type pendingCall struct {
	id, name, signature string
	args                strings.Builder
}

// NewStream returns a Stream writing server-sent events (sse) or a JSON
// array.
func NewStream(sse bool) *Stream {
	return &Stream{sse: sse, calls: map[int]map[int]*pendingCall{}, finish: map[int]string{}}
}

// ContentType is the stream's.
func (s *Stream) ContentType() string {
	if s.sse {
		return "text/event-stream"
	}
	return "application/json"
}

// The Chat chunk, as far as Gemini's reads it.
type chatChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content   json.RawMessage `json:"content"`
			Refusal   *string         `json:"refusal"`
			ToolCalls []struct {
				Index    int         `json:"index"`
				ID       string      `json:"id"`
				Function chatFuncArg `json:"function"`
				Extra    *chatExtras `json:"extra_content"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

type chatFuncArg struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Event writes the Gemini chunks of one Chat event's data to out.
func (s *Stream) Event(out, data []byte) []byte {
	if s.failed || s.final || len(data) == 0 || string(data) == "[DONE]" {
		return out
	}
	var c chatChunk
	if err := json.Unmarshal(data, &c); err != nil {
		return s.fail(out, 500, "a chunk of the answer doesn't read")
	}
	if c.Error != nil {
		return s.fail(out, streamStatus(c.Error.Type), c.Error.Message)
	}
	if s.id == "" {
		s.id = c.ID
	}
	if s.model == "" {
		s.model = c.Model
	}
	if c.Usage != nil {
		s.usage = usageOf(c.Usage)
	}
	for _, ch := range c.Choices {
		text := contentText(ch.Delta.Content)
		if ch.Delta.Refusal != nil {
			text += *ch.Delta.Refusal
		}
		if text != "" {
			out = s.chunk(out, gResponse{Candidates: []gCandidate{{Content: gContent{Role: "model",
				Parts: []gPart{textPart(text)}}, Index: ch.Index}}})
		}
		for _, tc := range ch.Delta.ToolCalls {
			calls := s.calls[ch.Index]
			if calls == nil {
				calls = map[int]*pendingCall{}
				s.calls[ch.Index] = calls
			}
			p := calls[tc.Index]
			if p == nil {
				p = &pendingCall{}
				calls[tc.Index] = p
			}
			if p.id == "" {
				p.id = tc.ID
			}
			if p.name == "" {
				p.name = tc.Function.Name
			}
			if tc.Extra != nil && p.signature == "" {
				p.signature = tc.Extra.Google.ThoughtSignature
			}
			p.args.WriteString(tc.Function.Arguments)
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			s.finish[ch.Index] = finishReason(*ch.FinishReason)
		}
	}
	if len(s.finish) > 0 && s.usage != nil {
		out = s.last(out)
	}
	return out
}

// End writes what the stream holds back: its last chunk, if the usage
// never came, and the array's closing bracket.
func (s *Stream) End(out []byte) []byte {
	if !s.failed && !s.final && (len(s.finish) > 0 || s.sent || len(s.calls) > 0) {
		out = s.last(out)
	}
	if !s.sse {
		if !s.opened {
			out = append(out, '[')
		}
		out = append(out, ']')
	}
	return out
}

// last is the last chunk: each answer's function calls and finish reason,
// and the usage.
func (s *Stream) last(out []byte) []byte {
	s.final = true
	r := gResponse{UsageMetadata: s.usage}
	choices := map[int]bool{}
	for i := range s.finish {
		choices[i] = true
	}
	for i := range s.calls {
		choices[i] = true
	}
	if len(choices) == 0 {
		choices[0] = true
	}
	for _, i := range sortedKeys(choices) {
		var parts []gPart
		calls := s.calls[i]
		for _, j := range sortedKeys(calls) {
			p := calls[j]
			extra := signature(p.signature)
			parts = append(parts, callPart(p.id, p.name, p.args.String(), extra))
		}
		reason, ok := s.finish[i]
		if !ok {
			reason = "STOP"
		}
		r.Candidates = append(r.Candidates, candidate(i, parts, reason))
	}
	return s.chunk(out, r)
}

// chunk writes one GenerateContentResponse.
func (s *Stream) chunk(out []byte, r gResponse) []byte {
	r.ModelVersion, r.ResponseID = s.model, s.id
	b, err := marshal(r)
	if err != nil {
		return out
	}
	return s.frame(out, b)
}

// fail ends the stream with an error, as Google's does.
func (s *Stream) fail(out []byte, status int, msg string) []byte {
	s.failed = true
	return s.frame(out, Error(status, msg))
}

func (s *Stream) frame(out, b []byte) []byte {
	s.sent = true
	if s.sse {
		out = append(out, "data: "...)
		out = append(out, b...)
		return append(out, "\n\n"...)
	}
	if s.opened {
		out = append(out, ",\r\n"...)
	} else {
		out = append(out, '[')
		s.opened = true
	}
	return append(out, b...)
}

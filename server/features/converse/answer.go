package converse

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
)

// The Messages answer, as Converse's reads it.
type mAnswer struct {
	Content []struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
		Thinking  string          `json:"thinking"`
		Signature string          `json:"signature"`
		Data      string          `json:"data"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      mUsage `json:"usage"`
}

// mUsage is Messages' usage; input_tokens leaves out the cache's reads and
// writes, as Converse's inputTokens does.
type mUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheCreation            *struct {
		FiveMinutes int64 `json:"ephemeral_5m_input_tokens"`
		OneHour     int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

// add takes the counts u has: a stream's message_start has the input's,
// its message_delta the output's (and, lately, the input's again).
func (u *mUsage) add(o mUsage) {
	if o.InputTokens > 0 {
		u.InputTokens = o.InputTokens
	}
	if o.OutputTokens > 0 {
		u.OutputTokens = o.OutputTokens
	}
	if o.CacheReadInputTokens > 0 {
		u.CacheReadInputTokens = o.CacheReadInputTokens
	}
	if o.CacheCreationInputTokens > 0 {
		u.CacheCreationInputTokens = o.CacheCreationInputTokens
	}
	if o.CacheCreation != nil {
		u.CacheCreation = o.CacheCreation
	}
}

// cUsageOut is Converse's usage, as an answer carries it.
type cUsageOut struct {
	InputTokens           int64          `json:"inputTokens"`
	OutputTokens          int64          `json:"outputTokens"`
	TotalTokens           int64          `json:"totalTokens"`
	CacheReadInputTokens  int64          `json:"cacheReadInputTokens,omitempty"`
	CacheWriteInputTokens int64          `json:"cacheWriteInputTokens,omitempty"`
	CacheDetails          []cCacheDetail `json:"cacheDetails,omitempty"`
}

type cCacheDetail struct {
	TTL         string `json:"ttl"`
	InputTokens int64  `json:"inputTokens"`
}

func (u mUsage) converse() cUsageOut {
	out := cUsageOut{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
		CacheReadInputTokens: u.CacheReadInputTokens, CacheWriteInputTokens: u.CacheCreationInputTokens,
		TotalTokens: u.InputTokens + u.OutputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens}
	if c := u.CacheCreation; c != nil {
		if c.FiveMinutes > 0 {
			out.CacheDetails = append(out.CacheDetails, cCacheDetail{TTL: "5m", InputTokens: c.FiveMinutes})
		}
		if c.OneHour > 0 {
			out.CacheDetails = append(out.CacheDetails, cCacheDetail{TTL: "1h", InputTokens: c.OneHour})
		}
	}
	return out
}

type cMetrics struct {
	LatencyMs int64 `json:"latencyMs"`
}

// converseStop is Converse's stop reason for Messages'.
func converseStop(s string) string {
	switch s {
	case "end_turn", "tool_use", "max_tokens", "stop_sequence", "model_context_window_exceeded":
		return s
	case "refusal":
		return "content_filtered"
	}
	return "end_turn"
}

// ToConverse turns a Messages answer into Converse's, with the call's
// latency.
func ToConverse(body []byte, latency time.Duration) ([]byte, error) {
	var in mAnswer
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	content := []cBlock{}
	for _, b := range in.Content {
		switch b.Type {
		case "text":
			text := b.Text
			content = append(content, cBlock{Text: &text})
		case "tool_use":
			input := b.Input
			if len(input) == 0 || string(input) == "null" {
				input = json.RawMessage(`{}`)
			}
			content = append(content, cBlock{ToolUse: &cToolUse{ToolUseID: b.ID, Name: b.Name, Input: input}})
		case "thinking":
			content = append(content, cBlock{ReasoningContent: &cReasoning{ReasoningText: &cReasoningText{Text: b.Thinking,
				Signature: b.Signature}}})
		case "redacted_thinking":
			content = append(content, cBlock{ReasoningContent: &cReasoning{RedactedContent: b.Data}})
		}
	}
	return jsonOf(cAnswer{Output: cOutput{Message: cOutMessage{Role: "assistant", Content: content}},
		StopReason: converseStop(in.StopReason), Usage: in.Usage.converse(), Metrics: cMetrics{LatencyMs: latency.Milliseconds()}})
}

// cAnswer is the Converse answer a Messages one becomes.
type cAnswer struct {
	Output     cOutput   `json:"output"`
	StopReason string    `json:"stopReason"`
	Usage      cUsageOut `json:"usage"`
	Metrics    cMetrics  `json:"metrics"`
}

type cOutput struct {
	Message cOutMessage `json:"message"`
}

type cOutMessage struct {
	Role    string   `json:"role"`
	Content []cBlock `json:"content"`
}

// TokenCounts are a Messages answer's input (all of it, the cache's
// included) and output tokens, for InvokeModel's headers.
func TokenCounts(body []byte) (input, output int64, ok bool) {
	var in struct {
		Usage *mUsage `json:"usage"`
	}
	if json.Unmarshal(body, &in) != nil || in.Usage == nil {
		return 0, 0, false
	}
	u := in.Usage
	return u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens, u.OutputTokens, true
}

// EventStream turns a Messages stream into AWS's event stream, one
// server-sent event at a time: ConverseStream's events, or
// InvokeModelWithResponseStream's chunks (each Messages event, base64).
type EventStream struct {
	invoke  bool
	start   time.Time
	first   time.Duration // to the first event, for InvokeModel's metrics
	enc     *eventstream.Encoder
	buf     bytes.Buffer
	usage   mUsage
	stop    string
	skipped map[int]bool // blocks Converse has no event for (server tools)
	done    bool
}

// NewEventStream returns an EventStream for ConverseStream, or for
// InvokeModelWithResponseStream (invoke); start is when the call began.
func NewEventStream(invoke bool, start time.Time) *EventStream {
	return &EventStream{invoke: invoke, start: start, enc: eventstream.NewEncoder(), skipped: map[int]bool{}}
}

// ContentType is the stream's.
const ContentType = "application/vnd.amazon.eventstream"

// The Messages stream's events, as far as Converse's reads them.
type mEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Block *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
		Data string `json:"data"`
	} `json:"content_block"`
	Delta *struct {
		Type        string  `json:"type"`
		Text        *string `json:"text"`
		PartialJSON *string `json:"partial_json"`
		Thinking    *string `json:"thinking"`
		Signature   *string `json:"signature"`
		StopReason  string  `json:"stop_reason"`
	} `json:"delta"`
	Message *struct {
		Usage mUsage `json:"usage"`
	} `json:"message"`
	Usage *mUsage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Event writes the frames of one Messages event (its name and data).
func (s *EventStream) Event(out []byte, name string, data []byte) []byte {
	if s.done || name == "ping" || len(data) == 0 {
		return out
	}
	if s.first == 0 {
		s.first = time.Since(s.start)
	}
	var e mEvent
	if err := json.Unmarshal(data, &e); err != nil {
		return s.exception(out, "internalServerException", "an event of the answer doesn't read")
	}
	if name == "" {
		name = e.Type
	}
	switch name {
	case "message_start":
		if e.Message != nil {
			s.usage.add(e.Message.Usage)
		}
	case "message_delta":
		if e.Delta != nil && e.Delta.StopReason != "" {
			s.stop = e.Delta.StopReason
		}
		if e.Usage != nil {
			s.usage.add(*e.Usage)
		}
	case "error":
		kind, msg := "internalServerException", "the provider's stream failed"
		if e.Error != nil {
			kind, msg = streamException(e.Error.Type), e.Error.Message
		}
		return s.exception(out, kind, msg)
	}
	if s.invoke {
		return s.chunk(out, name, data)
	}
	return s.converse(out, name, e)
}

// converse writes ConverseStream's events for one Messages event.
func (s *EventStream) converse(out []byte, name string, e mEvent) []byte {
	type m = map[string]any
	switch name {
	case "message_start":
		return s.event(out, "messageStart", m{"role": "assistant"})
	case "content_block_start":
		if e.Block == nil {
			return out
		}
		switch e.Block.Type {
		case "tool_use":
			return s.event(out, "contentBlockStart", m{"contentBlockIndex": e.Index,
				"start": m{"toolUse": m{"toolUseId": e.Block.ID, "name": e.Block.Name}}})
		case "redacted_thinking":
			return s.event(out, "contentBlockDelta", m{"contentBlockIndex": e.Index,
				"delta": m{"reasoningContent": m{"redactedContent": e.Block.Data}}})
		case "text", "thinking": // Converse opens these with their first delta
		default:
			s.skipped[e.Index] = true
		}
	case "content_block_delta":
		if e.Delta == nil || s.skipped[e.Index] {
			return out
		}
		var delta m
		switch d := e.Delta; {
		case d.Text != nil:
			delta = m{"text": *d.Text}
		case d.PartialJSON != nil:
			delta = m{"toolUse": m{"input": *d.PartialJSON}}
		case d.Thinking != nil:
			delta = m{"reasoningContent": m{"text": *d.Thinking}}
		case d.Signature != nil:
			delta = m{"reasoningContent": m{"signature": *d.Signature}}
		default:
			return out
		}
		return s.event(out, "contentBlockDelta", m{"contentBlockIndex": e.Index, "delta": delta})
	case "content_block_stop":
		if !s.skipped[e.Index] {
			return s.event(out, "contentBlockStop", m{"contentBlockIndex": e.Index})
		}
	case "message_stop":
		return s.end(out)
	}
	return out
}

// end writes messageStop and the metadata with the usage.
func (s *EventStream) end(out []byte) []byte {
	s.done = true
	out = s.event(out, "messageStop", map[string]any{"stopReason": converseStop(s.stop)})
	return s.event(out, "metadata", map[string]any{"usage": s.usage.converse(),
		"metrics": cMetrics{LatencyMs: time.Since(s.start).Milliseconds()}})
}

// chunk writes InvokeModelWithResponseStream's chunk of one Messages
// event: its data, base64; the last one carries Bedrock's invocation
// metrics.
func (s *EventStream) chunk(out []byte, name string, data []byte) []byte {
	if name == "message_stop" {
		s.done = true
		metrics := map[string]int64{"inputTokenCount": s.usage.InputTokens, "outputTokenCount": s.usage.OutputTokens,
			"invocationLatency": time.Since(s.start).Milliseconds(), "firstByteLatency": s.first.Milliseconds()}
		if s.usage.CacheReadInputTokens > 0 {
			metrics["cacheReadInputTokenCount"] = s.usage.CacheReadInputTokens
		}
		if s.usage.CacheCreationInputTokens > 0 {
			metrics["cacheWriteInputTokenCount"] = s.usage.CacheCreationInputTokens
		}
		v, _ := json.Marshal(metrics)
		var err error
		if data, err = setKey(data, "amazon-bedrock-invocationMetrics", v); err != nil {
			return out
		}
	}
	payload, _ := json.Marshal(map[string]string{"bytes": base64.StdEncoding.EncodeToString(data)})
	return s.frame(out, eventstream.Headers{
		{Name: ":message-type", Value: eventstream.StringValue("event")},
		{Name: ":event-type", Value: eventstream.StringValue("chunk")},
		{Name: ":content-type", Value: eventstream.StringValue("application/json")},
	}, payload)
}

// End writes what a stream that never stopped (no message_stop) holds:
// its stop, when the stop reason came.
func (s *EventStream) End(out []byte) []byte {
	if s.done || s.stop == "" || s.invoke {
		return out
	}
	return s.end(out)
}

// event writes one ConverseStream event.
func (s *EventStream) event(out []byte, name string, payload any) []byte {
	b, err := jsonOf(payload)
	if err != nil {
		return out
	}
	return s.frame(out, eventstream.Headers{
		{Name: ":message-type", Value: eventstream.StringValue("event")},
		{Name: ":event-type", Value: eventstream.StringValue(name)},
		{Name: ":content-type", Value: eventstream.StringValue("application/json")},
	}, b)
}

// exception ends the stream with an exception, kind named as the SDKs look
// it up (camelCase).
func (s *EventStream) exception(out []byte, kind, msg string) []byte {
	s.done = true
	b, _ := json.Marshal(map[string]string{"message": msg})
	return s.frame(out, eventstream.Headers{
		{Name: ":message-type", Value: eventstream.StringValue("exception")},
		{Name: ":exception-type", Value: eventstream.StringValue(kind)},
		{Name: ":content-type", Value: eventstream.StringValue("application/json")},
	}, b)
}

func (s *EventStream) frame(out []byte, h eventstream.Headers, payload []byte) []byte {
	s.buf.Reset()
	if err := s.enc.Encode(&s.buf, eventstream.Message{Headers: h, Payload: payload}); err != nil {
		return out
	}
	return append(out, s.buf.Bytes()...)
}

// streamException is the exception for an error event's type.
func streamException(typ string) string {
	switch typ {
	case "rate_limit_error":
		return "throttlingException"
	case "overloaded_error":
		return "serviceUnavailableException"
	case "invalid_request_error":
		return "validationException"
	}
	return "internalServerException"
}

// setKey sets a top-level key of a JSON object.
func setKey(data []byte, key string, value json.RawMessage) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, err
	}
	obj[key] = value
	return jsonOf(obj)
}

// ClientError is an error answer (the gateway's, a provider's, in
// Messages' shape or not) as AWS's SDKs read one: the status (Anthropic's
// 529 as 503), the X-Amzn-ErrorType they name the exception by, and
// {"message"}.
func ClientError(status int, body []byte) (int, string, []byte) {
	if status == 529 {
		status = http.StatusServiceUnavailable
	}
	var e struct {
		Message string `json:"message"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := ""
	if json.Unmarshal(body, &e) == nil {
		msg = e.Message
		if e.Error != nil && e.Error.Message != "" {
			msg = e.Error.Message
		}
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	out, _ := json.Marshal(map[string]string{"message": msg})
	return status, exceptionOf(status), out
}

// exceptionOf is the exception AWS's SDKs know for a status.
func exceptionOf(status int) string {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "AccessDeniedException"
	case status == http.StatusNotFound:
		return "ResourceNotFoundException"
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return "ModelTimeoutException"
	case status == http.StatusTooManyRequests:
		return "ThrottlingException"
	case status == http.StatusBadGateway || status == http.StatusServiceUnavailable:
		return "ServiceUnavailableException"
	case status >= 500:
		return "InternalServerException"
	}
	return "ValidationException"
}

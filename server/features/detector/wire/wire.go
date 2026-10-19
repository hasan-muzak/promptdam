// Package wire is what the gateway and the detector service speak: the
// service runs apart (its own module, ONNX Runtime through cgo, which the
// gateway's static build doesn't take), and imports only this.
package wire

// Priorities of a request: a call waiting on the answer goes first.
const (
	PrioritySync       = "sync"
	PriorityBackground = "background"
)

// SpansRequest asks for the spans of texts.
type SpansRequest struct {
	Priority string `json:"priority"`
	// DeadlineMS is how long the caller waits; the service drops what it
	// can't answer by then.
	DeadlineMS int64  `json:"deadline_ms"`
	Texts      []Text `json:"texts"`
}

// Text is one text to read.
type Text struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// SpansResponse answers a SpansRequest.
type SpansResponse struct {
	// Model is the model's version (a hash of its weights and labels):
	// spans of one version are cached apart from another's.
	Model string     `json:"model"`
	Texts []TextSpan `json:"texts"`
}

// TextSpan is one text's spans.
type TextSpan struct {
	ID    string `json:"id"`
	Spans []Span `json:"spans"`
}

// Span is one finding: text[Start:End] in UTF-8 bytes, a firewall label
// (the model's own labels are mapped by the service), and a score. The
// service returns every label it is at least 0.3 sure of; projects choose
// what to act on, so a setting changed never needs a text read again.
type Span struct {
	Start int     `json:"start"`
	End   int     `json:"end"`
	Label string  `json:"label"`
	Score float64 `json:"score"`
}

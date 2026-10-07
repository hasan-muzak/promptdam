package gateway

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/hasan-muzak/keel/pkg/ids"
	"github.com/hasan-muzak/keel/pkg/llm/wire"

	"github.com/hasan-muzak/keel/products/promptdam/server/brand"
)

// Error types, as both OpenAI and Anthropic name them, so SDKs map the
// gateway's own errors to their usual exceptions.
const (
	TypeInvalidRequest = "invalid_request_error"
	TypeAuthentication = "authentication_error"
	TypePermission     = "permission_error"
	TypeNotFound       = "not_found_error"
	TypeTooLarge       = "request_too_large"
	TypeRateLimit      = "rate_limit_error"
	TypeAPI            = "api_error"
	TypeOverloaded     = "overloaded_error"
)

// Error is a call the gateway refuses or can't complete. Resolvers return
// it to refuse a call; its Code is stable for clients to branch on.
type Error struct {
	Status  int
	Type    string
	Code    string
	Message string
	// RetryAfter is sent as Retry-After when set.
	RetryAfter time.Duration
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// writeError answers with e in the error shape of the client's API.
func writeError(w http.ResponseWriter, f wire.Format, e *Error) {
	var body any
	if f.Anthropic() {
		body = map[string]any{"type": "error", "error": map[string]string{"type": e.Type, "message": e.Message}}
	} else {
		body = map[string]any{"error": map[string]any{"message": e.Message, "type": e.Type, "code": e.Code, "param": nil}}
	}
	out, _ := json.Marshal(body)
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set(brand.HeaderErrorCode, e.Code)
	if e.RetryAfter > 0 {
		h.Set("Retry-After", strconv.Itoa(int((e.RetryAfter+time.Second-1)/time.Second)))
	}
	w.WriteHeader(e.Status)
	_, _ = w.Write(out)
}

// newTrace is a call ID: a TypeID, time-ordered.
func newTrace() string { return ids.Encode("req", ids.New()) }

package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/server/brand"
)

func (h *Handler) clientKey(r *http.Request) string {
	if h.cfg.ClientKey == nil {
		return ""
	}
	return h.cfg.ClientKey(r)
}

// models answers GET /v1/models with the models the caller may use, in
// OpenAI's shape, or Anthropic's when the client sends anthropic-version
// (as Anthropic's SDKs do).
func (h *Handler) models(w http.ResponseWriter, r *http.Request) {
	format := wire.OpenAIChat
	if r.Header.Get("Anthropic-Version") != "" {
		format = wire.AnthropicMessages
	}
	trace := h.cfg.NewTrace()
	w.Header().Set(brand.HeaderTrace, trace)
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, format, &Error{Status: http.StatusMethodNotAllowed, Type: TypeInvalidRequest, Code: "method_not_allowed",
			Message: "/v1/models accepts GET only"})
		return
	}
	lister, ok := h.cfg.Resolver.(ModelLister)
	if !ok {
		writeError(w, format, &Error{Status: http.StatusNotFound, Type: TypeNotFound, Code: "unknown_endpoint",
			Message: "GET /v1/models is not available"})
		return
	}
	models, err := lister.Models(r.Context(), &Call{Format: format, Header: r.Header, Trace: trace, Client: h.clientKey(r)})
	if err != nil {
		var e *Error
		if !errors.As(err, &e) {
			e = &Error{Status: http.StatusInternalServerError, Type: TypeAPI, Code: "internal_error", Message: "the gateway failed"}
		}
		writeError(w, format, e)
		return
	}
	var body any
	if format.Anthropic() {
		data := make([]map[string]any, len(models))
		for i, m := range models {
			data[i] = map[string]any{"type": "model", "id": m.ID, "display_name": m.ID, "created_at": time.Unix(0, 0).UTC()}
		}
		page := map[string]any{"data": data, "has_more": false, "first_id": nil, "last_id": nil}
		if len(models) > 0 {
			page["first_id"], page["last_id"] = models[0].ID, models[len(models)-1].ID
		}
		body = page
	} else {
		data := make([]map[string]any, len(models))
		for i, m := range models {
			data[i] = map[string]any{"id": m.ID, "object": "model", "created": 0, "owned_by": m.Provider}
		}
		body = map[string]any{"object": "list", "data": data}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

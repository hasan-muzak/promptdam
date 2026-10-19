// Package detector is the detector service: it reads texts with the
// firewall's model (ONNX Runtime) and answers their spans. Gateways call it
// on their private network (server/features/detector is their client).
package detector

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"expvar"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/hasan-muzak/promptdam/detector/engine"
	"github.com/hasan-muzak/promptdam/server/features/detector/wire"
)

// Limits of a request.
const (
	maxBody  = 8 << 20
	maxWait  = 10 * time.Second
	maxTexts = 4096
)

// Server answers /v1/spans with an Engine.
type Server struct {
	Engine  *engine.Engine
	Version string
	// Token is the bearer token callers must send: the texts are raw
	// personal data.
	Token string
	ready atomic.Bool
}

// Handler is the service's routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/spans", s.spans)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}`)) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !s.ready.Load() {
			http.Error(w, `{"status":"warming up"}`, http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ready","model":"` + s.Version + `"}`))
	})
	mux.Handle("GET /debug/vars", expvar.Handler())
	return mux
}

// WarmUp reads a text once, so the first call doesn't pay the model's
// first run; the service is ready after.
func (s *Server) WarmUp(ctx context.Context) error {
	_, err := s.Engine.Read(ctx, []string{"Warm up: Ayşe Yılmaz, ayse@example.com."}, wire.PrioritySync)
	if err == nil {
		s.ready.Store(true)
	}
	return err
}

func (s *Server) spans(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req wire.SpansRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req); err != nil || len(req.Texts) > maxTexts {
		http.Error(w, "the request is not a spans request", http.StatusBadRequest)
		return
	}
	wait := maxWait
	if d := time.Duration(req.DeadlineMS) * time.Millisecond; d > 0 && d < wait {
		wait = d
	}
	ctx, cancel := context.WithTimeout(r.Context(), wait)
	defer cancel()
	texts := make([]string, len(req.Texts))
	for i, t := range req.Texts {
		texts[i] = t.Text
	}
	spans, err := s.Engine.Read(ctx, texts, req.Priority)
	switch {
	case errors.Is(err, engine.ErrBusy):
		w.Header().Set("Retry-After", "1")
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	case errors.Is(err, context.DeadlineExceeded):
		http.Error(w, "too late", http.StatusGatewayTimeout)
		return
	case err != nil:
		http.Error(w, "the model failed", http.StatusInternalServerError)
		return
	}
	out := wire.SpansResponse{Model: s.Version, Texts: make([]wire.TextSpan, len(req.Texts))}
	for i, t := range req.Texts {
		out.Texts[i] = wire.TextSpan{ID: t.ID, Spans: spans[i]}
		if out.Texts[i].Spans == nil {
			out.Texts[i].Spans = []wire.Span{}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) authorized(r *http.Request) bool {
	if s.Token == "" {
		return true // development only: main refuses an empty token otherwise
	}
	got := r.Header.Get("Authorization")
	return subtle.ConstantTimeCompare([]byte(got), []byte("Bearer "+s.Token)) == 1
}

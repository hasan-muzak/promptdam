// Command gateway is Promptdam's data plane (gateway.<domain>): the
// OpenAI- and Anthropic-compatible endpoints clients point their base URL
// at. It is a plain net/http server, without the control plane's framework,
// because it sits on the path of every LLM call.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hasan-muzak/keel/pkg/keel"

	"github.com/hasan-muzak/keel/products/promptdam/server/core"
)

func main() {
	if err := run(); err != nil {
		slog.Error("promptdam gateway", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := core.Load()
	if err != nil {
		return err
	}
	cfg.Service = "promptdam-gateway"
	c, err := keel.Open(ctx, cfg.Config, core.Product)
	if err != nil {
		return err
	}
	defer c.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}`)) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := c.KV.Ping(r.Context()); err != nil {
			http.Error(w, `{"status":"valkey unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	srv := &http.Server{
		Addr:              cfg.GatewayAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// Request bodies (long prompts, images) arrive within a minute.
		ReadTimeout: 60 * time.Second,
		// No write timeout: a streamed answer lasts as long as the model
		// writes. The gateway bounds each upstream call itself.
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 64 << 10,
	}
	errc := make(chan error, 1)
	go func() {
		slog.Info("promptdam gateway listening", "addr", cfg.GatewayAddr)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	// In-flight streams get time to finish before the process exits.
	sctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return srv.Shutdown(sctx)
}

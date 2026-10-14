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

	"github.com/hasan-muzak/keel-core/pkg/config"
	"github.com/hasan-muzak/keel-core/pkg/counter"
	"github.com/hasan-muzak/keel-core/pkg/keel"
	"github.com/hasan-muzak/keel-core/pkg/safehttp"

	"github.com/hasan-muzak/promptdam/server/core"
	"github.com/hasan-muzak/promptdam/server/features/conversation"
	"github.com/hasan-muzak/promptdam/server/features/decide"
	"github.com/hasan-muzak/promptdam/server/features/evaluate"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
	"github.com/hasan-muzak/promptdam/server/features/requestlog"
	"github.com/hasan-muzak/promptdam/server/features/resolver"
	"github.com/hasan-muzak/promptdam/server/features/responsecache"
	"github.com/hasan-muzak/promptdam/server/features/snapshot"
)

func main() {
	if err := run(); err != nil {
		slog.Error("promptdam gateway", "err", err)
		os.Exit(1)
	}
}

// lifecycle is a background component started before serving and stopped
// (flushed) after.
type lifecycle interface {
	Start(context.Context) error
	Stop(context.Context) error
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
	prices, err := cfg.Prices()
	if err != nil {
		return err
	}
	app := core.NewApp(c, core.WithPrices(prices))

	snapshots := snapshot.NewStore(c.KV, app.Sources())
	rates := counter.New(c.KV, "rpm", counter.Options{})
	// Providers' base URLs are public addresses; a local stack's fake
	// provider runs on this machine.
	transport := safehttp.NewTransport(safehttp.TransportOptions{AllowLoopback: cfg.Env == config.Development})
	// Conversations' turns, shared by every gateway replica through Valkey.
	turns := conversation.New(c.KV, conversation.Options{})
	res := resolver.New(resolver.Config{
		Keys: c.APIKeys, Snapshots: snapshots, Quota: c.Metering, Spend: app.Spend, Rates: rates, Prices: app.Prices,
		Scope: core.ScopeGateway, Rules: app.RuleEnv, Entitlements: c.Metering.Entitlements, Decider: decide.New(transport),
		Turns: turns,
	})
	log := requestlog.New(requestlog.Config{
		Events: c.Events, Prices: app.Prices, Spend: app.Spend, Entitlements: c.Metering.Entitlements, Payloads: app.PayloadStore,
		Turns: turns,
	})
	evalSpend := counter.New(c.KV, "evalspend", counter.Options{})
	eval := evaluate.New(evaluate.Config{
		Resolver: res, Transport: transport, Events: c.Events, Prices: app.Prices, Spend: app.Spend, Counters: evalSpend,
		Entitlements: c.Metering.Entitlements,
	})
	h := gateway.New(gateway.Config{
		Resolver: res,
		// Every call is costed, counted against budgets and logged; a
		// router's sampled calls are evaluated in the background.
		Done: func(r *gateway.Record) {
			log.Record(r)
			eval.Offer(r)
		},
		Transport: transport,
		ClientKey: c.ClientIP.Key,
		Cache:     responsecache.New(c.KV),
	})

	// Prices are loaded and kept current, usage, spend and rate counters
	// flushed in the background, key last-used times and log rows batched,
	// sampled calls evaluated, and configuration changes listened for.
	// The payload store starts before the log and stops after it: the
	// log's last rows add the last payloads.
	for _, l := range []lifecycle{app.Prices, c.Metering, c.APIKeys.Lifecycle(), app.SpendCounters, rates, evalSpend, snapshots,
		turns, app.PayloadStore, log, eval} {
		if err := l.Start(ctx); err != nil {
			return err
		}
		defer func() {
			sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = l.Stop(sctx)
		}()
	}

	mux := http.NewServeMux()
	mux.Handle("/v1/", h)
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

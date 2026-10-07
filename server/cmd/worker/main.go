// Command worker runs Promptdam's background jobs: Stripe events, emails,
// usage rollups and reports, and webhook deliveries.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/hasan-muzak/keel/pkg/keel"

	"github.com/hasan-muzak/keel/products/promptdam/server/core"
)

func main() {
	if err := run(); err != nil {
		slog.Error("promptdam worker", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := core.Load()
	if err != nil {
		return err
	}
	cfg.Service = "promptdam-worker"
	c, err := keel.Open(context.Background(), cfg.Config, core.Product)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Worker(core.Version).RunSignals()
}

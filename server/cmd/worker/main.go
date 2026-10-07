// Command worker runs Promptdam's background jobs: Stripe events, emails,
// usage rollups and reports, webhook deliveries, budget alerts, the daily
// price list sync, model routers' classifier training, and the purge of
// deleted organizations' and projects' data.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/hasan-muzak/keel-core/pkg/keel"

	"github.com/hasan-muzak/promptdam/server/core"
	"github.com/hasan-muzak/promptdam/server/features/budgets"
	"github.com/hasan-muzak/promptdam/server/features/prices"
	"github.com/hasan-muzak/promptdam/server/features/purge"
	"github.com/hasan-muzak/promptdam/server/features/training"
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
	app := core.NewApp(c)
	purge.RegisterWorkers(c.Jobs, app.Purge)
	budgets.RegisterWorkers(c.Jobs, app.Budgets)
	prices.RegisterWorkers(c.Jobs, app.Prices)
	training.RegisterWorkers(c.Jobs, app.Training)
	return c.Worker(core.Version).RunSignals()
}

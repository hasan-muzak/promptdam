// Command api is Promptdam's control-plane API (api.<domain>). With
// arguments it runs a maintenance command instead (`api help` lists them).
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/hasan-muzak/keel-core/pkg/billing"
	"github.com/hasan-muzak/keel-core/pkg/cli"
	"github.com/hasan-muzak/keel-core/pkg/keel"

	"github.com/hasan-muzak/promptdam/server/core"
	"github.com/hasan-muzak/promptdam/server/routers"
)

func main() {
	if err := run(); err != nil {
		slog.Error("promptdam api", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()
	// Catalog commands need no database (CI and site builds run them).
	if handled, err := cli.Run(ctx, os.Args[1:], os.Stdout, billing.CatalogCommands(core.Product.Catalog)...); handled {
		return err
	}
	cfg, err := core.Load()
	if err != nil {
		return err
	}
	c, err := keel.Open(ctx, cfg.Config, core.Product)
	if err != nil {
		return err
	}
	defer c.Close()

	prices, err := cfg.Prices()
	if err != nil {
		return err
	}
	app := core.NewApp(c, core.WithPrices(prices), core.WithGatewayURL(cfg.GatewayURL),
		core.WithDetector(cfg.DetectorURL, cfg.DetectorToken))
	if handled, err := cli.Run(ctx, os.Args[1:], os.Stdout, append(c.Commands(), devCommands(c, app)...)...); handled {
		return err
	}
	a := c.API(core.Version)
	routers.Mount(a, app)
	return a.RunSignals()
}

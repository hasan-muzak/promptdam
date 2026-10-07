// Command migrate applies Promptdam's Postgres migrations (core, River and
// the product graph), then its ClickHouse migrations. The deploy runs it
// once before starting the new version.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/hasan-muzak/keel-core/pkg/keel"

	"github.com/hasan-muzak/promptdam/server/core"
)

func main() {
	ctx := context.Background()
	cfg, err := core.Load()
	cfg.QueryTimeoutSeconds = 0 // backfills and index builds take as long as they take
	if err == nil {
		var c *keel.Core
		if c, err = keel.Open(ctx, cfg.Config, core.Product); err == nil {
			err = c.Migrate(ctx)
			c.Close()
		}
	}
	if err != nil {
		slog.Error("promptdam migrate", "err", err)
		os.Exit(1)
	}
	slog.Info("promptdam migrations applied")
}

package analytics_test

import (
	"github.com/hasan-muzak/keel/pkg/events"

	"github.com/hasan-muzak/keel/products/promptdam/server/migrations"
)

func migrationsSet() events.MigrationSet { return migrations.ClickHouse() }

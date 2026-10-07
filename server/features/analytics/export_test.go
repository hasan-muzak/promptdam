package analytics_test

import (
	"github.com/hasan-muzak/keel-core/pkg/events"

	"github.com/hasan-muzak/promptdam/server/migrations"
)

func migrationsSet() events.MigrationSet { return migrations.ClickHouse() }

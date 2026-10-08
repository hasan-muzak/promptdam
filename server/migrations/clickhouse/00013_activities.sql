-- What happened to a project that was worth telling: budget thresholds,
-- routers ready or tiers demoted, the firewall's refusals, alert rules
-- firing and resolving. An append-only feed, kept 90 days (it moved here
-- from Postgres: rows come as often as a minute per project).

-- +goose Up
-- +goose StatementBegin
CREATE TABLE activities (
  project_id UUID,
  ts         DateTime64(3, 'UTC') CODEC(Delta, ZSTD(1)),
  id         UUID,
  org_id     UUID,
  kind       LowCardinality(String),
  severity   LowCardinality(String),
  title      String CODEC(ZSTD(1)),
  body       String CODEC(ZSTD(1)),
  subject    String CODEC(ZSTD(1)),
  url        String CODEC(ZSTD(1)),
  data       String CODEC(ZSTD(3))
) ENGINE = MergeTree
PARTITION BY toMonday(ts)
ORDER BY (project_id, ts, id)
TTL toDateTime(ts) + INTERVAL 1 DAY TO VOLUME 'r2',
    toDateTime(ts) + INTERVAL 90 DAY
SETTINGS storage_policy = 'tiered', ttl_only_drop_parts = 1;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE activities;
-- +goose StatementEnd

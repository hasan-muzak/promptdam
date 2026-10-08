-- The firewall: on each call, the strongest action it took (block,
-- tokenize, redact, alert, or '' when it found nothing) and the detectors
-- it found; and an event per detector and place of each call it found
-- something in, or a policy decided: what was found where, what it got,
-- how many. Never the values: an excerpt, kept only when the project asks,
-- has every finding masked.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE requests
  ADD COLUMN firewall_action LowCardinality(String) AFTER shadow,
  ADD COLUMN firewall_kinds  Array(LowCardinality(String)) AFTER firewall_action;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE firewall_events (
  project_id      UUID,
  ts              DateTime64(3, 'UTC') CODEC(Delta, ZSTD(1)),
  trace           String CODEC(ZSTD(1)),
  org_id          UUID,
  key_id          UUID,
  developer       String CODEC(ZSTD(1)),
  route           LowCardinality(String),
  requested_model LowCardinality(String),
  provider        LowCardinality(String),
  detector        LowCardinality(String),
  class           LowCardinality(String),
  action          LowCardinality(String),
  location        LowCardinality(String),
  count           UInt32,
  blocked         Bool,
  blocked_by      LowCardinality(String),
  policy          LowCardinality(String),
  excerpt         String CODEC(ZSTD(3)),
  retention_days  UInt16
) ENGINE = MergeTree
PARTITION BY (retention_days, toMonday(ts))
ORDER BY (project_id, ts, trace, detector)
TTL toDateTime(ts) + INTERVAL 1 DAY TO VOLUME 'r2',
    toDateTime(ts) + toIntervalDay(retention_days)
SETTINGS storage_policy = 'tiered', ttl_only_drop_parts = 1;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE firewall_events;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE requests DROP COLUMN firewall_kinds, DROP COLUMN firewall_action;
-- +goose StatementEnd

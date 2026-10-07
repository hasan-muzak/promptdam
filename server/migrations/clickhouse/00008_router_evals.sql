-- Model routers' evaluation: for each sampled call and each tier but the
-- default, whether that tier's answer held up against the default tier's
-- (win, tie or loss for the tier), how that was decided, and what the
-- evaluation cost on the project's keys. Never the call's text: its
-- features are hashes of its words and shape, which the classifier
-- learns from.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE router_evals (
  project_id       UUID,
  ts               DateTime64(3, 'UTC') CODEC(Delta, ZSTD(1)),
  org_id           UUID,
  router           LowCardinality(String),
  trace            String CODEC(ZSTD(1)),
  family_id        String CODEC(ZSTD(1)),
  served_tier      LowCardinality(String),
  decided_tier     LowCardinality(String),
  tier_source      LowCardinality(String),
  baseline_tier    LowCardinality(String),
  baseline_model   LowCardinality(String),
  candidate_tier   LowCardinality(String),
  candidate_model  LowCardinality(String),
  candidate_rank   Int8,
  verdict          LowCardinality(String),
  check            LowCardinality(String),
  judge_model      LowCardinality(String),
  consistent       Bool,
  candidate_micros Int64,
  baseline_micros  Int64,
  judge_micros     Int64,
  features         Array(UInt32) CODEC(ZSTD(3)),
  retention_days   UInt16
) ENGINE = MergeTree
PARTITION BY (retention_days, toMonday(ts))
ORDER BY (project_id, router, ts, trace)
TTL toDateTime(ts) + INTERVAL 1 DAY TO VOLUME 'r2',
    toDateTime(ts) + toIntervalDay(retention_days)
SETTINGS storage_policy = 'tiered', ttl_only_drop_parts = 1;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE router_evals;
-- +goose StatementEnd

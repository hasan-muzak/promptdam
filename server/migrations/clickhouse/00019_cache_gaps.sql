-- What the cache lifetime learner (features/cachepolicy) weighs, for calls
-- on Anthropic's own API (the cache that is written, and whose lifetime a
-- request sets).
--
-- cache_gaps_hourly: a family's calls by how long after their
-- conversation's previous call they came (the gateway's chain_gap_ms), in
-- cached tokens: mid_tokens came 5 to 60 minutes later (their cache reads
-- and writes: what an hour would have kept), inc_tokens within five minutes
-- (the tokens each wrote), long_tokens after an hour or first in their
-- conversation (the tokens each wrote either way).
--
-- prefix_starts: each call's start by its prefix (instructions and tools),
-- in prefix order, kept 8 days: the learner reads the gaps between calls
-- sharing a prefix (any conversation's) with a window function the order
-- already serves, and the calls that wrote a prefix within seconds of
-- another without reading it.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE cache_gaps_hourly (
  project_id      UUID,
  hour            DateTime('UTC') CODEC(Delta, ZSTD(1)),
  family_id       LowCardinality(String),
  served_model    LowCardinality(String),
  kind            LowCardinality(String),
  calls           UInt64,
  mid_calls       UInt64,
  mid_tokens      UInt64,
  inc_tokens      UInt64,
  long_tokens     UInt64,
  retention_days  UInt16
) ENGINE = SummingMergeTree((calls, mid_calls, mid_tokens, inc_tokens, long_tokens))
PARTITION BY (retention_days, toMonday(hour))
ORDER BY (project_id, hour, family_id, served_model, kind)
TTL hour + INTERVAL 1 DAY TO VOLUME 'r2',
    hour + toIntervalDay(retention_days)
SETTINGS storage_policy = 'tiered', ttl_only_drop_parts = 1, index_granularity = 256;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE MATERIALIZED VIEW cache_gaps_hourly_mv TO cache_gaps_hourly AS
SELECT project_id, toStartOfHour(ts) AS hour, family_id, served_model, kind, count() AS calls,
       countIf(chain_state = 'continued' AND chain_gap_ms >= 300000 AND chain_gap_ms < 3600000) AS mid_calls,
       sumIf(cache_read_tokens + cache_write_tokens,
             chain_state = 'continued' AND chain_gap_ms >= 300000 AND chain_gap_ms < 3600000) AS mid_tokens,
       sumIf(cache_write_tokens, chain_state = 'continued' AND chain_gap_ms < 300000) AS inc_tokens,
       sumIf(cache_write_tokens, chain_state = 'start' OR chain_gap_ms >= 3600000) AS long_tokens, retention_days
FROM requests
WHERE family_id != '' AND usage_known AND kind = 'anthropic' AND chain_state IN ('start', 'continued')
GROUP BY project_id, hour, family_id, served_model, kind, retention_days;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE prefix_starts (
  project_id          UUID,
  prefix_hash         String,
  ts                  DateTime64(3, 'UTC') CODEC(Delta, ZSTD(1)),
  family_id           LowCardinality(String),
  served_model        LowCardinality(String),
  prefix_tokens       UInt32,
  cache_read_tokens   UInt32,
  cache_write_tokens  UInt32
) ENGINE = MergeTree
ORDER BY (project_id, prefix_hash, ts)
TTL toDateTime(ts) + INTERVAL 8 DAY
SETTINGS ttl_only_drop_parts = 1;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE MATERIALIZED VIEW prefix_starts_mv TO prefix_starts AS
SELECT project_id, prefix_hash, ts, family_id, served_model, prefix_tokens, cache_read_tokens, cache_write_tokens
FROM requests
WHERE prefix_hash != '' AND usage_known AND kind = 'anthropic';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP VIEW prefix_starts_mv;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE prefix_starts;
-- +goose StatementEnd
-- +goose StatementBegin
DROP VIEW cache_gaps_hourly_mv;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE cache_gaps_hourly;
-- +goose StatementEnd

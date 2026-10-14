-- How well each model caches, by what the calls should have read.
--
-- cache_expected_tokens  the cached prefix a call should have read: its
--                        conversation's previous call's input, when that
--                        call asked the same model of the same kind, went
--                        to the same backend with the same strategy, within
--                        the cache's lifetime, long enough to cache (0
--                        otherwise; see requestlog.expected).
-- input_micros           the call's input priced from the price list, cache
--                        reads and writes included: what a strategy is
--                        scored by.
-- cost_reported          cost_micros is the provider's own figure.
-- pooled                 the call went to a provider's own public API (its
--                        kind's base URL, never a custom endpoint): only
--                        such calls teach the caching profiles every
--                        project is planned by, since a project's own
--                        endpoint could answer anything.
--
-- cache_health_hourly: per kind, target model, backend, strategy, trial and pool,
-- the calls, those that expected a cache read and those that got one, the
-- tokens expected and read, and the costs. The learner pools it across
-- projects (cacheprofile); project_id stays so a project's rows go when it
-- does.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE requests
  ADD COLUMN pooled                 Bool AFTER cache_trial,
  ADD COLUMN cost_reported          Bool AFTER computed_micros,
  ADD COLUMN cache_expected_tokens  UInt32 AFTER cache_write_tokens,
  ADD COLUMN input_micros           Int64 AFTER cost_reported;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE cache_health_hourly (
  project_id             UUID,
  hour                   DateTime('UTC') CODEC(Delta, ZSTD(1)),
  kind                   LowCardinality(String),
  target_model           LowCardinality(String),
  backend                LowCardinality(String),
  cache_strategy         LowCardinality(String),
  cache_trial            Bool,
  pooled                 Bool,
  calls                  UInt64,
  expected_calls         UInt64,
  hit_calls              UInt64,
  expected_tokens        UInt64,
  expected_read_tokens   UInt64,
  read_tokens            UInt64,
  write_tokens           UInt64,
  input_tokens           UInt64,
  input_micros           Int64,
  cost_micros            Int64,
  computed_micros        Int64,
  reported_calls         UInt64,
  reported_cost_micros   Int64,
  reported_computed      Int64,
  retention_days         UInt16
) ENGINE = SummingMergeTree((calls, expected_calls, hit_calls, expected_tokens, expected_read_tokens, read_tokens,
  write_tokens, input_tokens, input_micros, cost_micros, computed_micros, reported_calls, reported_cost_micros,
  reported_computed))
PARTITION BY (retention_days, toMonday(hour))
ORDER BY (project_id, hour, kind, target_model, backend, cache_strategy, cache_trial, pooled)
TTL hour + INTERVAL 1 DAY TO VOLUME 'r2',
    hour + toIntervalDay(retention_days)
SETTINGS storage_policy = 'tiered', ttl_only_drop_parts = 1, index_granularity = 256;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE MATERIALIZED VIEW cache_health_hourly_mv TO cache_health_hourly AS
SELECT project_id, toStartOfHour(ts) AS hour, kind, target_model, backend, cache_strategy, cache_trial, pooled,
       count() AS calls,
       countIf(cache_expected_tokens > 0) AS expected_calls,
       countIf(cache_expected_tokens > 0 AND cache_read_tokens > 0) AS hit_calls,
       sum(cache_expected_tokens) AS expected_tokens,
       sumIf(cache_read_tokens, cache_expected_tokens > 0) AS expected_read_tokens,
       sum(cache_read_tokens) AS read_tokens,
       sum(cache_write_tokens) AS write_tokens,
       sum(input_tokens) AS input_tokens,
       sum(input_micros) AS input_micros,
       sum(cost_micros) AS cost_micros,
       sum(computed_micros) AS computed_micros,
       countIf(cost_reported) AS reported_calls,
       -- requests.: the columns, not the sums aliased with their names.
       sumIf(requests.cost_micros, cost_reported) AS reported_cost_micros,
       sumIf(requests.computed_micros, cost_reported) AS reported_computed,
       retention_days
FROM requests
WHERE usage_known AND cache_strategy != '' AND target_model != ''
GROUP BY project_id, hour, kind, target_model, backend, cache_strategy, cache_trial, pooled, retention_days;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP VIEW cache_health_hourly_mv;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE cache_health_hourly;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE requests DROP COLUMN input_micros, DROP COLUMN cache_expected_tokens, DROP COLUMN cost_reported, DROP COLUMN pooled;
-- +goose StatementEnd

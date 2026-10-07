-- Prompt families: each call's family, the hash of its prefix as sent and
-- the changing values its instructions hold, and two rollups:
--
--   families_hourly   calls, errors, tokens, cache reads, spend and latency
--                     per family and model: top families by spend, a
--                     family's model mix, without scanning raw rows
--   prefixes_hourly   calls and cache reads per family and exact prefix,
--                     with the changing values seen: the cache doctor tells
--                     families whose prefix keeps changing (and why) from
--                     those that cache well

-- +goose Up
-- +goose StatementBegin
ALTER TABLE requests
  ADD COLUMN family_id     LowCardinality(String) AFTER route,
  ADD COLUMN prefix_hash   String CODEC(ZSTD(1)) AFTER family_id,
  ADD COLUMN prefix_flags  UInt16 AFTER prefix_hash;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE families_hourly (
  project_id          UUID,
  hour                DateTime('UTC') CODEC(Delta, ZSTD(1)),
  family_id           LowCardinality(String),
  requested_model     LowCardinality(String),
  served_model        LowCardinality(String),
  requests            SimpleAggregateFunction(sum, UInt64),
  errors              SimpleAggregateFunction(sum, UInt64),
  input_tokens        SimpleAggregateFunction(sum, UInt64),
  output_tokens       SimpleAggregateFunction(sum, UInt64),
  cache_read_tokens   SimpleAggregateFunction(sum, UInt64),
  cost_micros         SimpleAggregateFunction(sum, Int64),
  latency_ms          AggregateFunction(quantilesTDigest(0.5, 0.95, 0.99), UInt32),
  retention_days      UInt16
) ENGINE = AggregatingMergeTree
PARTITION BY (retention_days, toMonday(hour))
ORDER BY (project_id, hour, family_id, requested_model, served_model)
TTL hour + INTERVAL 1 DAY TO VOLUME 'r2',
    hour + toIntervalDay(retention_days)
SETTINGS storage_policy = 'tiered', ttl_only_drop_parts = 1, index_granularity = 256;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE MATERIALIZED VIEW families_hourly_mv TO families_hourly AS
SELECT project_id, toStartOfHour(ts) AS hour, family_id, requested_model, served_model,
       count() AS requests, countIf(error_code != '') AS errors,
       sum(input_tokens) AS input_tokens, sum(output_tokens) AS output_tokens,
       sum(cache_read_tokens) AS cache_read_tokens, sum(cost_micros) AS cost_micros,
       quantilesTDigestState(0.5, 0.95, 0.99)(latency_ms) AS latency_ms,
       retention_days
FROM requests
WHERE family_id != ''
GROUP BY project_id, hour, family_id, requested_model, served_model, retention_days;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE prefixes_hourly (
  project_id          UUID,
  hour                DateTime('UTC') CODEC(Delta, ZSTD(1)),
  family_id           LowCardinality(String),
  prefix_hash         String,
  requests            SimpleAggregateFunction(sum, UInt64),
  input_tokens        SimpleAggregateFunction(sum, UInt64),
  cache_read_tokens   SimpleAggregateFunction(sum, UInt64),
  cache_write_tokens  SimpleAggregateFunction(sum, UInt64),
  prefix_flags        SimpleAggregateFunction(groupBitOr, UInt16),
  served_model        SimpleAggregateFunction(any, LowCardinality(String)),
  kind                SimpleAggregateFunction(any, LowCardinality(String)),
  retention_days      UInt16
) ENGINE = AggregatingMergeTree
PARTITION BY (retention_days, toMonday(hour))
ORDER BY (project_id, hour, family_id, prefix_hash)
TTL hour + INTERVAL 1 DAY TO VOLUME 'r2',
    hour + toIntervalDay(retention_days)
SETTINGS storage_policy = 'tiered', ttl_only_drop_parts = 1, index_granularity = 256;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE MATERIALIZED VIEW prefixes_hourly_mv TO prefixes_hourly AS
SELECT project_id, toStartOfHour(ts) AS hour, family_id, prefix_hash,
       count() AS requests, sum(input_tokens) AS input_tokens,
       sum(cache_read_tokens) AS cache_read_tokens, sum(cache_write_tokens) AS cache_write_tokens,
       groupBitOr(prefix_flags) AS prefix_flags, any(served_model) AS served_model, any(kind) AS kind,
       retention_days
FROM requests
WHERE family_id != '' AND usage_known
GROUP BY project_id, hour, family_id, prefix_hash, retention_days;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP VIEW prefixes_hourly_mv;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE prefixes_hourly;
-- +goose StatementEnd
-- +goose StatementBegin
DROP VIEW families_hourly_mv;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE families_hourly;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE requests DROP COLUMN prefix_flags, DROP COLUMN prefix_hash, DROP COLUMN family_id;
-- +goose StatementEnd

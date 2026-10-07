-- The gateway's request log and the rollups analytics read.
--
--   requests                 one row per gateway call (metadata only, never
--                            payloads), kept per the plan's retention; read
--                            by time range and by trace (whose TypeID holds
--                            the call's start, so a lookup reads a few ms of
--                            one project's rows)
--   requests_hourly          calls, errors, tokens, cost and latency
--                            percentiles per provider, model, key and route
--   requests_hourly_developer, requests_hourly_tag
--                            the same sums per developer and per tag
--
-- Rollups are filled by materialized views on insert, so spend and latency
-- endpoints never scan raw rows.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE requests (
  project_id          UUID,
  ts                  DateTime64(3, 'UTC') CODEC(Delta, ZSTD(1)),
  trace               String CODEC(ZSTD(1)),
  org_id              UUID,
  key_id              UUID,
  developer           String CODEC(ZSTD(1)),
  end_user            String CODEC(ZSTD(1)),
  tags                Array(LowCardinality(String)),
  route               LowCardinality(String),
  api_format          LowCardinality(String),
  provider            LowCardinality(String),
  kind                LowCardinality(String),
  requested_model     LowCardinality(String),
  served_model        LowCardinality(String),
  stream              Bool,
  status              UInt16,
  error_code          LowCardinality(String),
  attempts            UInt8,
  ttfb_ms             UInt32,
  latency_ms          UInt32,
  upstream_ms         UInt32,
  usage_known         Bool,
  input_tokens        UInt32,
  output_tokens       UInt32,
  cache_read_tokens   UInt32,
  cache_write_tokens  UInt32,
  reasoning_tokens    UInt32,
  cost_micros         Int64,
  request_bytes       UInt32,
  response_bytes      UInt64,
  retention_days      UInt16
) ENGINE = MergeTree
PARTITION BY (retention_days, toMonday(ts))
ORDER BY (project_id, ts)
TTL toDateTime(ts) + INTERVAL 1 DAY TO VOLUME 'r2',
    toDateTime(ts) + toIntervalDay(retention_days)
SETTINGS storage_policy = 'tiered', ttl_only_drop_parts = 1;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE requests_hourly (
  project_id          UUID,
  hour                DateTime('UTC') CODEC(Delta, ZSTD(1)),
  provider            LowCardinality(String),
  served_model        LowCardinality(String),
  key_id              UUID,
  route               LowCardinality(String),
  requests            SimpleAggregateFunction(sum, UInt64),
  errors              SimpleAggregateFunction(sum, UInt64),
  input_tokens        SimpleAggregateFunction(sum, UInt64),
  output_tokens       SimpleAggregateFunction(sum, UInt64),
  cache_read_tokens   SimpleAggregateFunction(sum, UInt64),
  cache_write_tokens  SimpleAggregateFunction(sum, UInt64),
  cost_micros         SimpleAggregateFunction(sum, Int64),
  latency_ms          AggregateFunction(quantilesTDigest(0.5, 0.95, 0.99), UInt32),
  ttfb_ms             AggregateFunction(quantilesTDigest(0.5, 0.95, 0.99), UInt32),
  retention_days      UInt16
) ENGINE = AggregatingMergeTree
PARTITION BY (retention_days, toMonday(hour))
ORDER BY (project_id, hour, provider, served_model, key_id, route)
TTL hour + INTERVAL 1 DAY TO VOLUME 'r2',
    hour + toIntervalDay(retention_days)
SETTINGS storage_policy = 'tiered', ttl_only_drop_parts = 1, index_granularity = 256;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE MATERIALIZED VIEW requests_hourly_mv TO requests_hourly AS
SELECT project_id, toStartOfHour(ts) AS hour, provider, served_model, key_id, route,
       count() AS requests, countIf(error_code != '') AS errors,
       sum(input_tokens) AS input_tokens, sum(output_tokens) AS output_tokens,
       sum(cache_read_tokens) AS cache_read_tokens, sum(cache_write_tokens) AS cache_write_tokens,
       sum(cost_micros) AS cost_micros,
       quantilesTDigestState(0.5, 0.95, 0.99)(latency_ms) AS latency_ms,
       quantilesTDigestState(0.5, 0.95, 0.99)(ttfb_ms) AS ttfb_ms,
       retention_days
FROM requests
GROUP BY project_id, hour, provider, served_model, key_id, route, retention_days;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE requests_hourly_developer (
  project_id      UUID,
  hour            DateTime('UTC') CODEC(Delta, ZSTD(1)),
  developer       String,
  requests        UInt64,
  errors          UInt64,
  input_tokens    UInt64,
  output_tokens   UInt64,
  cost_micros     Int64,
  retention_days  UInt16
) ENGINE = SummingMergeTree((requests, errors, input_tokens, output_tokens, cost_micros))
PARTITION BY (retention_days, toMonday(hour))
ORDER BY (project_id, hour, developer)
TTL hour + INTERVAL 1 DAY TO VOLUME 'r2',
    hour + toIntervalDay(retention_days)
SETTINGS storage_policy = 'tiered', ttl_only_drop_parts = 1, index_granularity = 256;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE MATERIALIZED VIEW requests_hourly_developer_mv TO requests_hourly_developer AS
SELECT project_id, toStartOfHour(ts) AS hour, developer,
       count() AS requests, countIf(error_code != '') AS errors,
       sum(input_tokens) AS input_tokens, sum(output_tokens) AS output_tokens, sum(cost_micros) AS cost_micros,
       retention_days
FROM requests
WHERE developer != ''
GROUP BY project_id, hour, developer, retention_days;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE requests_hourly_tag (
  project_id      UUID,
  hour            DateTime('UTC') CODEC(Delta, ZSTD(1)),
  tag             LowCardinality(String),
  requests        UInt64,
  errors          UInt64,
  input_tokens    UInt64,
  output_tokens   UInt64,
  cost_micros     Int64,
  retention_days  UInt16
) ENGINE = SummingMergeTree((requests, errors, input_tokens, output_tokens, cost_micros))
PARTITION BY (retention_days, toMonday(hour))
ORDER BY (project_id, hour, tag)
TTL hour + INTERVAL 1 DAY TO VOLUME 'r2',
    hour + toIntervalDay(retention_days)
SETTINGS storage_policy = 'tiered', ttl_only_drop_parts = 1, index_granularity = 256;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE MATERIALIZED VIEW requests_hourly_tag_mv TO requests_hourly_tag AS
SELECT project_id, toStartOfHour(ts) AS hour, arrayJoin(tags) AS tag,
       count() AS requests, countIf(error_code != '') AS errors,
       sum(input_tokens) AS input_tokens, sum(output_tokens) AS output_tokens, sum(cost_micros) AS cost_micros,
       retention_days
FROM requests
WHERE notEmpty(tags)
GROUP BY project_id, hour, tag, retention_days;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP VIEW requests_hourly_tag_mv;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE requests_hourly_tag;
-- +goose StatementEnd
-- +goose StatementBegin
DROP VIEW requests_hourly_developer_mv;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE requests_hourly_developer;
-- +goose StatementEnd
-- +goose StatementBegin
DROP VIEW requests_hourly_mv;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE requests_hourly;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE requests;
-- +goose StatementEnd

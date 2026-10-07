-- Model routers' calls per hour, for the router report: the tier that
-- served them and the one the router chose (they differ in shadow mode),
-- the step that chose it, and their tokens and cost, so what the chosen
-- tiers would have cost can be priced without reading the request log.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE routers_hourly (
  project_id          UUID,
  hour                DateTime('UTC') CODEC(Delta, ZSTD(1)),
  router              LowCardinality(String),
  tier                LowCardinality(String),
  decided_tier        LowCardinality(String),
  tier_source         LowCardinality(String),
  shadow              Bool,
  calls               UInt64,
  errors              UInt64,
  priced              UInt64,
  cost_micros         Int64,
  savings_micros      Int64,
  input_tokens        UInt64,
  output_tokens       UInt64,
  cache_read_tokens   UInt64,
  cache_write_tokens  UInt64,
  retention_days      UInt16
) ENGINE = SummingMergeTree((calls, errors, priced, cost_micros, savings_micros, input_tokens, output_tokens,
  cache_read_tokens, cache_write_tokens))
PARTITION BY (retention_days, toMonday(hour))
ORDER BY (project_id, router, hour, tier, decided_tier, tier_source, shadow)
TTL hour + INTERVAL 1 DAY TO VOLUME 'r2',
    hour + toIntervalDay(retention_days)
SETTINGS storage_policy = 'tiered', ttl_only_drop_parts = 1, index_granularity = 256;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE MATERIALIZED VIEW routers_hourly_mv TO routers_hourly AS
SELECT project_id, toStartOfHour(ts) AS hour, router, tier, decided_tier, tier_source, shadow,
       count() AS calls, countIf(error_code != '') AS errors, countIf(usage_known) AS priced,
       sum(cost_micros) AS cost_micros, sum(savings_micros) AS savings_micros,
       sum(input_tokens) AS input_tokens, sum(output_tokens) AS output_tokens,
       sum(cache_read_tokens) AS cache_read_tokens, sum(cache_write_tokens) AS cache_write_tokens, retention_days
FROM requests
WHERE router != ''
GROUP BY project_id, hour, router, tier, decided_tier, tier_source, shadow, retention_days;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP VIEW routers_hourly_mv;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE routers_hourly;
-- +goose StatementEnd

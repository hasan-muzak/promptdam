-- The savings ledger: per call, what it would have cost on the model asked
-- for at list price without any cache (counterfactual), what it cost, the
-- difference and its reason, and how the response cache answered; and a
-- rollup of savings per reason and family.
--
--   savings_reason  response_cache: answered from the cache, no provider call
--                   routing: a rule sent it to another model
--                   fallback: the model asked for failed, another answered
--                   prompt_cache: the provider's prompt cache served input
--                   '' when nothing changed what it cost
--   cache           hit or miss on routes with the response cache, else ''

-- +goose Up
-- +goose StatementBegin
ALTER TABLE requests
  ADD COLUMN counterfactual_micros  Int64 AFTER cost_micros,
  ADD COLUMN savings_micros         Int64 AFTER counterfactual_micros,
  ADD COLUMN savings_reason         LowCardinality(String) AFTER savings_micros,
  ADD COLUMN cache                  LowCardinality(String) AFTER savings_reason;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE savings_hourly (
  project_id             UUID,
  hour                   DateTime('UTC') CODEC(Delta, ZSTD(1)),
  savings_reason         LowCardinality(String),
  family_id              LowCardinality(String),
  requests               UInt64,
  counterfactual_micros  Int64,
  cost_micros            Int64,
  savings_micros         Int64,
  retention_days         UInt16
) ENGINE = SummingMergeTree((requests, counterfactual_micros, cost_micros, savings_micros))
PARTITION BY (retention_days, toMonday(hour))
ORDER BY (project_id, hour, savings_reason, family_id)
TTL hour + INTERVAL 1 DAY TO VOLUME 'r2',
    hour + toIntervalDay(retention_days)
SETTINGS storage_policy = 'tiered', ttl_only_drop_parts = 1, index_granularity = 256;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE MATERIALIZED VIEW savings_hourly_mv TO savings_hourly AS
SELECT project_id, toStartOfHour(ts) AS hour, savings_reason, family_id,
       count() AS requests, sum(counterfactual_micros) AS counterfactual_micros,
       sum(cost_micros) AS cost_micros, sum(savings_micros) AS savings_micros, retention_days
FROM requests
WHERE usage_known
GROUP BY project_id, hour, savings_reason, family_id, retention_days;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP VIEW savings_hourly_mv;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE savings_hourly;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE requests DROP COLUMN cache, DROP COLUMN savings_reason, DROP COLUMN savings_micros,
  DROP COLUMN counterfactual_micros;
-- +goose StatementEnd

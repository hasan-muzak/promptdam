-- Which model of a tier served a router's calls, for the router report's
-- cost and cache rate per model: the tier's own, or one of its fallbacks.
--
-- provider      the project's provider that served the calls
-- target_model  the model the gateway asked it for
--
-- Rows from before keep both empty.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE routers_hourly
  ADD COLUMN provider      LowCardinality(String) AFTER shadow,
  ADD COLUMN target_model  LowCardinality(String) AFTER provider,
  MODIFY ORDER BY (project_id, router, hour, tier, decided_tier, tier_source, shadow, provider, target_model);
-- +goose StatementEnd

-- +goose StatementBegin
DROP VIEW routers_hourly_mv;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE MATERIALIZED VIEW routers_hourly_mv TO routers_hourly AS
SELECT project_id, toStartOfHour(ts) AS hour, router, tier, decided_tier, tier_source, shadow, provider, target_model,
       count() AS calls, countIf(error_code != '') AS errors, countIf(usage_known) AS priced,
       sum(cost_micros) AS cost_micros, sum(savings_micros) AS savings_micros,
       sum(input_tokens) AS input_tokens, sum(output_tokens) AS output_tokens,
       sum(cache_read_tokens) AS cache_read_tokens, sum(cache_write_tokens) AS cache_write_tokens, retention_days
FROM requests
WHERE router != ''
GROUP BY project_id, hour, router, tier, decided_tier, tier_source, shadow, provider, target_model, retention_days;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP VIEW routers_hourly_mv;
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

-- +goose StatementBegin
ALTER TABLE routers_hourly MODIFY ORDER BY (project_id, router, hour, tier, decided_tier, tier_source, shadow);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE routers_hourly DROP COLUMN target_model, DROP COLUMN provider;
-- +goose StatementEnd

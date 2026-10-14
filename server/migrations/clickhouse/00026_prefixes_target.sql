-- The model the gateway asked for, by prefix: the cache doctor finds a
-- model's learned caching profile by it (a router of providers reports a
-- dated name of its own as the served model).

-- +goose Up
-- +goose StatementBegin
ALTER TABLE prefixes_hourly ADD COLUMN target_model SimpleAggregateFunction(any, LowCardinality(String)) AFTER served_model;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE prefixes_hourly_mv MODIFY QUERY
SELECT project_id, toStartOfHour(ts) AS hour, family_id, prefix_hash,
       count() AS requests, sum(input_tokens) AS input_tokens,
       sum(cache_read_tokens) AS cache_read_tokens, sum(cache_write_tokens) AS cache_write_tokens,
       groupBitOr(prefix_flags) AS prefix_flags, max(prefix_tokens) AS prefix_tokens,
       any(served_model) AS served_model, any(target_model) AS target_model, any(kind) AS kind, retention_days
FROM requests
WHERE family_id != '' AND usage_known
GROUP BY project_id, hour, family_id, prefix_hash, retention_days;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE prefixes_hourly_mv MODIFY QUERY
SELECT project_id, toStartOfHour(ts) AS hour, family_id, prefix_hash,
       count() AS requests, sum(input_tokens) AS input_tokens,
       sum(cache_read_tokens) AS cache_read_tokens, sum(cache_write_tokens) AS cache_write_tokens,
       groupBitOr(prefix_flags) AS prefix_flags, max(prefix_tokens) AS prefix_tokens,
       any(served_model) AS served_model, any(kind) AS kind, retention_days
FROM requests
WHERE family_id != '' AND usage_known
GROUP BY project_id, hour, family_id, prefix_hash, retention_days;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE prefixes_hourly DROP COLUMN target_model;
-- +goose StatementEnd

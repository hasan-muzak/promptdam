-- The cache doctor weighs what a family's prefix could save: each call's
-- prefix length (estimated tokens of its instructions and tools), rolled
-- up as the longest per prefix.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE requests ADD COLUMN prefix_tokens UInt32 AFTER prefix_flags;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE prefixes_hourly ADD COLUMN prefix_tokens SimpleAggregateFunction(max, UInt32) AFTER prefix_flags;
-- +goose StatementEnd
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

-- +goose Down
-- +goose StatementBegin
ALTER TABLE prefixes_hourly_mv MODIFY QUERY
SELECT project_id, toStartOfHour(ts) AS hour, family_id, prefix_hash,
       count() AS requests, sum(input_tokens) AS input_tokens,
       sum(cache_read_tokens) AS cache_read_tokens, sum(cache_write_tokens) AS cache_write_tokens,
       groupBitOr(prefix_flags) AS prefix_flags, any(served_model) AS served_model, any(kind) AS kind,
       retention_days
FROM requests
WHERE family_id != '' AND usage_known
GROUP BY project_id, hour, family_id, prefix_hash, retention_days;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE prefixes_hourly DROP COLUMN prefix_tokens;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE requests DROP COLUMN prefix_tokens;
-- +goose StatementEnd

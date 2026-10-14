-- How a call used its provider's prompt cache: cache_strategy markers (the
-- gateway added breakpoints) or auto (the provider caches on its own), ''
-- when its route doesn't cache; cache_trial a conversation trying the
-- strategy its model's profile is learning about.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE requests
  ADD COLUMN cache_strategy  LowCardinality(String) AFTER cache,
  ADD COLUMN cache_trial     Bool AFTER cache_strategy;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE requests DROP COLUMN cache_trial, DROP COLUMN cache_strategy;
-- +goose StatementEnd

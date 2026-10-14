-- Which backend served a call, and what the provider says it cost.
--
-- backend          the backend a router of providers reports (OpenRouter's
--                  "provider": "Google AI Studio", "Google"…), '' elsewhere:
--                  a model's backends don't all cache.
-- target_model     the model the gateway asked for (served_model is the
--                  provider's name for it, often dated): what a model's
--                  caching is learned by.
-- computed_micros  the call priced from the price list; cost_micros is the
--                  provider's own figure when it reports one (OpenRouter's
--                  usage.cost), else the same.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE requests
  ADD COLUMN backend          LowCardinality(String) AFTER served_model,
  ADD COLUMN target_model     LowCardinality(String) AFTER backend,
  ADD COLUMN computed_micros  Int64 AFTER cost_micros;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE requests DROP COLUMN computed_micros, DROP COLUMN target_model, DROP COLUMN backend;
-- +goose StatementEnd

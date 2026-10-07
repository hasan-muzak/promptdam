-- How a model router placed a call: the router, the tier that served it,
-- the tier the router chose (they differ while a router only watches, in
-- shadow mode), the step of the cascade that chose it and how sure it was.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE requests
  ADD COLUMN router          LowCardinality(String) AFTER rule,
  ADD COLUMN tier            LowCardinality(String) AFTER router,
  ADD COLUMN decided_tier    LowCardinality(String) AFTER tier,
  ADD COLUMN tier_source     LowCardinality(String) AFTER decided_tier,
  ADD COLUMN tier_confidence Float32 AFTER tier_source,
  ADD COLUMN shadow          Bool AFTER tier_confidence;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE requests DROP COLUMN shadow, DROP COLUMN tier_confidence, DROP COLUMN tier_source, DROP COLUMN decided_tier,
  DROP COLUMN tier, DROP COLUMN router;
-- +goose StatementEnd

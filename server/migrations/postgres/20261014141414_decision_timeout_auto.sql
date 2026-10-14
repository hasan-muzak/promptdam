-- +goose Up
-- modify "model_routers" table
ALTER TABLE "model_routers" ALTER COLUMN "decision_timeout_ms" SET DEFAULT 0;
-- Routers on the old default (150 ms, shorter than any hosted model answers) wait the auto timeout.
UPDATE "model_routers" SET "decision_timeout_ms" = 0 WHERE "decision_timeout_ms" = 150;

-- +goose Down
-- reverse: modify "model_routers" table
ALTER TABLE "model_routers" ALTER COLUMN "decision_timeout_ms" SET DEFAULT 150;

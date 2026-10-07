-- +goose Up
-- modify "model_routers" table
ALTER TABLE "model_routers" ADD COLUMN "demoted" jsonb NOT NULL DEFAULT '[]', ADD COLUMN "ready_at" timestamptz NULL;

-- +goose Down
-- reverse: modify "model_routers" table
ALTER TABLE "model_routers" DROP COLUMN "ready_at", DROP COLUMN "demoted";

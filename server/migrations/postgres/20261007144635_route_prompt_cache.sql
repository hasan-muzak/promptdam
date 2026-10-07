-- +goose Up
-- modify "routes" table
ALTER TABLE "routes" ADD COLUMN "prompt_cache" boolean NOT NULL DEFAULT false;

-- +goose Down
-- reverse: modify "routes" table
ALTER TABLE "routes" DROP COLUMN "prompt_cache";

-- +goose Up
-- modify "routes" table
ALTER TABLE "routes" ADD COLUMN "cache_ttl_seconds" bigint NOT NULL DEFAULT 0;

-- +goose Down
-- reverse: modify "routes" table
ALTER TABLE "routes" DROP COLUMN "cache_ttl_seconds";

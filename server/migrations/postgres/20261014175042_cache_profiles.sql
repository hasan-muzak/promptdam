-- +goose Up
-- create "cache_profiles" table
CREATE TABLE "cache_profiles" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NOT NULL,
  "updated_at" timestamptz NOT NULL,
  "doc" bytea NOT NULL,
  "models" bigint NOT NULL,
  PRIMARY KEY ("id")
);
-- create index "cacheprofile_created_at" to table: "cache_profiles"
CREATE INDEX "cacheprofile_created_at" ON "cache_profiles" ("created_at");

-- +goose Down
-- reverse: create index "cacheprofile_created_at" to table: "cache_profiles"
DROP INDEX "cacheprofile_created_at";
-- reverse: create "cache_profiles" table
DROP TABLE "cache_profiles";

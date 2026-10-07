-- +goose Up
-- create "price_overrides" table
CREATE TABLE "price_overrides" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NOT NULL,
  "updated_at" timestamptz NOT NULL,
  "org_id" uuid NOT NULL,
  "project_id" uuid NOT NULL,
  "model" character varying NOT NULL,
  "provider" character varying NOT NULL DEFAULT '',
  "input" double precision NOT NULL,
  "cached_input" double precision NOT NULL,
  "cache_write" double precision NOT NULL,
  "output" double precision NOT NULL,
  PRIMARY KEY ("id")
);
-- create index "priceoverride_org_id_project_id" to table: "price_overrides"
CREATE INDEX "priceoverride_org_id_project_id" ON "price_overrides" ("org_id", "project_id");
-- create index "priceoverride_project_id_provider_model" to table: "price_overrides"
CREATE UNIQUE INDEX "priceoverride_project_id_provider_model" ON "price_overrides" ("project_id", "provider", "model");

-- +goose Down
-- reverse: create index "priceoverride_project_id_provider_model" to table: "price_overrides"
DROP INDEX "priceoverride_project_id_provider_model";
-- reverse: create index "priceoverride_org_id_project_id" to table: "price_overrides"
DROP INDEX "priceoverride_org_id_project_id";
-- reverse: create "price_overrides" table
DROP TABLE "price_overrides";

-- +goose Up
-- create "price_lists" table
CREATE TABLE "price_lists" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NOT NULL,
  "updated_at" timestamptz NOT NULL,
  "source" character varying NOT NULL,
  "catalog" bytea NOT NULL,
  "models" bigint NOT NULL,
  "held" bigint NOT NULL DEFAULT 0,
  PRIMARY KEY ("id")
);
-- create index "pricelist_created_at" to table: "price_lists"
CREATE INDEX "pricelist_created_at" ON "price_lists" ("created_at");

-- +goose Down
-- reverse: create index "pricelist_created_at" to table: "price_lists"
DROP INDEX "pricelist_created_at";
-- reverse: create "price_lists" table
DROP TABLE "price_lists";

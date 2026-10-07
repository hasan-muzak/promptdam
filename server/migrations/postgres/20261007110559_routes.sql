-- +goose Up
-- create "routes" table
CREATE TABLE "routes" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NOT NULL,
  "updated_at" timestamptz NOT NULL,
  "org_id" uuid NOT NULL,
  "project_id" uuid NOT NULL,
  "name" character varying NOT NULL,
  "fallbacks" jsonb NOT NULL,
  "retries" bigint NOT NULL DEFAULT 1,
  PRIMARY KEY ("id")
);
-- create index "route_org_id_project_id" to table: "routes"
CREATE INDEX "route_org_id_project_id" ON "routes" ("org_id", "project_id");
-- create index "route_project_id_name" to table: "routes"
CREATE UNIQUE INDEX "route_project_id_name" ON "routes" ("project_id", "name");

-- +goose Down
-- reverse: create index "route_project_id_name" to table: "routes"
DROP INDEX "route_project_id_name";
-- reverse: create index "route_org_id_project_id" to table: "routes"
DROP INDEX "route_org_id_project_id";
-- reverse: create "routes" table
DROP TABLE "routes";

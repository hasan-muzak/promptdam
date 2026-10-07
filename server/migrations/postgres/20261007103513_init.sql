-- +goose Up
-- create "providers" table
CREATE TABLE "providers" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NOT NULL,
  "updated_at" timestamptz NOT NULL,
  "org_id" uuid NOT NULL,
  "project_id" uuid NOT NULL,
  "name" character varying NOT NULL,
  "kind" character varying NOT NULL,
  "api_format" character varying NOT NULL,
  "base_url" character varying NOT NULL,
  "secrets" character varying NOT NULL DEFAULT '',
  "secret_hint" character varying NOT NULL DEFAULT '',
  "models" jsonb NOT NULL,
  "priority" bigint NOT NULL DEFAULT 0,
  "enabled" boolean NOT NULL DEFAULT true,
  "checked_at" timestamptz NULL,
  "check_error" character varying NOT NULL DEFAULT '',
  PRIMARY KEY ("id")
);
-- create index "provider_org_id_project_id" to table: "providers"
CREATE INDEX "provider_org_id_project_id" ON "providers" ("org_id", "project_id");
-- create index "provider_project_id_name" to table: "providers"
CREATE UNIQUE INDEX "provider_project_id_name" ON "providers" ("project_id", "name");

-- +goose Down
-- reverse: create index "provider_project_id_name" to table: "providers"
DROP INDEX "provider_project_id_name";
-- reverse: create index "provider_org_id_project_id" to table: "providers"
DROP INDEX "provider_org_id_project_id";
-- reverse: create "providers" table
DROP TABLE "providers";

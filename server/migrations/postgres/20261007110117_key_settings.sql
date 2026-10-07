-- +goose Up
-- create "key_settings" table
CREATE TABLE "key_settings" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NOT NULL,
  "updated_at" timestamptz NOT NULL,
  "org_id" uuid NOT NULL,
  "project_id" uuid NOT NULL,
  "api_key_id" uuid NOT NULL,
  "allowed_models" jsonb NOT NULL,
  "tags" jsonb NOT NULL,
  "developer" character varying NOT NULL DEFAULT '',
  "rpm" bigint NOT NULL DEFAULT 0,
  "route" character varying NOT NULL DEFAULT '',
  PRIMARY KEY ("id")
);
-- create index "keysetting_api_key_id" to table: "key_settings"
CREATE UNIQUE INDEX "keysetting_api_key_id" ON "key_settings" ("api_key_id");
-- create index "keysetting_org_id_project_id" to table: "key_settings"
CREATE INDEX "keysetting_org_id_project_id" ON "key_settings" ("org_id", "project_id");

-- +goose Down
-- reverse: create index "keysetting_org_id_project_id" to table: "key_settings"
DROP INDEX "keysetting_org_id_project_id";
-- reverse: create index "keysetting_api_key_id" to table: "key_settings"
DROP INDEX "keysetting_api_key_id";
-- reverse: create "key_settings" table
DROP TABLE "key_settings";

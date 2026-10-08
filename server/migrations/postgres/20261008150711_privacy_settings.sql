-- +goose Up
-- create "privacy_settings" table
CREATE TABLE "privacy_settings" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NOT NULL,
  "updated_at" timestamptz NOT NULL,
  "org_id" uuid NOT NULL,
  "payload_free" boolean NOT NULL DEFAULT false,
  "updated_by" character varying NOT NULL DEFAULT '',
  PRIMARY KEY ("id")
);
-- create index "privacysetting_org_id" to table: "privacy_settings"
CREATE INDEX "privacysetting_org_id" ON "privacy_settings" ("org_id");
-- create index "privacysetting_org_unique" to table: "privacy_settings"
CREATE UNIQUE INDEX "privacysetting_org_unique" ON "privacy_settings" ("org_id");

-- +goose Down
-- reverse: create index "privacysetting_org_unique" to table: "privacy_settings"
DROP INDEX "privacysetting_org_unique";
-- reverse: create index "privacysetting_org_id" to table: "privacy_settings"
DROP INDEX "privacysetting_org_id";
-- reverse: create "privacy_settings" table
DROP TABLE "privacy_settings";

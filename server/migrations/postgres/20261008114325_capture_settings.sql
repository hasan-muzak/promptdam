-- +goose Up
-- create "capture_settings" table
CREATE TABLE "capture_settings" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NOT NULL,
  "updated_at" timestamptz NOT NULL,
  "org_id" uuid NOT NULL,
  "project_id" uuid NOT NULL,
  "enabled" boolean NOT NULL DEFAULT false,
  "sample_rate" double precision NOT NULL DEFAULT 1,
  "answers" boolean NOT NULL DEFAULT true,
  PRIMARY KEY ("id")
);
-- create index "capturesetting_org_id_project_id" to table: "capture_settings"
CREATE INDEX "capturesetting_org_id_project_id" ON "capture_settings" ("org_id", "project_id");
-- create index "capturesetting_project_id" to table: "capture_settings"
CREATE UNIQUE INDEX "capturesetting_project_id" ON "capture_settings" ("project_id");

-- +goose Down
-- reverse: create index "capturesetting_project_id" to table: "capture_settings"
DROP INDEX "capturesetting_project_id";
-- reverse: create index "capturesetting_org_id_project_id" to table: "capture_settings"
DROP INDEX "capturesetting_org_id_project_id";
-- reverse: create "capture_settings" table
DROP TABLE "capture_settings";

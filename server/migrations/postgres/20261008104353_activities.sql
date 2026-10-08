-- +goose Up
-- create "activities" table
CREATE TABLE "activities" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NOT NULL,
  "org_id" uuid NOT NULL,
  "project_id" uuid NOT NULL,
  "kind" character varying NOT NULL,
  "severity" character varying NOT NULL DEFAULT 'info',
  "title" character varying NOT NULL,
  "body" character varying NOT NULL DEFAULT '',
  "subject" character varying NOT NULL DEFAULT '',
  "url" character varying NOT NULL DEFAULT '',
  "data" jsonb NOT NULL,
  PRIMARY KEY ("id")
);
-- create index "activity_created_at" to table: "activities"
CREATE INDEX "activity_created_at" ON "activities" ("created_at");
-- create index "activity_org_id_project_id" to table: "activities"
CREATE INDEX "activity_org_id_project_id" ON "activities" ("org_id", "project_id");
-- create index "activity_project_id_id" to table: "activities"
CREATE INDEX "activity_project_id_id" ON "activities" ("project_id", "id");
-- create index "activity_project_id_subject_id" to table: "activities"
CREATE INDEX "activity_project_id_subject_id" ON "activities" ("project_id", "subject", "id");

-- +goose Down
-- reverse: create index "activity_project_id_subject_id" to table: "activities"
DROP INDEX "activity_project_id_subject_id";
-- reverse: create index "activity_project_id_id" to table: "activities"
DROP INDEX "activity_project_id_id";
-- reverse: create index "activity_org_id_project_id" to table: "activities"
DROP INDEX "activity_org_id_project_id";
-- reverse: create index "activity_created_at" to table: "activities"
DROP INDEX "activity_created_at";
-- reverse: create "activities" table
DROP TABLE "activities";

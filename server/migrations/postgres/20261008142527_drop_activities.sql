-- +goose Up
-- drop "activities" table
DROP TABLE "activities";

-- +goose Down
-- reverse: drop "activities" table
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
CREATE INDEX "activity_created_at" ON "activities" ("created_at");
CREATE INDEX "activity_org_id_project_id" ON "activities" ("org_id", "project_id");
CREATE INDEX "activity_project_id_id" ON "activities" ("project_id", "id");
CREATE INDEX "activity_project_id_subject_id" ON "activities" ("project_id", "subject", "id");

-- +goose Up
-- create "family_labels" table
CREATE TABLE "family_labels" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NOT NULL,
  "updated_at" timestamptz NOT NULL,
  "org_id" uuid NOT NULL,
  "project_id" uuid NOT NULL,
  "family_id" character varying NOT NULL,
  "label" character varying NOT NULL,
  PRIMARY KEY ("id")
);
-- create index "familylabel_org_id_project_id" to table: "family_labels"
CREATE INDEX "familylabel_org_id_project_id" ON "family_labels" ("org_id", "project_id");
-- create index "familylabel_project_id_family_id" to table: "family_labels"
CREATE UNIQUE INDEX "familylabel_project_id_family_id" ON "family_labels" ("project_id", "family_id");

-- +goose Down
-- reverse: create index "familylabel_project_id_family_id" to table: "family_labels"
DROP INDEX "familylabel_project_id_family_id";
-- reverse: create index "familylabel_org_id_project_id" to table: "family_labels"
DROP INDEX "familylabel_org_id_project_id";
-- reverse: create "family_labels" table
DROP TABLE "family_labels";

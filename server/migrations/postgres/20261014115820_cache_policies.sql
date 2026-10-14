-- +goose Up
-- create "cache_policies" table
CREATE TABLE "cache_policies" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NOT NULL,
  "updated_at" timestamptz NOT NULL,
  "org_id" uuid NOT NULL,
  "project_id" uuid NOT NULL,
  "families" jsonb NOT NULL,
  "learned_at" timestamptz NOT NULL,
  PRIMARY KEY ("id")
);
-- create index "cachepolicy_org_id_project_id" to table: "cache_policies"
CREATE INDEX "cachepolicy_org_id_project_id" ON "cache_policies" ("org_id", "project_id");
-- create index "cachepolicy_project_id" to table: "cache_policies"
CREATE UNIQUE INDEX "cachepolicy_project_id" ON "cache_policies" ("project_id");

-- +goose Down
-- reverse: create index "cachepolicy_project_id" to table: "cache_policies"
DROP INDEX "cachepolicy_project_id";
-- reverse: create index "cachepolicy_org_id_project_id" to table: "cache_policies"
DROP INDEX "cachepolicy_org_id_project_id";
-- reverse: create "cache_policies" table
DROP TABLE "cache_policies";

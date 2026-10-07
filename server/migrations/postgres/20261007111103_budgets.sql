-- +goose Up
-- create "budgets" table
CREATE TABLE "budgets" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NOT NULL,
  "updated_at" timestamptz NOT NULL,
  "org_id" uuid NOT NULL,
  "project_id" uuid NOT NULL,
  "name" character varying NOT NULL,
  "scope" character varying NOT NULL,
  "target" character varying NOT NULL DEFAULT '',
  "window" character varying NOT NULL,
  "limit_micros" bigint NOT NULL,
  "action" character varying NOT NULL DEFAULT 'alert',
  "thresholds" jsonb NOT NULL,
  "channel_ids" jsonb NOT NULL,
  "enabled" boolean NOT NULL DEFAULT true,
  "alerted_period" timestamptz NULL,
  "alerted_percent" bigint NOT NULL DEFAULT 0,
  PRIMARY KEY ("id")
);
-- create index "budget_enabled_project_id" to table: "budgets"
CREATE INDEX "budget_enabled_project_id" ON "budgets" ("enabled", "project_id");
-- create index "budget_org_id_project_id" to table: "budgets"
CREATE INDEX "budget_org_id_project_id" ON "budgets" ("org_id", "project_id");

-- +goose Down
-- reverse: create index "budget_org_id_project_id" to table: "budgets"
DROP INDEX "budget_org_id_project_id";
-- reverse: create index "budget_enabled_project_id" to table: "budgets"
DROP INDEX "budget_enabled_project_id";
-- reverse: create "budgets" table
DROP TABLE "budgets";

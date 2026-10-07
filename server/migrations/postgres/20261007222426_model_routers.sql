-- +goose Up
-- modify "routes" table
ALTER TABLE "routes" ADD COLUMN "router" character varying NOT NULL DEFAULT '';
-- create "model_routers" table
CREATE TABLE "model_routers" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NOT NULL,
  "updated_at" timestamptz NOT NULL,
  "org_id" uuid NOT NULL,
  "project_id" uuid NOT NULL,
  "name" character varying NOT NULL,
  "status" character varying NOT NULL DEFAULT 'shadow',
  "tiers" jsonb NOT NULL,
  "rules" jsonb NOT NULL,
  "default_tier" character varying NOT NULL,
  "quality_floor" double precision NOT NULL DEFAULT 0.95,
  "sample_rate" double precision NOT NULL DEFAULT 0,
  "eval_budget_micros" bigint NOT NULL DEFAULT 100000000,
  "judge" character varying NOT NULL DEFAULT '',
  "decision_model" character varying NOT NULL DEFAULT '',
  "decision_timeout_ms" bigint NOT NULL DEFAULT 150,
  "activated_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
-- create index "modelrouter_org_id_project_id" to table: "model_routers"
CREATE INDEX "modelrouter_org_id_project_id" ON "model_routers" ("org_id", "project_id");
-- create index "modelrouter_project_id_name" to table: "model_routers"
CREATE UNIQUE INDEX "modelrouter_project_id_name" ON "model_routers" ("project_id", "name");

-- +goose Down
-- reverse: create index "modelrouter_project_id_name" to table: "model_routers"
DROP INDEX "modelrouter_project_id_name";
-- reverse: create index "modelrouter_org_id_project_id" to table: "model_routers"
DROP INDEX "modelrouter_org_id_project_id";
-- reverse: create "model_routers" table
DROP TABLE "model_routers";
-- reverse: modify "routes" table
ALTER TABLE "routes" DROP COLUMN "router";

-- +goose Up
-- create "alert_rules" table
CREATE TABLE "alert_rules" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NOT NULL,
  "updated_at" timestamptz NOT NULL,
  "org_id" uuid NOT NULL,
  "project_id" uuid NOT NULL,
  "name" character varying NOT NULL,
  "kind" character varying NOT NULL,
  "threshold" double precision NOT NULL,
  "window_minutes" bigint NOT NULL,
  "min_calls" bigint NOT NULL DEFAULT 0,
  "min_micros" bigint NOT NULL DEFAULT 0,
  "provider" character varying NOT NULL DEFAULT '',
  "model" character varying NOT NULL DEFAULT '',
  "channel_ids" jsonb NOT NULL,
  "enabled" boolean NOT NULL DEFAULT true,
  "cooldown_minutes" bigint NOT NULL DEFAULT 60,
  "firing" boolean NOT NULL DEFAULT false,
  "firing_on" jsonb NOT NULL,
  "last_value" double precision NOT NULL DEFAULT 0,
  "evaluated_at" timestamptz NULL,
  "fired_at" timestamptz NULL,
  "resolved_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
-- create index "alertrule_enabled" to table: "alert_rules"
CREATE INDEX "alertrule_enabled" ON "alert_rules" ("enabled");
-- create index "alertrule_org_id_project_id" to table: "alert_rules"
CREATE INDEX "alertrule_org_id_project_id" ON "alert_rules" ("org_id", "project_id");
-- create index "alertrule_project_id_name" to table: "alert_rules"
CREATE UNIQUE INDEX "alertrule_project_id_name" ON "alert_rules" ("project_id", "name");

-- +goose Down
-- reverse: create index "alertrule_project_id_name" to table: "alert_rules"
DROP INDEX "alertrule_project_id_name";
-- reverse: create index "alertrule_org_id_project_id" to table: "alert_rules"
DROP INDEX "alertrule_org_id_project_id";
-- reverse: create index "alertrule_enabled" to table: "alert_rules"
DROP INDEX "alertrule_enabled";
-- reverse: create "alert_rules" table
DROP TABLE "alert_rules";

-- +goose Up
-- create "firewall_settings" table
CREATE TABLE "firewall_settings" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NOT NULL,
  "updated_at" timestamptz NOT NULL,
  "org_id" uuid NOT NULL,
  "project_id" uuid NOT NULL,
  "settings" jsonb NOT NULL,
  PRIMARY KEY ("id")
);
-- create index "firewallsetting_org_id_project_id" to table: "firewall_settings"
CREATE INDEX "firewallsetting_org_id_project_id" ON "firewall_settings" ("org_id", "project_id");
-- create index "firewallsetting_project_id" to table: "firewall_settings"
CREATE UNIQUE INDEX "firewallsetting_project_id" ON "firewall_settings" ("project_id");

-- +goose Down
-- reverse: create index "firewallsetting_project_id" to table: "firewall_settings"
DROP INDEX "firewallsetting_project_id";
-- reverse: create index "firewallsetting_org_id_project_id" to table: "firewall_settings"
DROP INDEX "firewallsetting_org_id_project_id";
-- reverse: create "firewall_settings" table
DROP TABLE "firewall_settings";

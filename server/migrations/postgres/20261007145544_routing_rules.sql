-- +goose Up
-- create "routing_rules" table
CREATE TABLE "routing_rules" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NOT NULL,
  "updated_at" timestamptz NOT NULL,
  "org_id" uuid NOT NULL,
  "project_id" uuid NOT NULL,
  "name" character varying NOT NULL,
  "route" character varying NOT NULL DEFAULT '',
  "when" character varying NOT NULL,
  "to_model" character varying NOT NULL,
  "priority" bigint NOT NULL DEFAULT 0,
  "status" character varying NOT NULL DEFAULT 'draft',
  "activated_at" timestamptz NULL,
  PRIMARY KEY ("id")
);
-- create index "routingrule_org_id_project_id" to table: "routing_rules"
CREATE INDEX "routingrule_org_id_project_id" ON "routing_rules" ("org_id", "project_id");
-- create index "routingrule_project_id_name" to table: "routing_rules"
CREATE UNIQUE INDEX "routingrule_project_id_name" ON "routing_rules" ("project_id", "name");
-- create index "routingrule_project_id_status" to table: "routing_rules"
CREATE INDEX "routingrule_project_id_status" ON "routing_rules" ("project_id", "status");

-- +goose Down
-- reverse: create index "routingrule_project_id_status" to table: "routing_rules"
DROP INDEX "routingrule_project_id_status";
-- reverse: create index "routingrule_project_id_name" to table: "routing_rules"
DROP INDEX "routingrule_project_id_name";
-- reverse: create index "routingrule_org_id_project_id" to table: "routing_rules"
DROP INDEX "routingrule_org_id_project_id";
-- reverse: create "routing_rules" table
DROP TABLE "routing_rules";

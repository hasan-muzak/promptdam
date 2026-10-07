-- +goose Up
-- create "router_classifiers" table
CREATE TABLE "router_classifiers" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NOT NULL,
  "updated_at" timestamptz NOT NULL,
  "org_id" uuid NOT NULL,
  "project_id" uuid NOT NULL,
  "router_id" uuid NOT NULL,
  "model" jsonb NOT NULL,
  "samples" bigint NOT NULL DEFAULT 0,
  "trained_at" timestamptz NOT NULL,
  PRIMARY KEY ("id")
);
-- create index "routerclassifier_org_id_project_id" to table: "router_classifiers"
CREATE INDEX "routerclassifier_org_id_project_id" ON "router_classifiers" ("org_id", "project_id");
-- create index "routerclassifier_router_id" to table: "router_classifiers"
CREATE UNIQUE INDEX "routerclassifier_router_id" ON "router_classifiers" ("router_id");

-- +goose Down
-- reverse: create index "routerclassifier_router_id" to table: "router_classifiers"
DROP INDEX "routerclassifier_router_id";
-- reverse: create index "routerclassifier_org_id_project_id" to table: "router_classifiers"
DROP INDEX "routerclassifier_org_id_project_id";
-- reverse: create "router_classifiers" table
DROP TABLE "router_classifiers";

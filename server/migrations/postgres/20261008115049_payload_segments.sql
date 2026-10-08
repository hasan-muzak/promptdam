-- +goose Up
-- create "payload_segments" table
CREATE TABLE "payload_segments" (
  "id" uuid NOT NULL,
  "created_at" timestamptz NOT NULL,
  "org_id" uuid NOT NULL,
  "project_id" uuid NOT NULL,
  "key" character varying NOT NULL,
  "bytes" bigint NOT NULL,
  "payloads" bigint NOT NULL,
  "expires_at" timestamptz NOT NULL,
  PRIMARY KEY ("id")
);
-- create index "payload_segments_key_key" to table: "payload_segments"
CREATE UNIQUE INDEX "payload_segments_key_key" ON "payload_segments" ("key");
-- create index "payloadsegment_expires_at" to table: "payload_segments"
CREATE INDEX "payloadsegment_expires_at" ON "payload_segments" ("expires_at");
-- create index "payloadsegment_org_id_project_id" to table: "payload_segments"
CREATE INDEX "payloadsegment_org_id_project_id" ON "payload_segments" ("org_id", "project_id");
-- create index "payloadsegment_project_id_expires_at" to table: "payload_segments"
CREATE INDEX "payloadsegment_project_id_expires_at" ON "payload_segments" ("project_id", "expires_at");

-- +goose Down
-- reverse: create index "payloadsegment_project_id_expires_at" to table: "payload_segments"
DROP INDEX "payloadsegment_project_id_expires_at";
-- reverse: create index "payloadsegment_org_id_project_id" to table: "payload_segments"
DROP INDEX "payloadsegment_org_id_project_id";
-- reverse: create index "payloadsegment_expires_at" to table: "payload_segments"
DROP INDEX "payloadsegment_expires_at";
-- reverse: create index "payload_segments_key_key" to table: "payload_segments"
DROP INDEX "payload_segments_key_key";
-- reverse: create "payload_segments" table
DROP TABLE "payload_segments";

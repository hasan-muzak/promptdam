-- +goose Up
-- modify "alert_rules" table
ALTER TABLE "alert_rules" DROP COLUMN "last_value", DROP COLUMN "evaluated_at";

-- +goose Down
-- reverse: modify "alert_rules" table
ALTER TABLE "alert_rules" ADD COLUMN "evaluated_at" timestamptz NULL, ADD COLUMN "last_value" double precision NOT NULL DEFAULT 0;

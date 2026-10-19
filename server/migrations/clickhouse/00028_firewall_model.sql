-- The detector model (round PD): who found each firewall event's values,
-- a pattern or the model; and on each call, what the model did: read it
-- (read), read its newest text only (partial), failed to answer (failed),
-- read it after the call went (later, for labels that only alert), or ''
-- when it didn't read.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE firewall_events ADD COLUMN source LowCardinality(String) DEFAULT 'pattern' AFTER location;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE requests ADD COLUMN firewall_model LowCardinality(String) DEFAULT '' AFTER firewall_kinds;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE requests DROP COLUMN firewall_model;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE firewall_events DROP COLUMN source;
-- +goose StatementEnd

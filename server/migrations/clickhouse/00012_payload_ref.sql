-- Where a call's payload is kept, for projects that keep them: the
-- segment's key, then the payload's offset and length in it
-- (key#offset+length). Empty for calls kept without a payload.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE requests ADD COLUMN payload_ref String CODEC(ZSTD(1)) AFTER firewall_kinds;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE requests DROP COLUMN payload_ref;
-- +goose StatementEnd

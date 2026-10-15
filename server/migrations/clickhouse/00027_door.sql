-- The API the client spoke when a door converted it (gemini,
-- bedrock-converse, bedrock-invoke; '' for the gateway's own): api_format
-- stays the API the gateway served the call in.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE requests ADD COLUMN door LowCardinality(String) DEFAULT '' AFTER api_format;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE requests DROP COLUMN door;
-- +goose StatementEnd

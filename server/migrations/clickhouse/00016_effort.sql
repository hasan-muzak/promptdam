-- The reasoning effort the gateway sent a call with: a router tier's,
-- fitted to the model ('' when the call went with the client's own).

-- +goose Up
-- +goose StatementBegin
ALTER TABLE requests ADD COLUMN effort LowCardinality(String) AFTER shadow;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE requests DROP COLUMN effort;
-- +goose StatementEnd

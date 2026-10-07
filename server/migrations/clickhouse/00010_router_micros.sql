-- What a model router's decision model cost for a call (on the project's
-- own key), part of what the call spent and taken from its savings.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE requests ADD COLUMN router_micros Int64 AFTER shadow;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE requests DROP COLUMN router_micros;
-- +goose StatementEnd

-- Whether a provider of the other API format answered the call, translated
-- (an OpenAI Chat Completions call answered by an Anthropic model, or the
-- other way round).

-- +goose Up
-- +goose StatementBegin
ALTER TABLE requests ADD COLUMN translated Bool AFTER stream;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE requests DROP COLUMN translated;
-- +goose StatementEnd

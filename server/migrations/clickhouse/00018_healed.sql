-- How a call whose thinking a firewall rewrite invalidated was sent again:
-- drop_block (the provider asked to drop the blocks), strip_thinking (the
-- history's thinking blocks taken out), '' when it wasn't.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE requests ADD COLUMN healed LowCardinality(String) AFTER effort;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE requests DROP COLUMN healed;
-- +goose StatementEnd

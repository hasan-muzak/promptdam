-- Whether a verdict was judged in both orders: the judge's position bias
-- shows in how often those disagree (consistent = false), and a router
-- whose judge flips too often has every sample judged both ways.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE router_evals ADD COLUMN both_orders Bool AFTER consistent;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE router_evals DROP COLUMN both_orders;
-- +goose StatementEnd

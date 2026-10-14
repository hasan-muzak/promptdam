-- How much each tier writes against the default tier, on the same call:
-- the output tokens of a sampled call's answer on the candidate tier and on
-- the default tier (0 when unknown). Training learns each tier's ratio from
-- them, and the ledger prices a routed call's counterfactual with it.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE router_evals
  ADD COLUMN candidate_output_tokens UInt32 AFTER judge_micros,
  ADD COLUMN baseline_output_tokens  UInt32 AFTER candidate_output_tokens;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE router_evals DROP COLUMN baseline_output_tokens, DROP COLUMN candidate_output_tokens;
-- +goose StatementEnd

-- How the ledger knows each saving, and whether a call's cost is known.
--
--   priced         false when a model without a price served the call: its
--                  cost is unknown, so it books no counterfactual and no
--                  savings (it used to book the whole counterfactual)
--   savings_basis  provider: the provider's own cache counts priced
--                  (prompt_cache); cache: an answer the response cache kept,
--                  no provider call; estimate: the model asked for priced on
--                  the served call's tokens (routing, fallback); '' when
--                  nothing was saved

-- +goose Up
-- +goose StatementBegin
ALTER TABLE requests
  ADD COLUMN priced         Bool DEFAULT true AFTER cost_micros,
  ADD COLUMN savings_basis  LowCardinality(String) AFTER savings_reason;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE savings_hourly
  ADD COLUMN savings_basis LowCardinality(String) AFTER family_id,
  MODIFY ORDER BY (project_id, hour, savings_reason, family_id, savings_basis);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE savings_hourly_mv MODIFY QUERY
SELECT project_id, toStartOfHour(ts) AS hour, savings_reason, family_id, savings_basis,
       count() AS requests, sum(counterfactual_micros) AS counterfactual_micros,
       sum(cost_micros) AS cost_micros, sum(savings_micros) AS savings_micros, retention_days
FROM requests
WHERE usage_known
GROUP BY project_id, hour, savings_reason, family_id, savings_basis, retention_days;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE savings_hourly_mv MODIFY QUERY
SELECT project_id, toStartOfHour(ts) AS hour, savings_reason, family_id,
       count() AS requests, sum(counterfactual_micros) AS counterfactual_micros,
       sum(cost_micros) AS cost_micros, sum(savings_micros) AS savings_micros, retention_days
FROM requests
WHERE usage_known
GROUP BY project_id, hour, savings_reason, family_id, retention_days;
-- +goose StatementEnd
-- +goose StatementBegin
-- savings_hourly keeps savings_basis: a sorting key column can't be dropped.
ALTER TABLE requests DROP COLUMN savings_basis, DROP COLUMN priced;
-- +goose StatementEnd

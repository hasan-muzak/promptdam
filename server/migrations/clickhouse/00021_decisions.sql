-- How a router's decision model answered a call it was asked about:
-- decision answered (it named a tier), cached (an answer to the same
-- question reused), timeout (none within the router's wait, or the auto
-- one), error (the provider failed or refused), unreadable (an answer naming
-- no tier), '' when it wasn't asked; decision_ms how long the call waited.
--
-- decisions_hourly: per router and outcome, the calls, what the decision
-- model cost and how long they waited (the router report's decisions).

-- +goose Up
-- +goose StatementBegin
ALTER TABLE requests
  ADD COLUMN decision     LowCardinality(String) AFTER router_micros,
  ADD COLUMN decision_ms  UInt32 AFTER decision;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE decisions_hourly (
  project_id      UUID,
  hour            DateTime('UTC') CODEC(Delta, ZSTD(1)),
  router          LowCardinality(String),
  decision        LowCardinality(String),
  calls           SimpleAggregateFunction(sum, UInt64),
  router_micros   SimpleAggregateFunction(sum, Int64),
  decision_ms     AggregateFunction(quantilesTDigest(0.5, 0.95, 0.99), UInt32),
  retention_days  UInt16
) ENGINE = AggregatingMergeTree
PARTITION BY (retention_days, toMonday(hour))
ORDER BY (project_id, router, hour, decision)
TTL hour + INTERVAL 1 DAY TO VOLUME 'r2',
    hour + toIntervalDay(retention_days)
SETTINGS storage_policy = 'tiered', ttl_only_drop_parts = 1, index_granularity = 256;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE MATERIALIZED VIEW decisions_hourly_mv TO decisions_hourly AS
SELECT project_id, toStartOfHour(ts) AS hour, router, decision, count() AS calls, sum(router_micros) AS router_micros,
       quantilesTDigestState(0.5, 0.95, 0.99)(decision_ms) AS decision_ms, retention_days
FROM requests
WHERE decision != ''
GROUP BY project_id, hour, router, decision, retention_days;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP VIEW decisions_hourly_mv;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE decisions_hourly;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE requests DROP COLUMN decision_ms, DROP COLUMN decision;
-- +goose StatementEnd

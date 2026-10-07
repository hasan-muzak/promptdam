-- What routing rules read of a call, kept so a rule can be tried on the
-- request log before it routes anything: the input as the gateway
-- estimated it (bytes / 4, before any provider counted it), the messages,
-- the answer's token limit, whether it offered tools and wanted JSON; and
-- the rule that routed it.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE requests
  ADD COLUMN est_input_tokens  UInt32 AFTER prefix_tokens,
  ADD COLUMN messages          UInt32 AFTER est_input_tokens,
  ADD COLUMN max_tokens        UInt32 AFTER messages,
  ADD COLUMN has_tools         Bool AFTER max_tokens,
  ADD COLUMN json_output       Bool AFTER has_tools,
  ADD COLUMN rule              LowCardinality(String) AFTER route;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE requests DROP COLUMN rule, DROP COLUMN json_output, DROP COLUMN has_tools, DROP COLUMN max_tokens, DROP COLUMN messages,
  DROP COLUMN est_input_tokens;
-- +goose StatementEnd

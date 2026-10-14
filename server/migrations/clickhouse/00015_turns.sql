-- Each call's place in its conversation (features/conversation).
--
--   conversation  the conversation's key (hex): the client's session, else
--                 its first message, within its project, key, end user,
--                 route, router and family
--   turn_kind     start: the call answers a human message; continuation: it
--                 continues one (an agent's tool loop); '' without a turn
--   human_turns   the conversation's human messages so far
--   chain_gap_ms  how long after the conversation's previous served call
--                 this one started (start to start: what prompt caches
--                 count), 0 when unknown
--   chain_state   start: no earlier call known; continued: the gap is
--                 known; unknown: the turn store couldn't be read

-- +goose Up
-- +goose StatementBegin
ALTER TABLE requests
  ADD COLUMN conversation  String DEFAULT '' AFTER route,
  ADD COLUMN turn_kind     LowCardinality(String) AFTER conversation,
  ADD COLUMN human_turns   UInt16 AFTER turn_kind,
  ADD COLUMN chain_gap_ms  UInt32 AFTER human_turns,
  ADD COLUMN chain_state   LowCardinality(String) AFTER chain_gap_ms;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE requests DROP COLUMN chain_state, DROP COLUMN chain_gap_ms, DROP COLUMN human_turns, DROP COLUMN turn_kind,
  DROP COLUMN conversation;
-- +goose StatementEnd

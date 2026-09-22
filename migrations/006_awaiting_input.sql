-- 006_awaiting_input.sql — rename the human-wait states.
--
-- `awaiting_human` and `waiting_on_human` named the *counterparty* rather than
-- what the system needs. They cover questions, approvals and confirmations
-- alike, so `awaiting_input` describes the state without narrowing it.
--
-- `awaiting_confirmation` is deliberately left alone: it is the specific gate
-- before work begins, not the general "an agent asked you something" state.
--
-- ALTER TYPE ... RENAME VALUE rewrites the label in place, so existing rows
-- keep their identity and no data migration is needed.

ALTER TYPE work_item_status RENAME VALUE 'awaiting_human' TO 'awaiting_input';
ALTER TYPE session_status RENAME VALUE 'waiting_on_human' TO 'awaiting_input';

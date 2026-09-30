-- 060_directive_receipts.sql — a steer is taken, then read.
--
-- lux reports a directive twice: accepted when the harness took it (with
-- where it lands: at the agent's next step, or only when its turn ends),
-- and consumed when the agent's next model step has it in context.
-- delivered_at is the second; an older lux, or a harness with no read
-- receipt, sets it on the first. A failure is kept with lux's reason, so a
-- steer that never reached the agent says so rather than staying queued.
--
-- interrupt_only: "Interrupt now" on an instruction already submitted. It
-- supersedes that directive with the same words, and lux is sent only the
-- interrupt: the words went with the original, whatever became of it
-- since. Decided once, when the directive is created.
ALTER TABLE directives
  ADD COLUMN accepted_at    timestamptz,
  ADD COLUMN lands          text CHECK (lands IN ('next_step', 'next_turn')),
  ADD COLUMN failed_at      timestamptz,
  ADD COLUMN error          text,
  ADD COLUMN interrupt_only boolean NOT NULL DEFAULT false;

-- A finished turn is held open while a steer sent to it is unread: the
-- sweep compares that steer's delivery with the Run's last turn end. Both
-- are a few rows among the thousands a Run records; these find them
-- without reading the rest.
CREATE INDEX events_run_directive_delivered_idx ON events (run_id, (payload->>'directiveId'), cursor)
  WHERE event_type = 'run.directive.delivered';
CREATE INDEX events_run_turn_end_idx ON events (run_id, cursor)
  WHERE event_type = 'agent.session.stopped';

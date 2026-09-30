-- 062_directive_receipts.sql — a steer is taken, then read.
--
-- lux reports a directive twice: accepted when the harness took it (with
-- where it lands: at the agent's next step, or only when its turn ends),
-- and consumed when the agent's next model step has it in context.
-- delivered_at is the second; an older lux, or a harness with no read
-- receipt, sets it on the first. A failure is kept with lux's reason, so a
-- steer that never reached the agent says so rather than staying queued.
--
-- resends: "Interrupt now" on a queued instruction, the root directive
-- whose words it repeats (an interrupt re-sending a re-send names the
-- first). The root and every directive resending it are one instruction:
-- its words go once, with whichever of them carries them.
-- interrupt_only: whether a resend is sent as the interrupt alone (true)
-- or with the words (false). NULL until its first send attempt decides it
-- (the words are carried by a root or resend that lux has and has not
-- failed), then fixed, so every retry sends the same request.
ALTER TABLE directives
  ADD COLUMN accepted_at    timestamptz,
  ADD COLUMN lands          text CHECK (lands IN ('next_step', 'next_turn')),
  ADD COLUMN failed_at      timestamptz,
  ADD COLUMN error          text,
  ADD COLUMN resends        text REFERENCES directives(id) ON DELETE SET NULL,
  ADD COLUMN interrupt_only boolean DEFAULT false;

-- A finished turn is held open while a steer sent to it is unread: the
-- sweep compares that steer's delivery with the Run's last turn end. Both
-- are a few rows among the thousands a Run records; these find them
-- without reading the rest.
CREATE INDEX events_run_directive_delivered_idx ON events (run_id, (payload->>'directiveId'), cursor)
  WHERE event_type = 'run.directive.delivered';
CREATE INDEX events_run_turn_end_idx ON events (run_id, cursor)
  WHERE event_type = 'agent.session.stopped';

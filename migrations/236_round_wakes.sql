-- 236_round_wakes.sql — round: a conducted review round's wake, its
-- findings at the fix bar listed (delivery.WakeRound), bounded at 2,000
-- characters rather than a decision's 300.
-- NOT VALID: no scan of the table under ACCESS EXCLUSIVE; every row there
-- holds a kind the constraint it replaces allowed, all of which this allows.
ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_kind_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_kind_check
  CHECK (kind IN ('decision', 'escalation', 'question', 'safety', 'steer_read', 'steer_failed', 'pr_merged', 'pr_closed',
                  'published', 'publish_refused', 'publish_stalled', 'checkout', 'stalled', 'unblocked', 'feedback', 'wait',
                  'recovered', 'round')) NOT VALID;

-- 250_checks_wakes.sql — checks: the checks gate asks a conducted task's
-- conductor to decide a writer's checks report (delivery.WakeChecks), its
-- line the report's summary and one question per check, bounded at 2,000
-- characters (conductor_wakes_line_check).
-- NOT VALID: no scan of the table under ACCESS EXCLUSIVE; every row there
-- holds a kind the constraint it replaces allowed, all of which this allows.
ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_kind_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_kind_check
  CHECK (kind IN ('decision', 'escalation', 'question', 'safety', 'steer_read', 'steer_failed', 'pr_merged', 'pr_closed',
                  'published', 'publish_refused', 'publish_stalled', 'checkout', 'stalled', 'unblocked', 'feedback', 'wait',
                  'recovered', 'round', 'checks')) NOT VALID;
ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_line_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_line_check
  CHECK (kind = 'escalation' OR length(line) <= CASE kind WHEN 'stalled' THEN 2000 WHEN 'wait' THEN 2000 WHEN 'round' THEN 2000
    WHEN 'checks' THEN 2000 ELSE 300 END) NOT VALID;

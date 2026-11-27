-- 277_idle_turn_wakes.sql — a conductor whose turn ended with nothing that
-- will wake it (no Run at work, no wait, no open question, no pull request)
-- is woken once for it (delivery.WakeIdleTurn).
--
-- conductor_wakes gains kind idle_turn, its line bounded at 600: it quotes
-- the start of the conductor's last message. Every kind 273 allows is still
-- allowed, and every other kind's bound is 271's. NOT VALID: no scan under
-- ACCESS EXCLUSIVE; both checks only widen the ones they replace.
ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_kind_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_kind_check
  CHECK (kind IN ('decision', 'escalation', 'question', 'safety', 'steer_read', 'steer_failed', 'pr_merged', 'pr_closed',
                  'published', 'publish_refused', 'publish_stalled', 'checkout', 'stalled', 'unblocked', 'feedback', 'wait',
                  'recovered', 'round', 'checks', 'workflow', 'epic_merge', 'owner_github', 'idle_turn')) NOT VALID;
ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_line_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_line_check
  CHECK (kind = 'escalation' OR length(line) <= CASE kind WHEN 'stalled' THEN 2000 WHEN 'wait' THEN 2000 WHEN 'round' THEN 2000
    WHEN 'checks' THEN 16000 WHEN 'recovered' THEN 600 WHEN 'workflow' THEN 600 WHEN 'idle_turn' THEN 600 ELSE 300 END) NOT VALID;

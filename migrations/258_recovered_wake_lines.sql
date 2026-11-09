-- 258_recovered_wake_lines.sql — recovered: a recovery refused because its
-- Run's secret copy is gone (delivery.RecoverySecretsExpired) tells the
-- conductor why and what to tell the person, which does not fit a
-- decision's 300 characters; bounded at 600 (delivery.recoveredLineChars).
-- Every other kind's bound as 253 left it.
-- NOT VALID: no scan of the table under ACCESS EXCLUSIVE; the bound only
-- widens 253's, so no row there can break it.
ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_line_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_line_check
  CHECK (kind = 'escalation' OR length(line) <= CASE kind WHEN 'stalled' THEN 2000 WHEN 'wait' THEN 2000 WHEN 'round' THEN 2000
    WHEN 'checks' THEN 2000 WHEN 'recovered' THEN 600 ELSE 300 END) NOT VALID;

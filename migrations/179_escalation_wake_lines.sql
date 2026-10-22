-- 179_escalation_wake_lines.sql — an escalation's wake names the findings it
-- is about and the direction the person must give (not_converging): longer
-- than other reasons' ids and counts. delivery.recordWakeTx clips to the same
-- bounds.
ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_line_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_line_check
  CHECK (length(line) <= CASE kind WHEN 'stalled' THEN 2000 WHEN 'escalation' THEN 1200 ELSE 300 END);

-- 179_escalation_wake_lines.sql — an escalation's wake names the findings it
-- is about and the direction the person must give (not_converging): longer
-- than other reasons' ids and counts. delivery.recordWakeTx clips to the same
-- bounds.
--
-- NOT VALID: the new bound only widens 090's (escalation 300 → 1200, the
-- others unchanged), so every existing row, which 090's check admitted,
-- satisfies it already. Validating would scan conductor_wakes under this
-- ALTER's AccessExclusiveLock, blocking every wake insert, for no row it
-- could refuse; new and updated rows are checked either way.
ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_line_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_line_check
  CHECK (length(line) <= CASE kind WHEN 'stalled' THEN 2000 WHEN 'escalation' THEN 1200 ELSE 300 END) NOT VALID;

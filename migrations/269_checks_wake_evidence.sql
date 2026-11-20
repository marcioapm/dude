-- 269_checks_wake_evidence.sql — a checks wake now carries the evidence
-- the conductor decides each reported pass by: the Run's commits at the
-- report, whether its tree was clean, and its shell calls at the latest
-- commit, newest first, up to delivery.EvidenceCalls, each with its command,
-- its recorded exit and its output's tail (phases.checksWakeLine). That does
-- not fit 2000 characters; a checks line is bounded at 16000
-- (delivery.checksLineChars). Every other kind's bound as 264 left it.
-- NOT VALID: no scan of the table under ACCESS EXCLUSIVE; the bound only
-- widens 264's, so no row there can break it.
ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_line_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_line_check
  CHECK (kind = 'escalation' OR length(line) <= CASE kind WHEN 'stalled' THEN 2000 WHEN 'wait' THEN 2000 WHEN 'round' THEN 2000
    WHEN 'checks' THEN 16000 WHEN 'recovered' THEN 600 WHEN 'workflow' THEN 600 ELSE 300 END) NOT VALID;

-- 174_finding_acceptance.sql — what a reviewer said must hold once a
-- finding is fixed, and its verdicts on the findings it was shown.
--
-- acceptance: the reviewer's criterion for a finding (delivery.Finding.Acceptance),
--   written once when the finding is raised; NULL when it gave none. A later
--   reviewer judges against it and never changes it.
-- caused_by_fix: the reviewer said the problem is in code the preceding fix wrote.
ALTER TABLE review_findings
  ADD COLUMN acceptance text,
  ADD COLUMN caused_by_fix boolean NOT NULL DEFAULT false;

-- verdicts: a review Run's judgement of the findings it was shown (runs.finding_ids),
--   by finding id: "fixed" or "still". Absent: the reviewer said nothing about it.
--   Replaced whole on each report of the Run, as its findings are.
ALTER TABLE runs ADD COLUMN verdicts jsonb NOT NULL DEFAULT '{}'::jsonb;

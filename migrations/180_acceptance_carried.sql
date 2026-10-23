-- 180_acceptance_carried.sql — a criterion verdict a later test round kept
-- from the round before it, rather than tested again.
--
-- carried_from: the test Run whose verdict and evidence this row repeats
--   (delivery.carried): the previous round passed it, or a person waived or
--   accepted it there, at heads this round's descend from. NULL for a
--   verdict this Run judged itself. A carried row's evidence names files of
--   that Run, not this one.
-- A nullable column with no default: no existing row is rewritten.
ALTER TABLE acceptance_verdicts ADD COLUMN carried_from text REFERENCES runs(id) ON DELETE SET NULL;

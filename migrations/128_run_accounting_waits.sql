-- 128_run_accounting_waits.sql — how many times in a row a phase Run's
-- publish found its accounting unavailable for a reason that may pass (a
-- timeout, an unreachable lux): the next try waits 5 s doubling with each,
-- at most 5 min, so a hung host does not hold every sweep.
--
-- A column with a constant default: no table rewrite.

ALTER TABLE runs ADD COLUMN accounting_waits integer NOT NULL DEFAULT 0;

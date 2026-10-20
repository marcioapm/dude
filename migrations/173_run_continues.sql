-- 173_run_continues.sql — a review or test round that goes on from the
-- previous round of its role and category (delivery's continueOrCreateTx)
-- is a new runs row: it keeps the round's own creation key, findings,
-- verdicts and outcome.
--
-- continues_run_id: the previous round's Run, and continues_how how this
--   one goes on from it: 'resume', it took over that Run's lux Run (the
--   agent's conversation and workspace); 'replay', it is a fresh agent
--   given that conversation, from the ledger, in its first message.
-- agent_cost_base, lux_ai_base, lux_compute_base: what the lux Run had
--   cost when a resume took it over. The harness and lux report a lux Run's
--   cost cumulatively; each row stores its own share (reported minus base),
--   so a task's total stays a plain sum over its Runs.
ALTER TABLE runs
  ADD COLUMN continues_run_id text REFERENCES runs(id),
  ADD COLUMN continues_how text CHECK (continues_how IN ('resume', 'replay')),
  ADD COLUMN agent_cost_base numeric NOT NULL DEFAULT 0,
  ADD COLUMN lux_ai_base numeric NOT NULL DEFAULT 0,
  ADD COLUMN lux_compute_base numeric NOT NULL DEFAULT 0,
  ADD CONSTRAINT runs_continues_check CHECK ((continues_run_id IS NULL) = (continues_how IS NULL));

CREATE INDEX runs_continues_idx ON runs (continues_run_id) WHERE continues_run_id IS NOT NULL;

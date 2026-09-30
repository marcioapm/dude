-- What lux says a Run cost. lux prices a Run with its cost plugins — the
-- LLM proxy's metered AI models, the host's compute — and reports it on
-- GET /v1/runs/{id}/cost, settling it over up to seven days. The
-- orchestrator reads it and keeps the USD amounts of the two families dude
-- shows; amounts in other currencies are not kept.
--
-- NULL is "lux has reported nothing for this family", not zero: an AI cost
-- of 0 is a price, and replaces the harness's own figure (061).
ALTER TABLE runs
  ADD COLUMN lux_ai_usd       numeric,
  ADD COLUMN lux_compute_usd  numeric,
  -- lux's status for the whole Run's cost: pending, incomplete, complete,
  -- final. Only final never changes again; NULL: never read. Not checked:
  -- a status lux adds later is kept as sent and read as not final.
  ADD COLUMN lux_cost_status  text,
  -- When lux last answered, and when to ask it next (NULL: as soon as it
  -- has a lux Run).
  ADD COLUMN lux_cost_read_at timestamptz,
  ADD COLUMN lux_cost_next_at timestamptz;

-- The cost poller's work list: agents' Runs on lux whose cost is not yet
-- final (a preview's cost is not a task's).
CREATE INDEX runs_lux_cost_due_idx ON runs (lux_cost_next_at NULLS FIRST)
  WHERE kind = 'agent' AND lux_run_id IS NOT NULL AND lux_cost_status IS DISTINCT FROM 'final';

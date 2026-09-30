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
  -- When lux last answered, and when to ask it next. NULL next: no read is
  -- due — no lux Run, a preview, final, or ended more than eight days ago
  -- (lux settles within seven).
  ADD COLUMN lux_cost_read_at timestamptz,
  ADD COLUMN lux_cost_next_at timestamptz;

-- The cost poller's work list: only Runs that still have a read due.
CREATE INDEX runs_lux_cost_next_idx ON runs (lux_cost_next_at) WHERE lux_cost_next_at IS NOT NULL;

-- An agent's Run joins the work list when it gets a lux Run (a preview's
-- cost is not a task's). The poller takes it off (phases.Costs.read). The
-- eight days are phases.costPatience.
CREATE FUNCTION runs_lux_cost_due() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    NEW.lux_cost_next_at := now();
  ELSIF OLD.lux_run_id IS DISTINCT FROM NEW.lux_run_id THEN
    NEW.lux_cost_next_at := now();
  END IF;
  RETURN NEW;
END $$;

CREATE TRIGGER runs_lux_cost_due
  BEFORE INSERT OR UPDATE OF lux_run_id ON runs
  FOR EACH ROW WHEN (NEW.kind = 'agent' AND NEW.lux_run_id IS NOT NULL
                     AND (NEW.ended_at IS NULL OR NEW.ended_at > now() - interval '8 days'))
  EXECUTE FUNCTION runs_lux_cost_due();

UPDATE runs SET lux_cost_next_at = now()
  WHERE kind = 'agent' AND lux_run_id IS NOT NULL
    AND (ended_at IS NULL OR ended_at > now() - interval '8 days');

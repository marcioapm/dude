-- 105_session_model.sql — a session may choose its agent's tier and harness.
--
-- tier     the model tier its brainstorm runs on; NULL: the organisation's
--          Brainstorm setting. A tier removed sets it back to NULL (the
--          session follows the organisation from its next start).
-- harness  the coding agent its brainstorm runs on; NULL: the
--          organisation's.
--
-- Each overrides the organisation's on its own. Read when a Run is built
-- (phases.brainstormSpec); a resumed Run keeps what it was submitted with.
-- Whether the pair fits (delivery.SessionModelProblem) is checked when
-- either is set, by the orchestrator, against the organisation's value for
-- the other.
--
-- Catalog changes only, as in 103: the columns have no default, so adding
-- them rewrites nothing, and the foreign key and check are NOT VALID, so
-- they bind every write from now on without scanning sessions under the
-- ACCESS EXCLUSIVE lock. Every existing row holds NULL in both, which
-- satisfies them: there is nothing for a scan to find. The index on tier
-- is built concurrently by 106, outside this lock; until it exists a tier's
-- removal scans sessions, all NULL. sessions keeps its RLS policy and
-- grants; new columns are covered by them.

ALTER TABLE sessions ADD COLUMN tier text, ADD COLUMN harness text;
ALTER TABLE sessions ADD CONSTRAINT sessions_tier_fkey
  FOREIGN KEY (tier) REFERENCES model_tiers(id) ON DELETE SET NULL NOT VALID;
ALTER TABLE sessions ADD CONSTRAINT sessions_harness_check
  CHECK (harness IN ('opencode', 'claude-code', 'codex')) NOT VALID;

-- A removed tier a session chose: the trigger sets each such session back
-- to the organisation's and tells its members, on the session, one event
-- per row it changed. The UPDATE locks each row and re-reads it, so a
-- session moved to another tier while the removal waited is left alone and
-- told nothing; ON DELETE SET NULL then finds nothing left, and stays as
-- the net for a row the trigger cannot see. Nothing when the organisation
-- itself is going (its row is already gone by the time its tiers cascade).
--
-- The event id is newId("event")'s shape (packages/domain ids.ts): "evt_",
-- the clock's milliseconds in base 36, nine wide, then 16 hex, so the
-- events sort by time as the ones the services write.
CREATE FUNCTION model_tier_session_fallback() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  ms bigint := (extract(epoch FROM clock_timestamp()) * 1000)::bigint;
  b36 text := '';
BEGIN
  IF NOT EXISTS (SELECT 1 FROM organizations o WHERE o.id = OLD.organization_id) THEN
    RETURN OLD;
  END IF;
  WHILE ms > 0 LOOP
    b36 := substr('0123456789abcdefghijklmnopqrstuvwxyz', (ms % 36)::int + 1, 1) || b36;
    ms := ms / 36;
  END LOOP;
  WITH cleared AS (
    UPDATE sessions SET tier = NULL, updated_at = now() WHERE tier = OLD.id RETURNING id, organization_id
  )
  INSERT INTO events (id, organization_id, event_type, session_id, actor_type, actor_id, source, correlation_id, payload)
  SELECT 'evt_' || lpad(b36, 9, '0') || substr(replace(gen_random_uuid()::text, '-', ''), 1, 16),
         c.organization_id, 'session.model.fallback', c.id, 'system', 'dude', 'control-plane', c.id,
         jsonb_build_object('tier', jsonb_build_object('id', OLD.id, 'name', OLD.name))
  FROM cleared c;
  RETURN OLD;
END $$;

CREATE TRIGGER model_tier_session_fallback BEFORE DELETE ON model_tiers
  FOR EACH ROW EXECUTE FUNCTION model_tier_session_fallback();

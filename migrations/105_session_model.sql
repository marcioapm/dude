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
-- Whether the pair fits (delivery.HarnessFits) is checked when either is
-- set, by the orchestrator, against the organisation's value for the other.
--
-- Catalog changes only, as in 103: the columns have no default, so adding
-- them rewrites nothing, and the foreign key and check are NOT VALID, so
-- they bind every write from now on without scanning sessions under the
-- ACCESS EXCLUSIVE lock. Every existing row holds NULL in both, which
-- satisfies them: there is nothing for a scan to find. sessions keeps its
-- RLS policy and grants; new columns are covered by them.

ALTER TABLE sessions ADD COLUMN tier text, ADD COLUMN harness text;
ALTER TABLE sessions ADD CONSTRAINT sessions_tier_fkey
  FOREIGN KEY (tier) REFERENCES model_tiers(id) ON DELETE SET NULL NOT VALID;
ALTER TABLE sessions ADD CONSTRAINT sessions_harness_check
  CHECK (harness IN ('opencode', 'claude-code', 'codex')) NOT VALID;

-- Only sessions that chose a tier look it up on a tier's removal.
CREATE INDEX sessions_tier_idx ON sessions (tier) WHERE tier IS NOT NULL;

-- A removed tier a session chose: its members are told, on the session,
-- before ON DELETE SET NULL clears it. Not when the organisation itself is
-- going (its row is already gone by the time its tiers cascade).
CREATE FUNCTION model_tier_session_fallback() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO events (id, organization_id, event_type, session_id, actor_type, actor_id, source, correlation_id, payload)
  SELECT 'evt_' || replace(gen_random_uuid()::text, '-', ''), s.organization_id, 'session.model.fallback', s.id,
         'system', 'dude', 'control-plane', s.id,
         jsonb_build_object('tier', jsonb_build_object('id', OLD.id, 'name', OLD.name))
  FROM sessions s
  WHERE s.tier = OLD.id AND EXISTS (SELECT 1 FROM organizations o WHERE o.id = OLD.organization_id);
  RETURN OLD;
END $$;

CREATE TRIGGER model_tier_session_fallback BEFORE DELETE ON model_tiers
  FOR EACH ROW EXECUTE FUNCTION model_tier_session_fallback();

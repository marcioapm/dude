-- 111_run_children.sql — the child sessions (subagents) a Run's agent has
-- open, for the stall detector.
--
-- children: the parent's tool call id → {childId, kind, title, lastEventAt,
-- lastEvent, waitingOn?: {permission, patterns}}. The translator writes an
-- entry on every agent.child.* event and drops it when the parent's call
-- completes. An open call whose child moves is not stalled: stalledCall
-- measures it from the later of when it opened and the child's last event.
--
-- Adding a column with a constant default rewrites nothing (Postgres 11+);
-- it takes ACCESS EXCLUSIVE on runs only for the catalogue change.
ALTER TABLE runs ADD COLUMN children jsonb NOT NULL DEFAULT '{}';

-- run_stalled (from 090): a Run reported for an open call is no longer
-- stalled once the call's child did something after the report, as
-- phases.callSince measures it.
CREATE OR REPLACE FUNCTION run_stalled(r runs) RETURNS boolean LANGUAGE sql STABLE AS $$
  SELECT r.status = 'running' AND r.stall_reported_at IS NOT NULL AND (
    ('call' = ANY (r.stall_reasons) AND EXISTS (SELECT 1 FROM jsonb_each_text(r.open_tool_calls_at) oc
      WHERE GREATEST(oc.value::timestamptz, (r.children->oc.key->>'lastEventAt')::timestamptz) <= r.stall_reported_at))
    OR ('files' = ANY (r.stall_reasons) AND r.files_changed_at <= r.stall_reported_at)
    OR ('silent' = ANY (r.stall_reasons)
      AND COALESCE(r.agent_active_at, r.files_changed_at, r.started_at) <= r.stall_reported_at))
$$;

-- The scripted agent's fake/subagent and fake/subagent-ask (its
-- implementer starts a task subagent, which with -ask asks a permission
-- lux answers) are test models a tier may request.
ALTER TABLE model_tiers DROP CONSTRAINT model_tiers_model_check;
ALTER TABLE model_tiers ADD CONSTRAINT model_tiers_model_check
  CHECK (model IN ('fake/scripted', 'fake/hang', 'fake/tools', 'fake/request', 'fake/wait', 'fake/live', 'fake/ask', 'fake/ask-several',
                   'fake/command', 'fake/stuck', 'fake/stall', 'fake/silent', 'fake/lookup', 'fake/subagent', 'fake/subagent-ask')
         OR (length(model) BETWEEN 1 AND 200 AND model !~ '[[:space:]/]'));

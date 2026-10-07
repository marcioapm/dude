-- 085_stalled_runs.sql — dude notices a phase Run that makes no progress,
-- and tells its conductor or its owner the facts; either may restart it.

-- open_tool_calls_at: each open tool call's id → when it opened (the
-- translator stamps a call the first time it sees it open; open_tool_calls
-- stays the set). files_changed_at: when the Run's placement entered
-- running (its start, or a resume once it runs again), then each time its
-- live diff reads a new checksum or a commit of its lands.
--
-- stall_reported_at: when it was last reported as making no progress;
-- stall_reasons why ('call': a tool call open the whole window; 'files': a
-- Run that changes code whose files did not change; 'silent': no tool call
-- open, and its agent said, thought and did nothing — counted from
-- agent_active_at, or before the agent did anything from when its placement
-- entered running), stall_fingerprint its facts then (the open calls, the diff's
-- checksum and, while silent, when the silence began: unchanged facts wait
-- longer to be reported again), stall_usage lux's CPU and network counters
-- then (what the next report's window is measured from). stall_left_at: its
-- owner chose Leave it on the banner.
--
-- restart_note: why a person or the conductor restarted the Run this one
-- replaces, told to its agent. tier_override: the model tier it was
-- restarted on, in place of its role's. replaced_by: the Run that replaced
-- this one in its slot of the delivery.
ALTER TABLE runs
  ADD COLUMN open_tool_calls_at jsonb NOT NULL DEFAULT '{}',
  ADD COLUMN files_changed_at timestamptz,
  ADD COLUMN stall_reported_at timestamptz,
  ADD COLUMN stall_reasons text[] NOT NULL DEFAULT '{}',
  ADD COLUMN stall_fingerprint text,
  ADD COLUMN stall_usage jsonb,
  ADD COLUMN stall_left_at timestamptz,
  ADD COLUMN restart_note text CHECK (length(restart_note) <= 4000),
  ADD COLUMN tier_override text REFERENCES model_tiers(id) ON DELETE SET NULL,
  ADD COLUMN replaced_by text REFERENCES runs(id) ON DELETE SET NULL;

-- Live phase Runs from before: a Run already hung in a call writes nothing
-- more, so nothing would ever stamp its call, and it would never be
-- reported. Each call open now is open since the ledger's event opening it
-- (the earliest, as the translator keeps the first sighting), else since
-- the Run started; its files changed last at its latest diff or commit,
-- else at its start. Only rows still at the defaults above, so a second
-- application changes nothing.
UPDATE runs r SET
  open_tool_calls_at = COALESCE((SELECT jsonb_object_agg(c, to_jsonb(COALESCE(
      (SELECT min(e.occurred_at) FROM events e WHERE e.run_id = r.id AND e.event_type = 'agent.tool.called'
         AND e.payload->>'callId' = c),
      r.active_since, r.started_at, r.created_at)))
    FROM unnest(r.open_tool_calls) c), '{}'),
  files_changed_at = COALESCE(
    (SELECT max(e.occurred_at) FROM events e WHERE e.run_id = r.id
       AND e.event_type IN ('run.diff.updated', 'git.commit_created')),
    r.active_since, r.started_at, r.created_at)
WHERE r.phase IS NOT NULL AND r.status IN ('scheduled', 'starting', 'running', 'paused')
  AND r.open_tool_calls_at = '{}' AND r.files_changed_at IS NULL;

-- A Run reported stalled still is, until what it was reported for changes:
-- the call open then has closed, its files changed (or it was resumed), or
-- its silent agent said or did something (phases.silentSince).
-- The one definition, for the Sessions badge and the task's banner.
CREATE FUNCTION run_stalled(r runs) RETURNS boolean LANGUAGE sql STABLE AS $$
  SELECT r.status = 'running' AND r.stall_reported_at IS NOT NULL AND (
    ('call' = ANY (r.stall_reasons) AND EXISTS (SELECT 1 FROM jsonb_each_text(r.open_tool_calls_at) c
      WHERE c.value::timestamptz <= r.stall_reported_at))
    OR ('files' = ANY (r.stall_reasons) AND r.files_changed_at <= r.stall_reported_at)
    OR ('silent' = ANY (r.stall_reasons)
      AND COALESCE(r.agent_active_at, r.files_changed_at, r.started_at) <= r.stall_reported_at))
$$;

-- stalled: a phase Run of the task makes no progress. Its line carries the
-- facts, which are longer than other reasons' ids and counts. safety is
-- kept for the reasons it already recorded; nothing records it now.
ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_kind_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_kind_check
  CHECK (kind IN ('decision', 'escalation', 'question', 'safety', 'steer_read', 'steer_failed', 'pr_merged', 'pr_closed',
                  'published', 'publish_refused', 'publish_stalled', 'checkout', 'stalled'));
ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_line_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_line_check
  CHECK (length(line) <= CASE WHEN kind = 'stalled' THEN 2000 ELSE 300 END);

-- The sweep reads a Run's diff checksum to tell whether a stall's facts
-- changed.
GRANT SELECT ON run_diffs TO dude_sweeper;

-- The scripted agent's fake/stall (a task's first reviewer hangs in an open
-- tool call) and fake/silent (it hangs with no call open, saying nothing)
-- are test models a tier may request.
ALTER TABLE model_tiers DROP CONSTRAINT model_tiers_model_check;
ALTER TABLE model_tiers ADD CONSTRAINT model_tiers_model_check
  CHECK (model IN ('fake/scripted', 'fake/hang', 'fake/tools', 'fake/request', 'fake/wait', 'fake/live', 'fake/ask', 'fake/command',
                   'fake/stuck', 'fake/stall', 'fake/silent')
         OR (length(model) BETWEEN 1 AND 200 AND model !~ '[[:space:]/]'));

-- The notifier tells a task's owner of a Run that makes no progress.
DROP INDEX IF EXISTS events_asks_idx;
CREATE INDEX events_asks_idx ON events (cursor)
  WHERE event_type IN ('question.asked', 'repository.requested', 'task.ready_to_merge', 'run.stalled');

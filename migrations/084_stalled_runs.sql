-- 084_stalled_runs.sql — dude notices a phase Run that makes no progress,
-- and tells its conductor or its owner the facts; either may restart it.

-- open_tool_calls_at: each open tool call's id → when it opened (the
-- translator stamps a call the first time it sees it open; open_tool_calls
-- stays the set). files_changed_at: when the Run started (or was resumed),
-- then each time its live diff reads a new checksum or a commit of its
-- lands.
--
-- stall_reported_at: when it was last reported as making no progress;
-- stall_reasons why ('call': a tool call open the whole window; 'files': a
-- Run that changes code whose files did not change), stall_fingerprint its
-- facts then (the open calls and the diff's checksum: unchanged facts wait
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

-- A Run reported stalled still is, until what it was reported for changes:
-- the call open then has closed, or its files changed (or it was resumed).
-- The one definition, for the Sessions badge and the task's banner.
CREATE FUNCTION run_stalled(r runs) RETURNS boolean LANGUAGE sql STABLE AS $$
  SELECT r.status = 'running' AND r.stall_reported_at IS NOT NULL AND (
    ('call' = ANY (r.stall_reasons) AND EXISTS (SELECT 1 FROM jsonb_each_text(r.open_tool_calls_at) c
      WHERE c.value::timestamptz <= r.stall_reported_at))
    OR ('files' = ANY (r.stall_reasons) AND r.files_changed_at <= r.stall_reported_at))
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

-- The notifier tells a task's owner of a Run that makes no progress.
DROP INDEX IF EXISTS events_asks_idx;
CREATE INDEX events_asks_idx ON events (cursor)
  WHERE event_type IN ('question.asked', 'repository.requested', 'task.ready_to_merge', 'run.stalled');

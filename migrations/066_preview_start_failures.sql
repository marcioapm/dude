-- 066_preview_start_failures.sql — a wakeable preview recovers from a Run
-- that fails to start.
--
-- How many starts of a wakeable preview's lux Run in a row ended failed or
-- lost before it ran (a resume lux accepted whose container would not
-- start, an image that would not pull, a host lost mid-start). Not zero:
-- below servers.previewStartAttempts, the preview wakes again by itself
-- after a backoff. Back to zero once a start runs.
ALTER TABLE runs ADD COLUMN start_failures integer NOT NULL DEFAULT 0;

-- Whether the current start of the preview's lux Run ran, in lux's own
-- order of its events (run_events.id, the stream's afterEvent): the id of
-- the state event that began the latest start (resuming), and of the latest
-- running. A failed or lost Run whose latest running is not after its
-- latest start never ran that start. Both are of the lux Run in lux_run_id
-- and go back to 0 with lux_after_event when it is replaced.
ALTER TABLE runs ADD COLUMN lux_start_event bigint NOT NULL DEFAULT 0,
  ADD COLUMN lux_ran_event bigint NOT NULL DEFAULT 0;

-- A preview Run that already ran before this migration: its last running
-- is at most its cursor (and above the 0 of no start yet seen), so a later
-- crash still resumes it.
UPDATE runs SET lux_ran_event = GREATEST(lux_after_event, 1)
  WHERE kind = 'preview' AND lux_run_id IS NOT NULL AND lux_state IN ('running', 'stopping', 'stopped');

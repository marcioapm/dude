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
-- order of its events (run_events.id, the stream's afterEvent): where the
-- latest start began, and the id of the latest running. A start begins at
-- dude's submit of a new lux Run (1: before every event of that Run) or at
-- a resuming state event. A failed or lost Run whose latest running is
-- below its latest start never ran that start. Both are of the lux Run in
-- lux_run_id and go back to 0 with lux_after_event when it is replaced.
ALTER TABLE runs ADD COLUMN lux_start_event bigint NOT NULL DEFAULT 0,
  ADD COLUMN lux_ran_event bigint NOT NULL DEFAULT 0;

-- Existing preview Runs: the events up to the cursor were applied without
-- these markers, so whether the current start ran is not known for a Run
-- caught mid-start, or failed or lost already. Every one is taken as having
-- run (start = ran = cursor). A Run that did run then crashes, or is lost,
-- and is resumed from its snapshot, as before this migration. A Run whose
-- start in fact never ran costs one more resume at worst: that resume's own
-- resuming event begins a new start, whose failure is then counted. The
-- other choice, taking it as a failed start, cancels a Run that may have
-- run and discards its snapshot.
UPDATE runs SET lux_start_event = lux_after_event, lux_ran_event = lux_after_event
  WHERE kind = 'preview' AND lux_run_id IS NOT NULL;

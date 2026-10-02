-- 066_preview_start_failures.sql — a wakeable preview recovers from a Run
-- that fails to start.
--
-- How many starts of a wakeable preview's lux Run in a row ended failed or
-- lost before it ran (a resume lux accepted whose container would not
-- start, an image that would not pull, a host lost mid-start). Not zero:
-- the next wake submits a new Run instead of resuming that one; below
-- servers.previewStartAttempts, the preview wakes again by itself after a
-- backoff. Back to zero once a start runs.
ALTER TABLE runs ADD COLUMN start_failures integer NOT NULL DEFAULT 0;

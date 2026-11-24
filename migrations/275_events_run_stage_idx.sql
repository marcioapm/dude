-- dude:no-transaction
-- A Run's latest start stage (run.stage): the API's startStage (the
-- control plane's run read, its task's live Runs) and the orchestrator's
-- phases.StartStageJSON (getSession) read it on every read of a Run, live
-- or not, running too. events_run_cursor_idx alone walks every event of a
-- Run after its last stage to find it, thousands for a busy one. Partial
-- on the literal type, so it holds only stages. Concurrently, a file of
-- its own, IF NOT EXISTS, as 270.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_run_stage_idx ON events (run_id, cursor) WHERE event_type = 'run.stage';

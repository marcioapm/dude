-- 125_task_source_mode.sql — what dude does first with work it takes over:
-- 'continue' builds on it with an implementer before the review loop;
-- 'review' starts at review (what every source did before this column).
--
-- Chosen with the source, fixed with it once the task's delivery starts.
-- A new source's default comes from what it says: a draft pull request or a
-- bare branch is unfinished ('continue'), a pull request marked ready is
-- not ('review'). The orchestrator's source check decides that default.
--
-- A defaulted column only: every existing source keeps 'review'.

ALTER TABLE task_sources ADD COLUMN mode text NOT NULL DEFAULT 'review' CHECK (mode IN ('continue', 'review'));

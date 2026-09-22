-- 007_drop_workflow_lookup.sql — remove a superseded escape hatch.
--
-- 004 added `workflow_run_organization` so a caller holding only a run id
-- could resolve its tenant. That was the wrong answer: a caller that cannot
-- name the tenant is not authorized to act on the run.
--
-- WorkflowRuntime now takes the acting organization as an argument and lets
-- row-level security reject a mismatch, so the function has no callers. It is
-- dropped rather than left behind, because a SECURITY DEFINER function that
-- nobody needs is an invitation to reintroduce the pattern.

DROP FUNCTION IF EXISTS workflow_run_organization(text);

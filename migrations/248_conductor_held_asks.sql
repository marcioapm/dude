-- 248_conductor_held_asks.sql
-- Conductor-first (PR4b-2): a reviewer's question and an agent's
-- repository request go to the task's conductor first when one decides
-- the delivery. The conductor answers or forwards a question; it denies a
-- request, approves read access to a repository of the task's project, or
-- forwards it. Forwarded, or handed over at the deadline (the conductor
-- failed, has no model, or did not decide in time), the item is the
-- person's. Every existing row stays the person's (the default).
ALTER TABLE review_questions
  ADD COLUMN addressee text NOT NULL DEFAULT 'person' CHECK (addressee IN ('person', 'conductor'));

ALTER TABLE repository_requests
  ADD COLUMN addressee text NOT NULL DEFAULT 'person' CHECK (addressee IN ('person', 'conductor'));

-- The handover sweep (phases handOverReviewQuestions and
-- handOverRepositoryRequests) reads every open conductor-held item each
-- pass, across organizations; the board asks per task whether one holds
-- it. Few rows at once: the partial indexes stay small.
CREATE INDEX review_questions_conductor_open_idx ON review_questions (task_id, created_at)
  WHERE status = 'open' AND addressee = 'conductor';
CREATE INDEX repository_requests_conductor_pending_idx ON repository_requests (task_id, created_at)
  WHERE status = 'pending' AND addressee = 'conductor';

-- The sweep reads them as dude_sweeper (repository_requests is granted
-- already, migration 020).
GRANT SELECT (id, organization_id, task_id, run_id, status, addressee, created_at) ON review_questions TO dude_sweeper;

-- A reviewer's question its conductor still holds dies with the attempt it
-- was asked in, as a Run's question dies with its Run (015) and a request
-- with its Run (020): its review Run has ended already, so here it is the
-- delivery's end — merged, closed, failed, or a Start over completing it —
-- or the task closed while stopped. A Stop alone (the delivery aborted)
-- keeps it for a pick-up. Nobody is asked an abandoned attempt's question,
-- and the partial index above keeps only live ones.
CREATE FUNCTION withdraw_held_review_questions(p_task text) RETURNS void LANGUAGE sql AS $$
  UPDATE review_questions SET status = 'withdrawn', answered_at = now()
  WHERE task_id = p_task AND status = 'open' AND addressee = 'conductor'
$$;

CREATE FUNCTION withdraw_held_review_questions_of_delivery() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  PERFORM withdraw_held_review_questions(NEW.task_id);
  RETURN NULL;
END $$;

CREATE TRIGGER workflow_runs_withdraw_held_review_questions
  AFTER UPDATE OF status ON workflow_runs
  FOR EACH ROW WHEN (NEW.workflow_type = 'task.delivery' AND NEW.task_id IS NOT NULL
    AND NEW.status IN ('completed', 'failed', 'dead_lettered') AND OLD.status IS DISTINCT FROM NEW.status)
  EXECUTE FUNCTION withdraw_held_review_questions_of_delivery();

CREATE FUNCTION withdraw_held_review_questions_of_task() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  PERFORM withdraw_held_review_questions(NEW.id);
  RETURN NULL;
END $$;

CREATE TRIGGER tasks_withdraw_held_review_questions
  AFTER UPDATE OF status, resolution ON tasks
  FOR EACH ROW WHEN ((NEW.status = 'done' OR NEW.status = 'aborted' AND NEW.resolution IS NOT NULL)
    AND (OLD.status, OLD.resolution) IS DISTINCT FROM (NEW.status, NEW.resolution))
  EXECUTE FUNCTION withdraw_held_review_questions_of_task();

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
GRANT SELECT (id, organization_id, task_id, status, addressee, created_at) ON review_questions TO dude_sweeper;

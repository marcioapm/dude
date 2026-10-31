-- 235_finding_evidence.sql — a finding's evidence, and a writer's outcome.
--
-- On review_findings, the reviewer's own words (delivery.Finding):
-- trivial: the smallest fix is obvious, local and a few lines; NULL not said
--   (stored before 235, or omitted).
-- introduced: this round's range introduced or worsened it; NULL not said.
-- evidence_kind, evidence: how it is known, and the evidence itself.
--   An improvised kind is stored NULL.
-- scenario: the entry point, the state it needs, and what goes wrong.
-- And the layer's facts, never the reviewer's:
-- unverified: a finding at medium or above missing its file, line,
--   scenario, evidence_kind or evidence. Presence only; its severity
--   stands. Rows from before 235 are false: never marked retroactively.
-- review_round, touches_fix_run_id: the round that raised it and the fix
--   whose lines it touches; written by later steps, NULL until then.
ALTER TABLE review_findings
  ADD COLUMN trivial boolean,
  ADD COLUMN introduced boolean,
  ADD COLUMN evidence_kind text CHECK (evidence_kind IN ('failing_test', 'mutation', 'command', 'query_plan', 'trace')),
  ADD COLUMN evidence text,
  ADD COLUMN scenario text,
  ADD COLUMN unverified boolean NOT NULL DEFAULT false,
  ADD COLUMN review_round smallint,
  ADD COLUMN touches_fix_run_id text REFERENCES runs(id) ON DELETE SET NULL;

-- On runs: a writer's own account of its work, from its final checks
-- document (delivery.ParseChecks): done, partial or blocked, and its one
-- line. NULL for a Run that is no writer, or ended before 235.
-- review_round, fix_hunks: a review Run's round and a fix's changed hunks,
-- written by later steps.
ALTER TABLE runs
  ADD COLUMN writer_outcome text CHECK (writer_outcome IN ('done', 'partial', 'blocked')),
  ADD COLUMN writer_outcome_note text,
  ADD COLUMN review_round smallint,
  ADD COLUMN fix_hunks jsonb;

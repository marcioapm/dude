-- run_project_secrets' header (129) says a copy is "retained only through
-- final output and collection". It is kept for as long as something can
-- still produce or read the Run's output (phases.copyNeeded): while the
-- Run is kept to be resumed or for its next round, and for 30 days after
-- it ended if it published a text file or is a failed or aborted writer
-- recover_run may push. The catalog says so.
COMMENT ON TABLE run_project_secrets IS
  'Every secret value sent to a Run, plaintext; application access only, never selected by a client route. '
  'Deleted once nothing can produce or read the Run''s output (phases.copyNeeded): kept while the Run is kept '
  '(runs.keep) to be resumed or handed to its next round, and for 30 days after it ended if it published a text '
  'file or is a failed or aborted writer recover_run may push; otherwise at its collection, or by the backstop '
  'from an hour after it ended. Past those 30 days a recovery of a Run given secrets is refused.';

-- dude:no-transaction
-- A task's credential asks (question.asked of kind credential): what its
-- pull request waits on a person for. Read per task row by credentialWait
-- (work.ts's credentialWaitJson: /v1/navigation, the task) and by
-- delivery.askCredentialTx's once-per-key check. events_asks_v2_idx holds
-- every ask of the organization, so a task's probe through it reads them
-- all. Partial on the literal predicates those readers write.
-- Concurrently, a file of its own, IF NOT EXISTS, as 255.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_task_credential_asks_idx ON events (task_id, cursor)
  WHERE event_type = 'question.asked' AND payload->>'kind' = 'credential';

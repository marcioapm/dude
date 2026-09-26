-- Who drives a task: one person, who is told when it waits on someone
-- and who answers for it. Until there are users (plan §53) a person is an
-- API key, named; the owner is one of the organization's keys, and moves
-- to a user when sign-in lands.
ALTER TABLE tasks ADD COLUMN owner_key_id text REFERENCES api_keys(id) ON DELETE SET NULL;
CREATE INDEX tasks_owner_idx ON tasks (owner_key_id) WHERE owner_key_id IS NOT NULL;

-- Tasks from before: owned by whoever created them if the ledger says so;
-- one an agent created, by the owner of the task it was working on; else
-- by the organization's first key — someone is always driving. A revoked
-- key could never answer, so it drives nothing.
UPDATE tasks t SET owner_key_id = k.id
FROM events e JOIN api_keys k ON k.id = e.actor_id
WHERE e.task_id = t.id AND e.event_type = 'task.created' AND e.actor_type = 'human'
  AND k.kind = 'user' AND k.revoked_at IS NULL;

UPDATE tasks t SET owner_key_id = parent.owner_key_id
FROM runs r JOIN tasks parent ON parent.id = r.task_id
WHERE t.owner_key_id IS NULL AND r.id = t.created_by_run_id;

UPDATE tasks t SET owner_key_id =
  (SELECT k.id FROM api_keys k WHERE k.organization_id = t.organization_id AND k.kind = 'user'
     AND k.revoked_at IS NULL ORDER BY k.created_at LIMIT 1)
WHERE t.owner_key_id IS NULL;

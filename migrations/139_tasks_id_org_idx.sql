-- dude:no-transaction
-- A task's id with its organization, unique: the key model_calls (141)
-- names a task by, so a call cannot name another organisation's task. 141
-- attaches it as a constraint (UNIQUE USING INDEX). Built concurrently, so
-- tasks are read and written throughout; a file of its own, outside a
-- transaction. IF NOT EXISTS makes a retry after a crash before it was
-- recorded a no-op.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS tasks_id_organization_id_key ON tasks (id, organization_id);

-- "Work item" becomes "task": the unit of work people ask for, deliver and
-- merge. Renamed in place — tables, columns, types, indexes, constraints,
-- policies, triggers, functions — so every row carries over, and the
-- JSON the ledger and the workflows keep is renamed with it. Keys stay
-- (TEXT-20); ids keep their "wi_" prefix, which is only an id.

ALTER TABLE work_items RENAME TO tasks;
ALTER TABLE work_item_repositories RENAME TO task_repositories;
ALTER TYPE work_item_status RENAME TO task_status;

ALTER TABLE task_repositories RENAME COLUMN work_item_id TO task_id;
ALTER TABLE runs RENAME COLUMN work_item_id TO task_id;
ALTER TABLE workflow_runs RENAME COLUMN work_item_id TO task_id;
ALTER TABLE events RENAME COLUMN work_item_id TO task_id;
ALTER TABLE questions RENAME COLUMN work_item_id TO task_id;
ALTER TABLE directives RENAME COLUMN work_item_id TO task_id;
ALTER TABLE pull_requests RENAME COLUMN work_item_id TO task_id;
ALTER TABLE review_findings RENAME COLUMN work_item_id TO task_id;
ALTER TABLE repository_requests RENAME COLUMN work_item_id TO task_id;
ALTER TABLE cost_samples RENAME COLUMN work_item_id TO task_id;
ALTER TABLE projects RENAME COLUMN next_work_item_number TO next_task_number;

-- Names that say what they are.
ALTER INDEX work_items_pkey RENAME TO tasks_pkey;
ALTER INDEX work_items_project_status_idx RENAME TO tasks_project_status_idx;
ALTER INDEX work_items_org_created_idx RENAME TO tasks_org_created_idx;
ALTER INDEX work_items_number_unique RENAME TO tasks_number_unique;
ALTER INDEX work_item_repositories_pkey RENAME TO task_repositories_pkey;
ALTER INDEX work_item_repositories_repository_idx RENAME TO task_repositories_repository_idx;
ALTER INDEX runs_work_item_idx RENAME TO runs_task_idx;
ALTER INDEX events_work_item_cursor_idx RENAME TO events_task_cursor_idx;
ALTER INDEX workflow_runs_work_item_idx RENAME TO workflow_runs_task_idx;
ALTER INDEX pull_requests_work_item_idx RENAME TO pull_requests_task_idx;
ALTER INDEX review_findings_work_item_idx RENAME TO review_findings_task_idx;

ALTER TABLE tasks RENAME CONSTRAINT work_items_organization_id_fkey TO tasks_organization_id_fkey;
ALTER TABLE tasks RENAME CONSTRAINT work_items_project_id_fkey TO tasks_project_id_fkey;
ALTER TABLE tasks RENAME CONSTRAINT work_items_epic_id_fkey TO tasks_epic_id_fkey;
ALTER TABLE tasks RENAME CONSTRAINT work_items_requested_by_fkey TO tasks_requested_by_fkey;
ALTER TABLE tasks RENAME CONSTRAINT work_items_created_by_run_id_fkey TO tasks_created_by_run_id_fkey;
ALTER TABLE task_repositories RENAME CONSTRAINT work_item_repositories_organization_id_fkey TO task_repositories_organization_id_fkey;
ALTER TABLE task_repositories RENAME CONSTRAINT work_item_repositories_work_item_id_fkey TO task_repositories_task_id_fkey;
ALTER TABLE task_repositories RENAME CONSTRAINT work_item_repositories_repository_id_fkey TO task_repositories_repository_id_fkey;
ALTER TABLE runs RENAME CONSTRAINT runs_work_item_id_fkey TO runs_task_id_fkey;
ALTER TABLE workflow_runs RENAME CONSTRAINT workflow_runs_work_item_id_fkey TO workflow_runs_task_id_fkey;
ALTER TABLE questions RENAME CONSTRAINT questions_work_item_id_fkey TO questions_task_id_fkey;
ALTER TABLE cost_samples RENAME CONSTRAINT cost_samples_work_item_id_fkey TO cost_samples_task_id_fkey;
ALTER TABLE directives RENAME CONSTRAINT directives_work_item_id_fkey TO directives_task_id_fkey;
ALTER TABLE pull_requests RENAME CONSTRAINT pull_requests_work_item_id_fkey TO pull_requests_task_id_fkey;
ALTER TABLE review_findings RENAME CONSTRAINT review_findings_work_item_id_fkey TO review_findings_task_id_fkey;
ALTER TABLE repository_requests RENAME CONSTRAINT repository_requests_work_item_id_fkey TO repository_requests_task_id_fkey;

ALTER POLICY work_item_repositories_isolation ON task_repositories RENAME TO task_repositories_isolation;
ALTER TRIGGER work_items_updated_at ON tasks RENAME TO tasks_updated_at;

-- The live notification names the task.
CREATE OR REPLACE FUNCTION notify_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_notify('dude_events', json_build_object(
    'cursor', NEW.cursor,
    'organizationId', NEW.organization_id,
    'taskId', NEW.task_id,
    'runId', NEW.run_id,
    'sessionId', NEW.session_id)::text);
  RETURN NULL;
END $$;

-- A function's result columns cannot be renamed in place.
DROP FUNCTION lookup_run_by_mcp_token(text);
CREATE FUNCTION lookup_run_by_mcp_token(p_hash text)
RETURNS TABLE (run_id text, organization_id text, project_id text, task_id text, role text, status text)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = public, pg_temp AS $$
  SELECT id, organization_id, project_id, task_id, role::text, status::text
  FROM runs WHERE mcp_token_hash = p_hash
$$;
REVOKE ALL ON FUNCTION lookup_run_by_mcp_token(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION lookup_run_by_mcp_token(text) TO dude_app;

-- The ledger's event types and payload keys, and the workflows' state.
UPDATE events SET event_type = 'task.' || substr(event_type, length('work_item.') + 1)
  WHERE event_type LIKE 'work_item.%';
UPDATE events SET payload = (payload - 'createdByWorkItemId') || jsonb_build_object('createdByTaskId', payload->'createdByWorkItemId')
  WHERE payload ? 'createdByWorkItemId';
UPDATE events SET payload = (payload - 'workItemStatus') || jsonb_build_object('taskStatus', payload->'workItemStatus')
  WHERE payload ? 'workItemStatus';
UPDATE workflow_runs SET workflow_type = 'task.delivery' WHERE workflow_type = 'work_item.delivery';
UPDATE workflow_runs SET state = (state - 'workItemId') || jsonb_build_object('taskId', state->'workItemId')
  WHERE state ? 'workItemId';

-- The notifier's index follows the event it now reads.
DROP INDEX IF EXISTS events_asks_idx;
CREATE INDEX events_asks_idx ON events (cursor)
  WHERE event_type IN ('question.asked', 'repository.requested', 'task.ready_to_merge');

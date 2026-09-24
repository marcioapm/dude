-- Organising work: epics in an order, work items pointed at a repository.

-- Epics are ordered within their project, by the person organising the
-- work: the order is a statement of priority, so it is stored, not derived.
ALTER TABLE epics ADD COLUMN position integer NOT NULL DEFAULT 0;

UPDATE epics e SET position = o.n
FROM (SELECT id, row_number() OVER (PARTITION BY project_id ORDER BY created_at) - 1 AS n FROM epics) o
WHERE e.id = o.id;

-- The repository a work item changes, chosen when it is created or edited.
-- Null means the project's only one, or a work item that changes no code.
-- One repository per work item: work across several is split into
-- sibling work items in an epic.
ALTER TABLE work_items ADD COLUMN repository_id text REFERENCES repositories(id) ON DELETE SET NULL;

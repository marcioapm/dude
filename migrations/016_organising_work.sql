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

-- Work items get a short key people can say and type: the project's prefix
-- and a number counting up within it (TK-12). The ulid stays the id; the
-- key is for people.
ALTER TABLE projects ADD COLUMN key_prefix text,
  ADD COLUMN next_work_item_number integer NOT NULL DEFAULT 1;
ALTER TABLE work_items ADD COLUMN number integer;

-- The prefix from the slug: its first letters, up to four, upper-cased.
UPDATE projects SET key_prefix = upper(left(regexp_replace(slug, '[^a-zA-Z]', '', 'g'), 4));
UPDATE projects SET key_prefix = 'WI' WHERE key_prefix IS NULL OR key_prefix = '';
ALTER TABLE projects ALTER COLUMN key_prefix SET NOT NULL;

-- Existing work items numbered in the order they were created.
UPDATE work_items w SET number = n.n
FROM (SELECT id, row_number() OVER (PARTITION BY project_id ORDER BY created_at) AS n FROM work_items) n
WHERE w.id = n.id;
UPDATE projects p SET next_work_item_number = COALESCE(
  (SELECT max(number) + 1 FROM work_items WHERE project_id = p.id), 1);
ALTER TABLE work_items ALTER COLUMN number SET NOT NULL;
ALTER TABLE work_items ADD CONSTRAINT work_items_number_unique UNIQUE (project_id, number);

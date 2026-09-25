-- The repositories the lux Run holds that it may push (its spec says so):
-- a publishing phase pushes only if it holds one. Kept beside
-- lux_repositories and written with it — at submit, and as lux clones
-- one a resume added — so the decision is what lux was told, not what the
-- work item says now.
ALTER TABLE runs ADD COLUMN lux_pushes text[] NOT NULL DEFAULT '{}';
UPDATE runs r SET lux_pushes = ARRAY(
  SELECT repo.name FROM work_item_repositories wr JOIN repositories repo ON repo.id = wr.repository_id
  WHERE wr.work_item_id = r.work_item_id AND wr.access = 'write' AND repo.name = ANY (r.lux_repositories))
WHERE cardinality(r.lux_repositories) > 0;

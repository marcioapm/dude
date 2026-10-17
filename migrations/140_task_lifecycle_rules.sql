-- 140_task_lifecycle_rules.sql — the start rules that read lifecycle
-- (docs/design/tracking-work.md, "Duplicate" and "Start paths: one rule").
-- Separate from 139 because they name the `duplicates` relation kind 139
-- adds, which can't be used in the transaction that adds it.
--
-- A task closed as a duplicate hands its blocking to the task it
-- duplicates: a `blocks` edge from it counts as one from its canonical task,
-- one hop (the canonical task is never itself a duplicate: Close refuses
-- that, and one task has at most one `duplicates` edge out).

CREATE UNIQUE INDEX task_relations_one_duplicate ON task_relations (from_task_id) WHERE kind = 'duplicates';

-- A task's open blockers: every `blocks` edge into it, its blocker read
-- through a `duplicates` edge when the blocker was closed as a duplicate,
-- whose (effective) blocker is not merged — done, with a merged pull
-- request. Two duplicates of one canonical task name it once. The rest is
-- 111's rule unchanged.
CREATE OR REPLACE FUNCTION task_blocked_by(task text) RETURNS TABLE (key text, status task_status, id text)
  LANGUAGE sql STABLE
  AS $$
    SELECT DISTINCT ON (b.project_id, b.number, b.id) p.key_prefix || '-' || b.number, b.status, b.id
      FROM task_relations r
      LEFT JOIN task_relations d ON d.from_task_id = r.from_task_id AND d.kind = 'duplicates'
      -- CASE, not COALESCE: under RLS the planner probes tasks_pkey only
      -- with the CASE form; COALESCE scans the organisation's tasks.
      JOIN tasks b ON b.id = CASE WHEN d.to_task_id IS NULL THEN r.from_task_id ELSE d.to_task_id END
      JOIN projects p ON p.id = b.project_id
     WHERE r.to_task_id = task AND r.kind = 'blocks'
       AND NOT (b.status = 'done'
                AND EXISTS (SELECT 1 FROM pull_requests pr WHERE pr.task_id = b.id AND pr.state = 'merged'))
     ORDER BY b.project_id, b.number, b.id
  $$;

-- 111's loop check, walking what holds a start: every `blocks` edge as
-- stored, and every edge a duplicate handed over (canonical C → each task
-- its duplicate blocks). So "CP-62 blocks CP-47" is a loop when CP-47
-- already blocks CP-62 through a duplicate, and closing a task as a
-- duplicate is checked as the handed-over edges it creates.
CREATE OR REPLACE FUNCTION task_relation_would_cycle(src text, dst text) RETURNS text[]
  LANGUAGE plpgsql STABLE
  AS $$
  DECLARE
    parents  jsonb := jsonb_build_object(dst, NULL);
    frontier text[] := ARRAY[dst];
    layer    jsonb;
    walked   text[] := ARRAY[src];
    cur      text := src;
  BEGIN
    -- Each step probes its edges from the worktable (the primary key for
    -- blocks out of a task, task_relations_to_idx for the duplicates hop
    -- into it): a join to a union of all edges would scan them every step.
    IF NOT EXISTS (
      WITH RECURSIVE reach (task) AS (
        SELECT dst
        UNION
        (WITH cur AS (SELECT task FROM reach)
         SELECT r.to_task_id FROM cur JOIN task_relations r ON r.from_task_id = cur.task AND r.kind = 'blocks'
         UNION ALL
         SELECT r.to_task_id FROM cur JOIN task_relations d ON d.to_task_id = cur.task AND d.kind = 'duplicates'
           JOIN task_relations r ON r.from_task_id = d.from_task_id AND r.kind = 'blocks')
      )
      SELECT 1 FROM reach WHERE task = src
    ) THEN
      RETURN NULL;
    END IF;
    WHILE NOT parents ? src LOOP
      SELECT jsonb_object_agg(e.to_id, e.from_id) INTO layer
        FROM (SELECT DISTINCT ON (x.to_id) x.to_id, x.from_id
                FROM (SELECT r.from_task_id AS from_id, r.to_task_id AS to_id FROM task_relations r WHERE r.kind = 'blocks'
                      UNION ALL
                      SELECT d.to_task_id, r.to_task_id FROM task_relations d
                        JOIN task_relations r ON r.from_task_id = d.from_task_id AND r.kind = 'blocks'
                       WHERE d.kind = 'duplicates') x
               WHERE x.from_id = ANY (frontier) AND NOT parents ? x.to_id
               ORDER BY x.to_id, x.from_id) e;
      IF layer IS NULL THEN
        RETURN NULL;
      END IF;
      parents := parents || layer;
      frontier := ARRAY(SELECT jsonb_object_keys(layer));
    END LOOP;
    WHILE cur <> dst LOOP
      cur := parents ->> cur;
      walked := array_prepend(cur, walked);
    END LOOP;
    RETURN (
      SELECT array_agg(p.key_prefix || '-' || t.number ORDER BY s.n)
        FROM unnest(array_append(walked, dst)) WITH ORDINALITY AS s(task, n)
        JOIN tasks t ON t.id = s.task
        JOIN projects p ON p.id = t.project_id
    );
  END
  $$;

-- Why the task can't start now, the first that holds: 'archived' (put
-- away), 'closed' (a person ended it: a resolution other than delivery's
-- nothing_to_merge), 'blocked' (task_blocked_by is not empty); NULL when it
-- can. Both processes read it — the orchestrator's RefuseStartTx and the
-- backend's bare Run — so the order is written once. The first two are
-- never overridden; only 'blocked' is, by a person.
CREATE FUNCTION task_start_refusal(task text) RETURNS text
  LANGUAGE sql STABLE
  AS $$
    SELECT CASE
             WHEN t.archived_at IS NOT NULL THEN 'archived'
             WHEN t.resolution IS NOT NULL AND t.resolution <> 'nothing_to_merge' THEN 'closed'
             WHEN EXISTS (SELECT 1 FROM task_blocked_by(t.id)) THEN 'blocked'
           END
      FROM tasks t WHERE t.id = task
  $$;

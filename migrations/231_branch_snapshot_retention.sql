-- 231_branch_snapshot_retention.sql — a bound on task_branch_snapshots.
--
-- Every new head of a task branch stores the whole branch diff again (230),
-- and nothing removed a row but a task's deletion. The orchestrator's
-- branch-snapshots-prune loop (branchdiff.Prune, as dude_sweeper, which may
-- not delete from it: dude_app may not either) calls
-- prune_branch_snapshots, which deletes the ones nothing can still read:
--
--   - the latest of each (task, attempt, repository) stays: every attempt,
--     the older ones too, shows it;
--   - one a capture request names stays (the branch is at it: the read's
--     and the capturer's "latest", even after a reset);
--   - one a feedback item names stays (branch_snapshot_id): its anchor
--     resolves;
--   - one that was the latest in the last 24 hours stays: a Changes tab
--     pinned to it (?snapshot=) keeps reading while the person has it open.
--     The 24 hours count from when it stopped being the latest, read as
--     when its successor was captured: it goes only once a newer one is
--     itself over a day old (a branch quiet for days, then pushed, keeps
--     what the open tab shows).
--
-- Lineage: newer.count and newer.relation are walked along
-- previous_snapshot_id (control plane, newerThan). A deleted snapshot's
-- children are rewired to its previous in the same transaction, and carry
-- in steps how many captures their link spans, so the walk counts what it
-- did before and a rewrite anywhere in the span still reads as rewritten.
-- No tombstone row: a pruned id is unknown to the API, as any other.

-- Captures this snapshot's link to previous_snapshot_id spans: 1 as
-- captured, more once the ones between were pruned.
ALTER TABLE task_branch_snapshots ADD COLUMN steps integer NOT NULL DEFAULT 1 CHECK (steps >= 1);

-- Oldest first, without reading every row's files.
CREATE INDEX task_branch_snapshots_captured_idx ON task_branch_snapshots (captured_at, id);
-- The keep rule's "a capture request names it", and the foreign key's SET
-- NULL when one goes.
CREATE INDEX task_branch_capture_requests_snapshot_idx ON task_branch_capture_requests (snapshot_id)
  WHERE snapshot_id IS NOT NULL;

-- Up to p_limit snapshots no rule keeps, oldest first; returns how many
-- went. Never waits: a row another caller holds (FOR UPDATE SKIP LOCKED),
-- or one whose children or whose previous (the children's new one) someone
-- holds, is skipped for the next pass. Every row a rewire writes is held
-- first, so two callers never rewire one lineage at once. A capture
-- inserting a child of a row holds it (the foreign key's KEY SHARE), so a
-- lineage never gains a child while it is rewired.
-- Nor does it make writers wait on the rows it keeps: the children it
-- rewires are held FOR NO KEY UPDATE, which a foreign key's KEY SHARE does
-- not conflict with, so a capture of their next child or a feedback item
-- on them (usually the lineage's latest) goes ahead while the batch is
-- open. Only the deleted rows themselves (FOR UPDATE) block a writer, and
-- no rule keeps them.
CREATE FUNCTION prune_branch_snapshots(p_limit integer) RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp AS $$
DECLARE
  -- A Changes tab pinned to a snapshot reads it by id while it is open:
  -- one captured, or superseded, in the last 24 hours is kept, whatever
  -- else holds.
  keep_recent constant interval := interval '24 hours';
  candidates text[];
  c text;
  m record;
  children integer;
  held integer;
  pruned integer := 0;
BEGIN
  IF p_limit IS NULL OR p_limit < 1 THEN
    RETURN 0;
  END IF;
  -- All locked before any is rewired: a row this transaction has changed
  -- would be skipped by a lock taken after.
  SELECT array_agg(x.id ORDER BY x.captured_at, x.id) INTO candidates FROM (
    SELECT s.id, s.captured_at FROM task_branch_snapshots s
    WHERE s.captured_at < now() - keep_recent
      AND EXISTS (SELECT 1 FROM task_branch_snapshots n WHERE n.task_id = s.task_id AND n.attempt = s.attempt
                  AND n.repository_id = s.repository_id AND (n.captured_at, n.id) > (s.captured_at, s.id)
                  AND n.captured_at < now() - keep_recent)
      AND NOT EXISTS (SELECT 1 FROM task_branch_capture_requests q WHERE q.snapshot_id = s.id)
      AND NOT EXISTS (SELECT 1 FROM feedback_items f WHERE f.branch_snapshot_id = s.id)
    ORDER BY s.captured_at, s.id
    LIMIT p_limit
    FOR UPDATE OF s SKIP LOCKED) x;
  FOREACH c IN ARRAY COALESCE(candidates, '{}') LOOP
    -- Read again: an older one pruned in this call rewired it.
    SELECT s.id, s.previous_snapshot_id, s.relation, s.steps INTO m
      FROM task_branch_snapshots s WHERE s.id = c;
    -- Named since the candidates were read (committed before the lock
    -- above was had): kept. Once held, nothing new can name it: a writer's
    -- foreign key waits on the lock.
    IF EXISTS (SELECT 1 FROM task_branch_capture_requests q WHERE q.snapshot_id = m.id)
       OR EXISTS (SELECT 1 FROM feedback_items f WHERE f.branch_snapshot_id = m.id) THEN
      CONTINUE;
    END IF;
    SELECT count(*) INTO children FROM task_branch_snapshots WHERE previous_snapshot_id = m.id;
    -- NO KEY UPDATE: the rewire never changes a child's id, so a capture
    -- of the child's own child, or a feedback item on it (their foreign
    -- keys' KEY SHARE), goes ahead while this batch is open; two pruners
    -- still exclude each other.
    SELECT count(*) INTO held FROM (SELECT 1 FROM task_branch_snapshots WHERE previous_snapshot_id = m.id
      FOR NO KEY UPDATE SKIP LOCKED) x;
    IF held < children THEN
      CONTINUE;
    END IF;
    -- The children's new previous, held as their foreign key will: one
    -- another caller holds is skipped, never waited on.
    IF m.previous_snapshot_id IS NOT NULL AND children > 0 AND NOT EXISTS (
        SELECT 1 FROM task_branch_snapshots WHERE id = m.previous_snapshot_id FOR KEY SHARE SKIP LOCKED) THEN
      CONTINUE;
    END IF;
    -- Each child now follows m's previous, over both links: rewritten if
    -- either was, else by the merge bases at its ends (spanRelation).
    UPDATE task_branch_snapshots ch SET
      previous_snapshot_id = m.previous_snapshot_id,
      steps = ch.steps + m.steps,
      relation = CASE
        WHEN m.previous_snapshot_id IS NULL THEN ch.relation
        WHEN ch.relation = 'rewritten' OR m.relation IN ('rewritten', 'initial') THEN 'rewritten'
        WHEN (SELECT p.merge_base_sha FROM task_branch_snapshots p WHERE p.id = m.previous_snapshot_id) = ch.merge_base_sha
          THEN 'fast-forward'
        ELSE 'merged-target' END
    WHERE ch.previous_snapshot_id = m.id;
    DELETE FROM task_branch_snapshots WHERE id = m.id;
    pruned := pruned + 1;
  END LOOP;
  RETURN pruned;
END
$$;
REVOKE ALL ON FUNCTION prune_branch_snapshots(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION prune_branch_snapshots(integer) TO dude_sweeper;

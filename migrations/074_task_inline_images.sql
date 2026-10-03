-- 074_task_inline_images.sql — a task's images sit in its text.
--
-- A task's goal and acceptance criteria are Markdown, and an image belongs
-- at a place in them: `![name](attachment:att_…)`. The backend parses the
-- references on every create and update and, in the same transaction, sets
-- `for_prompt` and `attached_at` on the task's attachments now referenced
-- and clears both on those no longer referenced. So `for_prompt` keeps its
-- column and its CHECKs, and now means "referenced by the task's text";
-- `position` is the order of first appearance (goal, then criteria).
--
-- An image let go that way is an unsent upload again, and the sweeper takes
-- it a day after it was let go, not a day after it was uploaded:
-- `detached_at` is when, and the sweep's index follows it.

ALTER TABLE attachments ADD COLUMN detached_at timestamptz;

DROP INDEX attachments_unattached_idx;
CREATE INDEX attachments_unattached_idx ON attachments ((COALESCE(detached_at, created_at))) WHERE attached_at IS NULL;

-- Images a task was given through 071's tray are in no text yet. Each is
-- appended to its task's goal as a reference, in the tray's order, so the
-- rule above holds for them too and nothing a running delivery was given
-- goes missing: an agent reads them as it did, at the end of the goal.
-- '[' and ']' in a name would end its alt early (names have no newlines).
UPDATE tasks t SET goal = rtrim(t.goal) || E'\n\n' || refs.md
FROM (
  SELECT task_id, string_agg('![' || translate(name, '[]', '()') || '](attachment:' || id || ')', E'\n\n' ORDER BY position, id) AS md
  FROM attachments WHERE for_prompt GROUP BY task_id
) refs
WHERE refs.task_id = t.id;

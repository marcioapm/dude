-- 230_task_branch_snapshots.sql — a task attempt's whole branch, pinned.
--
-- A snapshot is one repository's committed task branch of one attempt
-- against its target, as `git diff <target>...<head>` shows it: the merge
-- base is computed at capture (merge-base(target, head)), so merging the
-- target into the branch moves the base forward and the diff stays the
-- branch's own changes. Read from the repository's object graph (GitHub's
-- compare), never by putting Run diffs together.
--
-- Each snapshot names the one it supersedes for its attempt and repository
-- and how its head relates to that one's: fast-forward (head descends,
-- base unchanged), merged-target (head descends, base moved: the target
-- was merged in), rewritten (head does not descend: a rebase or a force
-- push); initial for the first. A move of the target alone, the branch
-- head unchanged, makes none (the diff would not change).
--
-- What a file or the whole could not show is said, never dropped: a file
-- whose patch GitHub left out (too large) is truncated, a binary file is
-- binary, and a compare at GitHub's 300-file cap is partial.

CREATE TABLE task_branch_snapshots (
  id                    text PRIMARY KEY,
  organization_id       text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  task_id               text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  attempt               integer NOT NULL CHECK (attempt > 0),
  repository_id         text NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  -- The repository's name, as runs.heads and code anchors name it.
  repository            text NOT NULL,
  branch_ref            text NOT NULL CHECK (length(branch_ref) BETWEEN 1 AND 255),
  target_ref            text NOT NULL CHECK (length(target_ref) BETWEEN 1 AND 255),
  target_sha            text NOT NULL CHECK (target_sha ~ '^[0-9a-f]{40,64}$'),
  merge_base_sha        text NOT NULL CHECK (merge_base_sha ~ '^[0-9a-f]{40,64}$'),
  head_sha              text NOT NULL CHECK (head_sha ~ '^[0-9a-f]{40,64}$'),
  -- RunDiffFile[] (packages/domain live.ts), each with its own truncated
  -- and binary flags.
  files                 jsonb NOT NULL CHECK (jsonb_typeof(files) = 'array'),
  checksum              text NOT NULL CHECK (length(checksum) BETWEEN 1 AND 200),
  -- {files, additions, deletions, truncatedFiles, binaryFiles, fileCapReached}.
  stats                 jsonb NOT NULL CHECK (jsonb_typeof(stats) = 'object'),
  -- Some file's lines were left out, or the file list was cut at the cap.
  truncated             boolean NOT NULL DEFAULT false,
  binary_files          integer NOT NULL DEFAULT 0 CHECK (binary_files >= 0),
  -- complete, or partial with why (GitHub's file cap).
  status                text NOT NULL DEFAULT 'complete' CHECK (status IN ('complete', 'partial')),
  reason                text CHECK (length(reason) <= 500),
  source                text NOT NULL DEFAULT 'github-compare' CHECK (source IN ('github-compare')),
  -- The published pull request's head when it was captured, if one was open.
  pr_head_sha           text CHECK (pr_head_sha ~ '^[0-9a-f]{40,64}$'),
  previous_snapshot_id  text REFERENCES task_branch_snapshots(id) ON DELETE SET NULL,
  relation              text NOT NULL CHECK (relation IN ('initial', 'fast-forward', 'merged-target', 'rewritten')),
  captured_at           timestamptz NOT NULL DEFAULT clock_timestamp(),
  CONSTRAINT task_branch_snapshots_task_id_attempt_repository_id_head_sh_key
    UNIQUE (task_id, attempt, repository_id, head_sha, merge_base_sha, target_ref),
  -- A first has none before it (a later one's may be gone: SET NULL).
  CHECK (relation <> 'initial' OR previous_snapshot_id IS NULL),
  CHECK ((status = 'partial') = (reason IS NOT NULL))
);
ALTER TABLE task_branch_snapshots ALTER COLUMN files SET COMPRESSION lz4;
-- The latest of an attempt's repository, and the list in order.
CREATE INDEX task_branch_snapshots_latest_idx ON task_branch_snapshots (task_id, attempt, repository_id, captured_at DESC, id DESC);
CREATE INDEX task_branch_snapshots_previous_idx ON task_branch_snapshots (previous_snapshot_id) WHERE previous_snapshot_id IS NOT NULL;

ALTER TABLE task_branch_snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE task_branch_snapshots FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON task_branch_snapshots
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
-- Recorded once, never changed: no UPDATE, no DELETE but the cascades
-- (002's default privileges grant both; taken back, as for events).
GRANT SELECT, INSERT ON task_branch_snapshots TO dude_app;
REVOKE UPDATE, DELETE ON task_branch_snapshots FROM dude_app;
GRANT SELECT ON task_branch_snapshots TO dude_sweeper;

-- What wants capturing: one row per attempt and repository, set when its
-- branch head may have moved (the triggers below, or a read that found
-- nothing yet). The orchestrator's worker (branchdiff.Capturer) takes the
-- due ones, reads the branch head and captures a snapshot when it is new.
-- 'unavailable' keeps why GitHub could not be read (the API says so);
-- 'absent': the branch does not exist yet (nothing to show).
CREATE TABLE task_branch_capture_requests (
  organization_id  text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  task_id          text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  attempt          integer NOT NULL CHECK (attempt > 0),
  repository_id    text NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  -- The head the trigger saw, when it knew one: a hint, the branch is read.
  head_sha         text,
  status           text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'done', 'unavailable', 'absent')),
  reason           text CHECK (length(reason) <= 500),
  failures         integer NOT NULL DEFAULT 0 CHECK (failures >= 0),
  next_attempt_at  timestamptz NOT NULL DEFAULT now(),
  requested_at     timestamptz NOT NULL DEFAULT now(),
  settled_at       timestamptz,
  -- The snapshot the branch was last read at: the newest captured, or an
  -- earlier one when the branch went back to its head (a reset).
  snapshot_id      text REFERENCES task_branch_snapshots(id) ON DELETE SET NULL,
  PRIMARY KEY (task_id, attempt, repository_id)
);
CREATE INDEX task_branch_capture_due_idx ON task_branch_capture_requests (next_attempt_at) WHERE status = 'pending';

ALTER TABLE task_branch_capture_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE task_branch_capture_requests FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON task_branch_capture_requests
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE ON task_branch_capture_requests TO dude_app;
-- The worker claims due rows across organizations (pushing next_attempt_at
-- ahead, committed before it reads anything), then works in each.
GRANT SELECT ON task_branch_capture_requests TO dude_sweeper;
GRANT UPDATE (next_attempt_at) ON task_branch_capture_requests TO dude_sweeper;

-- Asks for a capture of (task, attempt, repository): pending again, due
-- now, whatever it was. The worker reads the branch; a head it already
-- has a snapshot of makes none.
--
-- Called from triggers inside other writes (a publish, a PR sync): an ask
-- it cannot make — no attempt yet, a task or repository not of p_org — is
-- skipped, never raised, so it can never fail the write that moved the
-- branch.
CREATE FUNCTION want_branch_capture(p_org text, p_task text, p_attempt integer, p_repo text, p_head text)
  RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
BEGIN
  IF NULLIF(current_setting('app.organization_id', true), '') IS NOT NULL
     AND p_org IS DISTINCT FROM current_setting('app.organization_id', true) THEN
    RETURN;
  END IF;
  IF p_attempt IS NULL OR p_attempt < 1
     OR NOT EXISTS (SELECT 1 FROM tasks WHERE id = p_task AND organization_id = p_org)
     OR NOT EXISTS (SELECT 1 FROM repositories WHERE id = p_repo AND organization_id = p_org) THEN
    RETURN;
  END IF;
  INSERT INTO task_branch_capture_requests (organization_id, task_id, attempt, repository_id, head_sha)
  VALUES (p_org, p_task, p_attempt, p_repo, NULLIF(p_head, ''))
  ON CONFLICT (task_id, attempt, repository_id) DO UPDATE
    SET status = 'pending', head_sha = EXCLUDED.head_sha, reason = NULL,
        next_attempt_at = CASE WHEN task_branch_capture_requests.failures > 0
          THEN GREATEST(now(), task_branch_capture_requests.next_attempt_at) ELSE now() END,
        requested_at = clock_timestamp(), settled_at = NULL;
END
$$;
REVOKE ALL ON FUNCTION want_branch_capture(text, text, integer, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION want_branch_capture(text, text, integer, text, text) TO dude_app;

-- A task's agent Run published (runs.heads, by repository name): each of
-- its repositories' branch may have moved — a phase's publish, a merge of
-- the target an agent made after sync_base, a promotion's head.
CREATE FUNCTION runs_want_branch_capture() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE
  h record;
BEGIN
  -- One at a time in byte order of the repository's name, the order a
  -- publish's pull request updates take (Go's sort, phases/publish.go):
  -- two writers asking for the same rows never wait on each other in a
  -- cycle.
  FOR h IN SELECT r.id, x.value->>'sha' AS sha
    FROM jsonb_each(NEW.heads) x
    JOIN repositories r ON r.name = x.key
    JOIN task_repositories tr ON tr.repository_id = r.id AND tr.task_id = NEW.task_id
    WHERE jsonb_typeof(x.value) = 'object'
    ORDER BY r.name COLLATE "C"
  LOOP
    PERFORM want_branch_capture(NEW.organization_id, NEW.task_id, NEW.attempt, h.id, h.sha);
  END LOOP;
  RETURN NULL;
END
$$;
CREATE TRIGGER runs_branch_capture
  AFTER UPDATE OF heads ON runs
  FOR EACH ROW WHEN (NEW.kind = 'agent' AND NEW.task_id IS NOT NULL
    AND NEW.heads IS DISTINCT FROM OLD.heads AND NEW.heads <> '{}'::jsonb)
  EXECUTE FUNCTION runs_want_branch_capture();

-- A conductor's publish moved the task branch (delivery.PublishedTx). A
-- delivery from before attempts were kept records attempt 0: the task's.
CREATE FUNCTION publishes_want_branch_capture() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE
  h record;
BEGIN
  -- In byte order of the name, as runs_want_branch_capture.
  FOR h IN SELECT r.id, x.value->>'sha' AS sha
    FROM jsonb_each(COALESCE(NEW.heads, '{}'::jsonb)) x
    JOIN repositories r ON r.name = x.key
    JOIN task_repositories tr ON tr.repository_id = r.id AND tr.task_id = NEW.task_id
    WHERE jsonb_typeof(x.value) = 'object'
    ORDER BY r.name COLLATE "C"
  LOOP
    PERFORM want_branch_capture(NEW.organization_id, NEW.task_id, COALESCE(NULLIF(NEW.attempt, 0), task_attempt(NEW.task_id)),
      h.id, h.sha);
  END LOOP;
  RETURN NULL;
END
$$;
CREATE TRIGGER conductor_publishes_branch_capture
  AFTER UPDATE OF status ON conductor_publishes
  FOR EACH ROW WHEN (NEW.status = 'published' AND OLD.status IS DISTINCT FROM NEW.status)
  EXECUTE FUNCTION publishes_want_branch_capture();

-- A pull request of the task opened, or its head moved: dude's push
-- recorded (phases, candidate promotion), or someone else's, seen by the
-- PR sync (webhook or poll, prs.Syncer).
CREATE FUNCTION pull_requests_want_branch_capture() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
BEGIN
  IF NEW.task_id IS NOT NULL AND NEW.state IN ('draft', 'open') THEN
    PERFORM want_branch_capture(NEW.organization_id, NEW.task_id, task_attempt(NEW.task_id), NEW.repository_id, NEW.head_sha);
  END IF;
  RETURN NULL;
END
$$;
CREATE TRIGGER pull_requests_branch_capture_opened
  AFTER INSERT ON pull_requests
  FOR EACH ROW EXECUTE FUNCTION pull_requests_want_branch_capture();
CREATE TRIGGER pull_requests_branch_capture_moved
  AFTER UPDATE OF head_sha ON pull_requests
  FOR EACH ROW WHEN (NEW.head_sha IS DISTINCT FROM OLD.head_sha)
  EXECUTE FUNCTION pull_requests_want_branch_capture();

-- A code feedback item made on a branch snapshot (anchor scope 'branch')
-- names it, as a Run's names its Run (run_id); anchor_kind stays 'code'.
ALTER TABLE feedback_items ADD COLUMN branch_snapshot_id text REFERENCES task_branch_snapshots(id) ON DELETE SET NULL,
  ADD CONSTRAINT feedback_items_one_diff CHECK (run_id IS NULL OR branch_snapshot_id IS NULL);
CREATE INDEX feedback_items_branch_snapshot_idx ON feedback_items (branch_snapshot_id) WHERE branch_snapshot_id IS NOT NULL;

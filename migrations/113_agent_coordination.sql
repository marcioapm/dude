-- 113_agent_coordination.sql — agents on an epic see, ask and brief each
-- other (docs/design/agent-coordination.md).
--
-- task_peers   the peer rule, in one place: every tool, briefing and API
--              that asks "is this task a peer of that one?" reads it.
-- peer_messages, peer_waits   a message from one task's Run to another
--              task, and a Run waiting on the reply to one of its own.
-- epic_notes, epic_note_reads   numbered, never-edited notes on an epic,
--              and which Run was told of which change.
-- conductor_digests   how far a task's conductor has been told of its
--              epic's traffic ("Since you last looked").
--
-- New tables, functions and one trigger on runs: nothing existing is
-- rewritten or scanned. The trigger's CREATE takes runs' SHARE ROW
-- EXCLUSIVE lock for the catalog change only.

-- A task's peers: the other tasks on its epic. A task with no epic has
-- none. `via` says how each is a peer, for when "depends on" links join
-- the epic here (a UNION in this one function).
CREATE FUNCTION task_peers(task text) RETURNS TABLE (peer_id text, via text)
  LANGUAGE sql STABLE PARALLEL SAFE AS $$
  SELECT o.id, 'epic'::text
  FROM tasks t JOIN tasks o ON o.epic_id = t.epic_id AND o.id <> t.id
  WHERE t.id = task AND t.epic_id IS NOT NULL
$$;

-- One message from a task's Run to another task. Addressed to the task, so
-- it survives phase changes: delivered as a directive on whichever of the
-- recipient's Runs speaks for it (directive_id, delivered_run_id, read_at
-- from the directive's read receipt), or held until its next Run's briefing
-- (briefed_run_id).
CREATE TABLE peer_messages (
  id               text PRIMARY KEY,
  organization_id  text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  from_task_id     text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  from_run_id      text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  to_task_id       text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  reply_to         text REFERENCES peer_messages(id) ON DELETE SET NULL,
  text             text NOT NULL CHECK (length(btrim(text)) > 0),
  created_at       timestamptz NOT NULL DEFAULT now(),
  directive_id     text REFERENCES directives(id) ON DELETE SET NULL,
  delivered_run_id text REFERENCES runs(id) ON DELETE SET NULL,
  read_at          timestamptz,
  briefed_run_id   text REFERENCES runs(id) ON DELETE SET NULL,
  CHECK (from_task_id <> to_task_id)
);
-- What waits for a task: its next briefing reads these.
CREATE INDEX peer_messages_to_idx ON peer_messages (to_task_id, created_at);
CREATE INDEX peer_messages_pair_idx ON peer_messages (from_task_id, to_task_id, created_at);
-- The rate limit: what a Run sent in the last hour.
CREATE INDEX peer_messages_from_run_idx ON peer_messages (from_run_id, created_at);
-- A directive's read receipt finds its message.
CREATE INDEX peer_messages_directive_idx ON peer_messages (directive_id) WHERE directive_id IS NOT NULL;
-- "Has this been answered?": the replies to a message.
CREATE INDEX peer_messages_reply_idx ON peer_messages (reply_to) WHERE reply_to IS NOT NULL;

-- A Run's wait on the reply to one of its messages: `wait` from
-- wait_for_reply, `turn_ended` from a turn that ended with its question
-- unanswered. At most one open per Run. Closed by the reply (or the
-- sender's own reply closing the question), or by the Run ending; kept
-- after, for the thread's history.
CREATE TABLE peer_waits (
  id               text PRIMARY KEY,
  organization_id  text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  run_id           text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  message_id       text NOT NULL REFERENCES peer_messages(id) ON DELETE CASCADE,
  how              text NOT NULL CHECK (how IN ('wait', 'turn_ended')),
  since            timestamptz NOT NULL DEFAULT now(),
  -- When the project's grace parks it, as it stood when the wait opened.
  parks_at         timestamptz,
  closed_at        timestamptz,
  closed_by        text CHECK (closed_by IN ('reply', 'self', 'run_ended')),
  CHECK ((closed_at IS NULL) = (closed_by IS NULL))
);
CREATE UNIQUE INDEX peer_waits_open_idx ON peer_waits (run_id) WHERE closed_at IS NULL;
CREATE INDEX peer_waits_message_idx ON peer_waits (message_id);
CREATE INDEX peer_waits_run_closed_idx ON peer_waits (run_id, closed_at);

-- A note on an epic: a short working fact every agent on it must follow.
-- Never edited: a replacement is a new note (replaces_id), the old one
-- marked replaced_at; a person may retire one. Current: neither.
CREATE TABLE epic_notes (
  id               text PRIMARY KEY,
  organization_id  text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  epic_id          text NOT NULL REFERENCES epics(id) ON DELETE CASCADE,
  n                integer NOT NULL CHECK (n > 0),
  text             text NOT NULL CHECK (length(btrim(text)) BETWEEN 1 AND 2000),
  by_run_id        text REFERENCES runs(id) ON DELETE SET NULL,
  by_person        text REFERENCES people(id) ON DELETE SET NULL,
  -- A person filed it from a brainstorm session's proposal.
  from_session_id  text REFERENCES sessions(id) ON DELETE SET NULL,
  replaces_id      text REFERENCES epic_notes(id) ON DELETE SET NULL,
  replaced_at      timestamptz,
  retired_by       text REFERENCES people(id) ON DELETE SET NULL,
  retired_at       timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now(),
  -- Who pinned it, as written: an agent's "ABS-2's implementer", or the
  -- person's name; kept when the Run or person is gone.
  by_label         text NOT NULL,
  UNIQUE (epic_id, n),
  CHECK (from_session_id IS NULL OR by_run_id IS NULL),
  CHECK (replaced_at IS NULL OR retired_at IS NULL)
);
-- A note is replaced at most once: no fork in its history.
CREATE UNIQUE INDEX epic_notes_replaces_idx ON epic_notes (replaces_id) WHERE replaces_id IS NOT NULL;
-- The 10-note limit counts these, under a lock on the epic's row.
CREATE INDEX epic_notes_current_idx ON epic_notes (epic_id) WHERE replaced_at IS NULL AND retired_at IS NULL;

-- Which Run was told of which change: at a step (a directive, read_at from
-- its receipt), or in its briefing (change 'briefing', read when written).
CREATE TABLE epic_note_reads (
  note_id          text NOT NULL REFERENCES epic_notes(id) ON DELETE CASCADE,
  run_id           text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  change           text NOT NULL CHECK (change IN ('added', 'replaced', 'retired', 'briefing')),
  organization_id  text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  directive_id     text REFERENCES directives(id) ON DELETE SET NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  read_at          timestamptz,
  PRIMARY KEY (note_id, run_id, change)
);
CREATE INDEX epic_note_reads_directive_idx ON epic_note_reads (directive_id) WHERE directive_id IS NOT NULL;
CREATE INDEX epic_note_reads_run_idx ON epic_note_reads (run_id);

-- How far each task's conductor has been told of its epic's traffic: the
-- next briefing or wake note says what happened after `through`.
CREATE TABLE conductor_digests (
  task_id          text PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
  organization_id  text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  through          timestamptz NOT NULL
);

-- A wait dies with its Run, as a question does (015): nobody is left to
-- resume. Messages to the Run's task stay for the task's next Run. The
-- event id is newId("event")'s shape, as 106's trigger mints it.
CREATE FUNCTION close_peer_waits() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  ms bigint := (extract(epoch FROM clock_timestamp()) * 1000)::bigint;
  b36 text := '';
BEGIN
  WHILE ms > 0 LOOP
    b36 := substr('0123456789abcdefghijklmnopqrstuvwxyz', (ms % 36)::int + 1, 1) || b36;
    ms := ms / 36;
  END LOOP;
  WITH closed AS (
    UPDATE peer_waits w SET closed_at = now(), closed_by = 'run_ended'
    WHERE w.run_id = NEW.id AND w.closed_at IS NULL
    RETURNING w.message_id
  )
  INSERT INTO events (id, organization_id, event_type, project_id, task_id, run_id, actor_type, actor_id, source,
                      correlation_id, payload)
  SELECT 'evt_' || lpad(b36, 9, '0') || substr(replace(gen_random_uuid()::text, '-', ''), 1, 16),
         NEW.organization_id, 'agent.peer.wait_ended', NEW.project_id, NEW.task_id, NEW.id, 'system', 'dude',
         'control-plane', NEW.task_id,
         jsonb_build_object('message', c.message_id, 'on', m.to_task_id, 'by', 'run_ended')
  FROM closed c JOIN peer_messages m ON m.id = c.message_id;
  RETURN NEW;
END $$;

CREATE TRIGGER runs_close_peer_waits
  AFTER UPDATE OF status ON runs
  FOR EACH ROW WHEN (NEW.status IN ('completed', 'failed', 'aborted') AND OLD.status IS DISTINCT FROM NEW.status)
  EXECUTE FUNCTION close_peer_waits();

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['peer_messages', 'peer_waits', 'epic_notes', 'epic_note_reads', 'conductor_digests']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format(
      'CREATE POLICY tenant_isolation ON %I USING (organization_id = current_organization_id())'
      ' WITH CHECK (organization_id = current_organization_id())', t);
  END LOOP;
END $$;

GRANT SELECT, INSERT, UPDATE ON peer_messages, peer_waits, epic_notes, epic_note_reads, conductor_digests TO dude_app;
-- The syncer asks every live Run whether it waits on a peer (OpenAsk); a
-- Run it ends closes its waits (the trigger runs as whoever ends the Run).
GRANT SELECT, UPDATE ON peer_waits TO dude_sweeper;
GRANT SELECT ON peer_messages TO dude_sweeper;

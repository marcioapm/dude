-- 065_wakeable_previews.sql — branch previews that wake on request.
--
-- A preview is lux servers of its own (lux's /v1/servers, lux#41), one per
-- project server marked to start in previews, at a hostname dude chooses.
-- lux tells dude, on its event feed, when someone opens one with nothing
-- serving it (server.wake_requested) and when one has gone unused
-- (server.idle); dude resumes or submits the preview's Run, or stops it.
-- Previews from before this (Run-embedded servers, dude's own idle timer)
-- keep wakeable = false and their old path until they end.

ALTER TABLE runs
  ADD COLUMN wakeable boolean NOT NULL DEFAULT false,
  -- The highest server.wake_requested feed event applied: a replayed or a
  -- second replica's copy of it changes nothing.
  ADD COLUMN wake_event_id bigint NOT NULL DEFAULT 0,
  -- A wake is due: set by the feed, cleared once lux took the resume or
  -- the submit and attaches.
  ADD COLUMN wake_wanted_at timestamptz,
  -- The orchestrator acting on the wake, so another does not as well;
  -- taken over when older than two minutes.
  ADD COLUMN wake_claimed_at timestamptz,
  -- Bumped for each lux Run a wakeable preview submits: the submit's
  -- idempotency key is <run id>/<generation>.
  ADD COLUMN lux_generation integer NOT NULL DEFAULT 0,
  -- The task's branch moved: a running preview syncs to it.
  ADD COLUMN sync_wanted_at timestamptz,
  -- When dude last checked whether every server of a running preview is
  -- idle and found one in use: checked again no sooner than 30s later.
  ADD COLUMN park_checked_at timestamptz;

-- One lux server of a wakeable preview.
CREATE TABLE preview_servers (
  run_id          text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  -- The project server it serves.
  name            text NOT NULL,
  -- lux's id (srv_…): what feed events are matched by. Never derived from
  -- the hostname.
  lux_server_id   text NOT NULL UNIQUE,
  hostname        text NOT NULL,
  url             text,
  -- An unanswered server.idle: its feed event, when dude heard it, and the
  -- lastRequestAt it carried — a later one on the server means someone has
  -- used it since, and it is not idle after all.
  idle_event_id   bigint NOT NULL DEFAULT 0,
  idle_at         timestamptz,
  idle_last_request_at timestamptz,
  -- When someone last asked for it (a wake), for the reaper.
  last_woken_at   timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  -- Deleted in lux (by dude, or lux's own expiry).
  deleted_at      timestamptz,
  PRIMARY KEY (run_id, name)
);

ALTER TABLE preview_servers ENABLE ROW LEVEL SECURITY;
ALTER TABLE preview_servers FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON preview_servers
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON preview_servers TO dude_app;
-- The feed follower and the reaper act across organizations.
GRANT SELECT, UPDATE ON preview_servers TO dude_sweeper;

-- Where the feed follower is: lux's event id, settled (see servers.Feed).
-- One row; every orchestrator follows from it and only moves it forward.
CREATE TABLE lux_feed (
  id             text PRIMARY KEY,
  after_event_id bigint NOT NULL,
  updated_at     timestamptz NOT NULL DEFAULT now()
);
GRANT SELECT, INSERT, UPDATE ON lux_feed TO dude_app, dude_sweeper;

-- The task's branch moved — an agent published (runs.heads), or the forge
-- reported a new head on its pull request — while it has a live wakeable
-- preview: the preview syncs, if it runs.
CREATE FUNCTION preview_sync_wanted() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
BEGIN
  UPDATE runs SET sync_wanted_at = now()
  WHERE task_id = NEW.task_id AND kind = 'preview' AND wakeable
    AND status IN ('pending', 'scheduled', 'starting', 'running', 'paused');
  RETURN NULL;
END
$$;

CREATE TRIGGER runs_preview_sync_wanted
  AFTER UPDATE OF heads ON runs
  FOR EACH ROW WHEN (NEW.kind = 'agent' AND NEW.heads IS DISTINCT FROM OLD.heads AND NEW.heads <> '{}'::jsonb)
  EXECUTE FUNCTION preview_sync_wanted();

CREATE TRIGGER pull_requests_preview_sync_wanted
  AFTER UPDATE OF head_sha ON pull_requests
  FOR EACH ROW WHEN (NEW.head_sha IS DISTINCT FROM OLD.head_sha)
  EXECUTE FUNCTION preview_sync_wanted();

-- The wakeable sweep (servers.wakeableSelect) reads through these, a
-- BitmapOr of the two runs indexes, so its cost follows the live previews
-- and not every preview ever made. A preview leaves the second once
-- nothing of it is left in lux (lux_stop_reason = 'cancel').
CREATE INDEX runs_wakeable_live_idx ON runs (created_at)
  WHERE kind = 'preview' AND wakeable AND status IN ('pending', 'scheduled', 'starting', 'running', 'paused');
CREATE INDEX runs_wakeable_open_idx ON runs (created_at)
  WHERE kind = 'preview' AND wakeable AND lux_stop_reason IS DISTINCT FROM 'cancel';
CREATE INDEX preview_servers_live_idx ON preview_servers (run_id) WHERE deleted_at IS NULL;

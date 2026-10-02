-- 066_run_resumes.sql — how long each resume of a Run took, end to end.
--
-- One row per lux resume dude makes of a Run, keyed by the epoch of the
-- placement it resumed into, inserted when lux accepts the resume. Every
-- timestamp is written once (COALESCE), from what dude already handles:
-- its own resume, lux's state events and the agent's records on the Run's
-- stream, and lux's placements read with GET /v1/runs/{id} when the Run is
-- running again (and once more at the agent's first output, if lux had
-- not reported everything yet). NULL is unknown, never zero.
--
-- Timestamps from lux (the placements) are lux's clock; the others are
-- dude's. A phase that spans the two carries their skew.

CREATE TABLE run_resumes (
  run_id               text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  organization_id      text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  -- The new placement's epoch.
  epoch                integer NOT NULL,
  -- answer: an answered ask woke a person-park; repository: an approved
  -- repository; person: a person's Resume; idle: a person resumed an idle
  -- park.
  cause                text NOT NULL CHECK (cause IN ('answer', 'repository', 'person', 'idle')),
  -- When the resume became due: the person's Resume, the answer, the
  -- approval.
  woken_at             timestamptz,
  -- When lux accepted dude's resume.
  requested_at         timestamptz,
  -- The new placement, as lux reports it.
  assigned_at          timestamptz,
  image_ready_at       timestamptz,
  volumes_restored_at  timestamptz,
  container_started_at timestamptz,
  workload_started_at  timestamptz,
  host_name            text,
  -- The placement it was stopped from (the previous epoch).
  stopped_host_name    text,
  stop_requested_at    timestamptz,
  exited_at            timestamptz,
  snapshot_done_at     timestamptz,
  uploaded_at          timestamptz,
  snapshot_bytes       bigint,
  -- The host changed; NULL until both hosts are known.
  moved                boolean,
  -- dude's stream got lux's running state for the new epoch.
  running_at           timestamptz,
  -- The agent's first busy after the resume: it took its input.
  busy_at              timestamptz,
  -- The agent's first message, thought or tool call after the resume.
  first_output_at      timestamptz,
  -- run.resume.timed was written for it: once, whatever replays.
  timed_at             timestamptz,
  created_at           timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (run_id, epoch)
);

CREATE INDEX run_resumes_org_idx ON run_resumes (organization_id, created_at DESC);

ALTER TABLE run_resumes ENABLE ROW LEVEL SECURITY;
ALTER TABLE run_resumes FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON run_resumes
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON run_resumes TO dude_app;

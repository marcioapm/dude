-- 068_image_library.sql — the organization's images, built on the dude host.
--
-- An image is a name and a history of Containerfiles (versions). Saving
-- edits the image's one draft; "Build & publish" numbers it and queues a
-- build. dude-image-builder (its own process and role, below) builds it with
-- rootless podman, pushes it, adds the dude layer, and only then publishes
-- it: the image's published_version_id moves to it, in one transaction with
-- the previous published version becoming superseded. Everything that runs
-- in an image (a role, a project's runtime or previews, the organization's
-- default base) names the image by id and gets its published version on
-- its next Run. Design: docs/design/images.md.

CREATE TABLE images (
  id                   text PRIMARY KEY,
  organization_id      text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  -- Immutable: other Containerfiles name it (FROM image:<name>).
  name                 text NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
  description          text NOT NULL DEFAULT '' CHECK (length(description) <= 500),
  -- Hidden from pickers; whatever still names it keeps working.
  archived_at          timestamptz,
  created_by           text REFERENCES people(id) ON DELETE SET NULL,
  created_at           timestamptz NOT NULL DEFAULT now(),
  -- What every user of the image runs; NULL until a version has published.
  published_version_id text,
  UNIQUE (organization_id, name),
  -- The target of the same-organization foreign keys below.
  UNIQUE (organization_id, id)
);

-- Build args are not secrets (history shows them): string → string, at most 50.
CREATE FUNCTION image_build_args_valid(a jsonb) RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
  SELECT jsonb_typeof(a) = 'object'
    AND (SELECT count(*) FROM jsonb_each(a)) <= 50
    AND NOT EXISTS (SELECT 1 FROM jsonb_each(a) e WHERE jsonb_typeof(e.value) <> 'string'
                    OR e.key !~ '^[A-Za-z_][A-Za-z0-9_]{0,127}$' OR length(e.value #>> '{}') > 4096)
$$;

CREATE TABLE image_versions (
  id              text PRIMARY KEY,
  organization_id text NOT NULL,
  image_id        text NOT NULL,
  -- 1, 2, … per image, given when the version is queued; NULL for a draft.
  number          integer CHECK (number >= 1),
  containerfile   text NOT NULL CHECK (octet_length(containerfile) <= 65536),
  build_args      jsonb NOT NULL DEFAULT '{}' CHECK (image_build_args_valid(build_args)),
  note            text NOT NULL DEFAULT '' CHECK (length(note) <= 500),
  -- person: someone saved it; base_rebuild: dude queued it because an image
  -- it is built FROM published.
  source          text NOT NULL DEFAULT 'person' CHECK (source IN ('person', 'base_rebuild')),
  state           text NOT NULL DEFAULT 'draft'
                  CHECK (state IN ('draft', 'queued', 'building', 'pushing', 'published', 'failed', 'superseded', 'cancelled')),
  created_by      text REFERENCES people(id) ON DELETE SET NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  -- After a successful build: the image without the dude layer, by digest
  -- (<repository>@sha256:…), what children build FROM; and when.
  user_ref        text,
  built_at        timestamptz,
  -- Why its build failed, in one sentence.
  error           text,
  FOREIGN KEY (organization_id, image_id) REFERENCES images (organization_id, id) ON DELETE CASCADE,
  UNIQUE (image_id, number),
  UNIQUE (organization_id, id),
  CHECK ((state = 'draft') = (number IS NULL)),
  CHECK (state NOT IN ('published', 'superseded') OR user_ref IS NOT NULL)
);
-- One draft per image: saving edits it.
CREATE UNIQUE INDEX image_versions_one_draft ON image_versions (image_id) WHERE state = 'draft';
CREATE INDEX image_versions_image_idx ON image_versions (image_id, created_at DESC);

ALTER TABLE images ADD FOREIGN KEY (organization_id, published_version_id)
  REFERENCES image_versions (organization_id, id) DEFERRABLE INITIALLY DEFERRED;

-- What a version is built FROM (`FROM image:<name>`, resolved when it is
-- saved) and, once its build resolved it, the parent's version it used.
CREATE TABLE image_version_parents (
  organization_id   text NOT NULL,
  version_id        text NOT NULL,
  parent_image_id   text NOT NULL,
  parent_version_id text,
  PRIMARY KEY (version_id, parent_image_id),
  FOREIGN KEY (organization_id, version_id) REFERENCES image_versions (organization_id, id) ON DELETE CASCADE,
  FOREIGN KEY (organization_id, parent_image_id) REFERENCES images (organization_id, id),
  FOREIGN KEY (organization_id, parent_version_id) REFERENCES image_versions (organization_id, id)
);
CREATE INDEX image_version_parents_parent_idx ON image_version_parents (parent_image_id);

-- A version finished with one dude layer: its user image, the layer copied
-- on, and the final image Runs use. One per (version, layer): a new dude
-- layer finishes a published version again the first time a Run needs it.
CREATE TABLE image_finals (
  organization_id  text NOT NULL,
  image_version_id text NOT NULL,
  layer_ref        text NOT NULL,
  final_ref        text NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (image_version_id, layer_ref),
  FOREIGN KEY (organization_id, image_version_id) REFERENCES image_versions (organization_id, id) ON DELETE CASCADE
);

-- The builder's queue, one host-wide line: finish jobs (a Run waits on
-- them) before build jobs, each oldest first, one at a time.
CREATE TABLE image_builds (
  id               text PRIMARY KEY,
  organization_id  text NOT NULL,
  image_version_id text NOT NULL,
  kind             text NOT NULL CHECK (kind IN ('build', 'finish')),
  -- The dude layer: for a finish, the one it adds; for a build, the one it
  -- ended with (set when it gets there).
  layer_ref        text,
  -- NULL: dude (a cascade rebuild, a Run's finish).
  requested_by     text REFERENCES people(id) ON DELETE SET NULL,
  requested_at     timestamptz NOT NULL DEFAULT now(),
  started_at       timestamptz,
  heartbeat_at     timestamptz,
  finished_at      timestamptz,
  state            text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
  -- While running: resolving, building, pushing, finishing, publishing.
  stage            text,
  error            text,
  -- The tail of podman's output, at most 1 MiB (the builder keeps the end).
  log              text NOT NULL DEFAULT '',
  -- A job its builder died under is re-queued once; the second time it fails.
  restarts         integer NOT NULL DEFAULT 0,
  -- Seconds spent building and pushing, for the build page.
  build_seconds    double precision,
  push_seconds     double precision,
  FOREIGN KEY (organization_id, image_version_id) REFERENCES image_versions (organization_id, id) ON DELETE CASCADE,
  CHECK (kind = 'build' OR layer_ref IS NOT NULL)
);
-- Two Runs needing the same finish share one job.
CREATE UNIQUE INDEX image_builds_one_finish ON image_builds (image_version_id, layer_ref)
  WHERE kind = 'finish' AND state IN ('queued', 'running');
CREATE UNIQUE INDEX image_builds_one_build ON image_builds (image_version_id)
  WHERE kind = 'build' AND state IN ('queued', 'running');
CREATE INDEX image_builds_queue_idx ON image_builds ((kind = 'build'), requested_at) WHERE state = 'queued';
-- image_queue_ahead's scan of what is queued or running, whatever its kind.
CREATE INDEX image_builds_live_idx ON image_builds (state) WHERE state IN ('queued', 'running');
CREATE INDEX image_builds_version_idx ON image_builds (image_version_id, requested_at DESC);

-- Where an image is named, by id, same organization enforced. The free-text
-- columns stay: with no id set, the text is used as before.
ALTER TABLE organizations ADD COLUMN default_image_id text;
ALTER TABLE organizations ADD FOREIGN KEY (id, default_image_id) REFERENCES images (organization_id, id);
ALTER TABLE projects
  ADD COLUMN runtime_image_id text,
  ADD COLUMN preview_image_id text,
  ADD FOREIGN KEY (organization_id, runtime_image_id) REFERENCES images (organization_id, id),
  ADD FOREIGN KEY (organization_id, preview_image_id) REFERENCES images (organization_id, id);

-- What image a Run got: {imageId, name, versionId, version, ref, layer} —
-- ref the final digest lux pulls. Written when it is resolved, never again,
-- so a resume and the Run's page keep what it started with. image_build_id:
-- the finish job a pending Run waits on ("Preparing image").
ALTER TABLE runs
  ADD COLUMN image jsonb,
  ADD COLUMN image_build_id text REFERENCES image_builds(id) ON DELETE SET NULL;
-- The foreign key's ON DELETE reads runs by it.
CREATE INDEX runs_image_build_idx ON runs (image_build_id) WHERE image_build_id IS NOT NULL;

-- The builder's heartbeat, one row: written every 30 s, idle or not. One
-- not seen for 2 minutes is offline; the Images page and waiting Runs say
-- since when. Nobody's data, so no row-level security.
CREATE TABLE image_builder (
  id      boolean PRIMARY KEY DEFAULT true CHECK (id),
  seen_at timestamptz NOT NULL,
  version text NOT NULL DEFAULT ''
);
GRANT SELECT ON image_builder TO dude_app;

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['images', 'image_versions', 'image_version_parents', 'image_finals', 'image_builds']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format(
      'CREATE POLICY tenant_isolation ON %I USING (organization_id = current_organization_id())'
      ' WITH CHECK (organization_id = current_organization_id())', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO dude_app', t);
  END LOOP;
END $$;

-- The builder: its own login (aiverse sets its password), no BYPASSRLS. It
-- is cross-tenant by nature — one queue for every organization — so each
-- image table has a policy for it alone, and it is granted those tables,
-- INSERT of image.* events, and nothing else.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dude_builder') THEN
    CREATE ROLE dude_builder LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;
  END IF;
END $$;
GRANT USAGE ON SCHEMA public TO dude_builder;
GRANT SELECT, UPDATE ON images TO dude_builder;
GRANT SELECT, INSERT, UPDATE ON image_versions TO dude_builder;
GRANT SELECT, INSERT, UPDATE ON image_version_parents TO dude_builder;
GRANT SELECT, INSERT ON image_finals TO dude_builder;
GRANT SELECT, INSERT, UPDATE ON image_builds TO dude_builder;
GRANT INSERT ON events TO dude_builder;
GRANT SELECT, INSERT, UPDATE ON image_builder TO dude_builder;
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['images', 'image_versions', 'image_version_parents', 'image_finals', 'image_builds']
  LOOP
    EXECUTE format('CREATE POLICY builder ON %I TO dude_builder USING (true) WITH CHECK (true)', t);
  END LOOP;
END $$;
CREATE POLICY builder_events ON events FOR INSERT TO dude_builder WITH CHECK (event_type LIKE 'image.%');

-- How many jobs, of any organization, the builder takes before this one:
-- a count, nothing about whose they are.
CREATE FUNCTION image_queue_ahead(p_build text) RETURNS integer LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = public AS $$
  SELECT count(*)::integer FROM image_builds o, image_builds me
  WHERE me.id = p_build AND me.state = 'queued'
    AND me.organization_id = current_organization_id()
    AND (o.state = 'running'
         OR (o.state = 'queued' AND ((o.kind = 'build') < (me.kind = 'build')
             OR ((o.kind = 'build') = (me.kind = 'build') AND (o.requested_at, o.id) < (me.requested_at, me.id)))))
$$;
REVOKE ALL ON FUNCTION image_queue_ahead(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION image_queue_ahead(text) TO dude_app;

-- An id as packages/domain/src/ids.ts and orchestrator/internal/ids make
-- them (<prefix>_<base36 ms, 9 wide><16 hex>), for rows made in SQL.
CREATE FUNCTION new_id(prefix text) RETURNS text LANGUAGE plpgsql VOLATILE AS $$
DECLARE
  ms bigint := floor(extract(epoch FROM clock_timestamp()) * 1000);
  s text := '';
BEGIN
  WHILE ms > 0 LOOP
    s := substr('0123456789abcdefghijklmnopqrstuvwxyz', (ms % 36)::int + 1, 1) || s;
    ms := ms / 36;
  END LOOP;
  RETURN prefix || '_' || lpad(s, 9, '0') || substr(replace(gen_random_uuid()::text, '-', ''), 1, 16);
END $$;

-- Publish a built version: it becomes what every user of its image runs,
-- the one published before is superseded, and every image whose published
-- version is built FROM this image is queued to rebuild on it (a new
-- version with the same Containerfile, by dude) — unless one is already
-- waiting, which resolves its parents when it starts and so gets this one.
-- One definition for both writers: the backend (publishing an older
-- version again) and dude-image-builder (a build that passed). Returns the
-- rebuilds queued, for the caller's events.
CREATE FUNCTION image_publish(p_version text)
RETURNS TABLE (image_id text, image_name text, version_id text, version integer, build_id text)
LANGUAGE plpgsql AS $$
DECLARE
  v image_versions;
  parent images;
  child record;
  nv text;
  nb text;
  n integer;
BEGIN
  SELECT * INTO v FROM image_versions WHERE id = p_version FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'no image version %', p_version USING ERRCODE = 'P0002'; END IF;
  IF v.user_ref IS NULL THEN RAISE EXCEPTION 'version % has not been built', p_version USING ERRCODE = '22023'; END IF;
  SELECT * INTO parent FROM images WHERE id = v.image_id FOR UPDATE;
  UPDATE image_versions SET state = 'superseded', updated_at = now()
    WHERE image_versions.image_id = v.image_id AND state = 'published' AND id <> v.id;
  UPDATE image_versions SET state = 'published', updated_at = now(), error = NULL WHERE id = v.id;
  UPDATE images SET published_version_id = v.id WHERE id = v.image_id;
  FOR child IN
    SELECT c.id, c.name, c.organization_id, cv.containerfile, cv.build_args
    FROM images c
    JOIN image_versions cv ON cv.id = c.published_version_id
    WHERE c.organization_id = v.organization_id
      AND EXISTS (SELECT 1 FROM image_version_parents p WHERE p.version_id = cv.id AND p.parent_image_id = v.image_id)
      AND NOT EXISTS (SELECT 1 FROM image_versions w WHERE w.image_id = c.id AND w.state = 'queued')
    ORDER BY c.name
    FOR UPDATE OF c
  LOOP
    SELECT COALESCE(max(number), 0) + 1 INTO n FROM image_versions WHERE image_versions.image_id = child.id;
    nv := new_id('imv');
    nb := new_id('imb');
    INSERT INTO image_versions (id, organization_id, image_id, number, containerfile, build_args, note, source, state)
    VALUES (nv, child.organization_id, child.id, n, child.containerfile, child.build_args,
            'Rebuild on ' || parent.name || ' v' || v.number, 'base_rebuild', 'queued');
    INSERT INTO image_version_parents (organization_id, version_id, parent_image_id)
    SELECT child.organization_id, nv, p.parent_image_id FROM image_version_parents p
    JOIN images ci ON ci.published_version_id = p.version_id AND ci.id = child.id;
    INSERT INTO image_builds (id, organization_id, image_version_id, kind) VALUES (nb, child.organization_id, nv, 'build');
    image_id := child.id; image_name := child.name; version_id := nv; version := n; build_id := nb;
    RETURN NEXT;
  END LOOP;
END $$;
GRANT EXECUTE ON FUNCTION new_id(text), image_publish(text) TO dude_app, dude_builder;

-- A project's preview settings gain imageId (the column above).
CREATE OR REPLACE FUNCTION preview_settings(p projects) RETURNS json LANGUAGE sql STABLE AS $$
  SELECT json_build_object('image', p.preview_settings->'image',
    'imageId', p.preview_image_id,
    'egress', COALESCE(p.preview_settings->'egress', '[]'::jsonb),
    'idleTimeoutMinutes', COALESCE((p.preview_settings->>'idleTimeoutMinutes')::float8, 15),
    'machineSize', p.preview_settings->'machineSize')
$$;

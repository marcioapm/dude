-- 063_machine_sizes.sql — every agent and every branch preview runs on a
-- named machine size.
--
-- A size is the organization's, changed by its admins: CPUs, memory and
-- disk, and the lux pool it runs in (NULL: the organization's default pool
-- in lux). dude sends it as the RunSpec's `resources`, and its pool as
-- `placement.poolId`. The pool is kept by lux's id, which a rename in lux
-- leaves alone; its name is read from lux whenever it is shown. Every agent
-- role names one in its settings
-- (`machineSize` in organizations.default_agent_models and
-- projects.agent_models, field by field like its model); a branch preview
-- in projects.preview_settings. What names none runs on the organization's
-- default size.
--
-- Sizes move in steps — half a CPU, half a GiB of memory (stored in MiB, a
-- multiple of 512), 5 GiB of disk — held here as well as by the API, so an
-- illegal size cannot exist.
CREATE TABLE machine_sizes (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  name            text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 40),
  -- Unscaled, so 2.04 is refused rather than rounded to 2.0 first.
  cpus            numeric NOT NULL CHECK (cpus >= 0.5 AND cpus <= 256 AND cpus * 2 = trunc(cpus * 2)),
  memory_mib      integer NOT NULL CHECK (memory_mib >= 512 AND memory_mib <= 2097152 AND memory_mib % 512 = 0),
  disk_gib        integer NOT NULL CHECK (disk_gib >= 5 AND disk_gib <= 20000 AND disk_gib % 5 = 0),
  -- A lux pool's id (pool_…); NULL is the organization's default pool.
  pool_id         text CHECK (pool_id ~ '^pool_[A-Za-z0-9_-]+$'),
  is_default      boolean NOT NULL DEFAULT false,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      text REFERENCES people(id) ON DELETE SET NULL
);

-- A name is unique in its organization, whatever its case.
CREATE UNIQUE INDEX machine_sizes_name_idx ON machine_sizes (organization_id, lower(name));
-- At most one default per organization: a partial uniqueness, as an
-- exclusion constraint because only a constraint can be DEFERRABLE, and a
-- unique index is checked row by row — the API's one statement that moves
-- the default would fail or pass by the order it met the rows. Checked at
-- the statement's end instead. The API refuses to remove the default, so
-- there is always exactly one.
ALTER TABLE machine_sizes ADD CONSTRAINT machine_sizes_one_default
  EXCLUDE USING btree (organization_id WITH =) WHERE (is_default) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE machine_sizes ENABLE ROW LEVEL SECURITY;
ALTER TABLE machine_sizes FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON machine_sizes
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON machine_sizes TO dude_app;

-- Every organization starts with one size, lux's own built-in default, so
-- deploying this changes what nothing runs on.
CREATE FUNCTION seed_machine_size() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path = public AS $$
BEGIN
  INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib, pool_id, is_default)
  VALUES ('msz_' || substr(replace(gen_random_uuid()::text, '-', ''), 1, 24), NEW.id, 'Standard', 2, 8192, 20, NULL, true);
  RETURN NEW;
END $$;
CREATE TRIGGER organizations_seed_machine_size AFTER INSERT ON organizations
  FOR EACH ROW EXECUTE FUNCTION seed_machine_size();

INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib, pool_id, is_default)
SELECT 'msz_' || substr(replace(gen_random_uuid()::text, '-', ''), 1, 24), o.id, 'Standard', 2, 8192, 20, NULL, true
FROM organizations o;

-- A size as the API shows it, for both processes.
CREATE FUNCTION machine_size(s machine_sizes) RETURNS json LANGUAGE sql STABLE AS $$
  SELECT json_build_object('id', s.id, 'name', s.name, 'cpus', s.cpus::float8, 'memoryMiB', s.memory_mib,
    'diskGiB', s.disk_gib, 'poolId', s.pool_id, 'isDefault', s.is_default, 'updatedAt', s.updated_at,
    'updatedBy', (SELECT json_build_object('id', p.id, 'name', p.name) FROM people p WHERE p.id = s.updated_by))
$$;

-- What each Run ran on, as it was when its spec was built:
-- {sizeId, name, cpus, memoryMiB, diskGiB, poolId, pool, from} — pool is
-- the pool's name in lux then — and memoryLimit once lux reports the limit
-- it gave the container. Kept as written, so a Run's history stays true
-- after its size is edited or removed, or its pool renamed.
ALTER TABLE runs ADD COLUMN machine jsonb;

-- A project's preview settings gain machineSize (NULL: the default size).
CREATE OR REPLACE FUNCTION preview_settings(p projects) RETURNS json LANGUAGE sql STABLE AS $$
  SELECT json_build_object('image', p.preview_settings->'image',
    'egress', COALESCE(p.preview_settings->'egress', '[]'::jsonb),
    'idleTimeoutMinutes', COALESCE((p.preview_settings->>'idleTimeoutMinutes')::float8, 30),
    'machineSize', p.preview_settings->'machineSize')
$$;

/**
 * Migration 068, the image library: what the database itself holds — an
 * organization sees only its images, nothing names another organization's
 * image, one draft per image, one live finish per (version, layer) — and
 * what the builder's role may and may not do.
 *
 * Requires DATABASE_URL: a role that can create databases and set a
 * cluster role's password (the owner, a superuser in the dev container).
 */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const NAME = `dude_images_mig_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ROOT = new URL("../../..", import.meta.url).pathname;

let admin: SQL;
let owner: SQL;
let app: SQL;
let builder: SQL;

function urlFor(user: string, password: string): string {
  const url = new URL(OWNER_URL);
  url.pathname = `/${NAME}`;
  url.username = user;
  url.password = password;
  return url.toString();
}

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const migrated = Bun.spawnSync(["bun", "run", "apps/control-plane/src/db/migrate.ts"], {
    cwd: ROOT,
    env: { ...process.env, DATABASE_URL: urlFor(new URL(OWNER_URL).username, new URL(OWNER_URL).password) },
  });
  if (migrated.exitCode !== 0) throw new Error(migrated.stderr.toString());
  // aiverse sets the builder's password; the migration creates it with none.
  await admin.unsafe(`ALTER ROLE dude_builder PASSWORD 'dude_builder'`);
  owner = new SQL(urlFor(new URL(OWNER_URL).username, new URL(OWNER_URL).password));
  await owner`INSERT INTO organizations (id, name, slug) VALUES ('org_a', 'A', 'a'), ('org_b', 'B', 'b')`;
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_a', 'org_a', 'A', 'a', 'A')`;
  for (const org of ["org_a", "org_b"]) {
    await owner`INSERT INTO images (id, organization_id, name) VALUES (${"img_" + org}, ${org}, 'base')`;
    await owner`INSERT INTO image_versions (id, organization_id, image_id, number, containerfile, state)
      VALUES (${"imv_" + org}, ${org}, ${"img_" + org}, 1, 'FROM debian', 'queued')`;
    await owner`INSERT INTO image_builds (id, organization_id, image_version_id, kind, requested_at)
      VALUES (${"imb_" + org}, ${org}, ${"imv_" + org}, 'build', now() - make_interval(secs => ${org === "org_a" ? 60 : 30}))`;
  }
  app = new SQL(urlFor("dude_app", "dude_app"));
  builder = new SQL(urlFor("dude_builder", "dude_builder"));
}, 120_000);

afterAll(async () => {
  await Promise.all([app?.end(), builder?.end(), owner?.end()]);
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

const inOrg = <T>(org: string, fn: (tx: SQL) => Promise<T>) =>
  app.begin(async (tx) => {
    await tx`SELECT set_config('app.organization_id', ${org}, true)`;
    return fn(tx as unknown as SQL);
  });
const failure = async (p: Promise<unknown>): Promise<string> => {
  try {
    await p;
    return "";
  } catch (err) {
    return String(err);
  }
};

describe("tenancy", () => {
  test("an organization sees only its own images, versions and builds", async () => {
    const seen = await inOrg("org_a", async (tx) => ({
      images: (await tx`SELECT id FROM images`).map((r: { id: string }) => r.id),
      versions: (await tx`SELECT id FROM image_versions`).map((r: { id: string }) => r.id),
      builds: (await tx`SELECT id FROM image_builds`).map((r: { id: string }) => r.id),
    }));
    expect(seen).toEqual({ images: ["img_org_a"], versions: ["imv_org_a"], builds: ["imb_org_a"] });
  });

  test("an organization reads only its own build logs", async () => {
    await owner`INSERT INTO image_build_log (organization_id, build_id, start_offset, chunk)
      VALUES ('org_a', 'imb_org_a', 0, 'a'), ('org_b', 'imb_org_b', 0, 'b')`;
    expect(await inOrg("org_a", async (tx) => (await tx`SELECT chunk FROM image_build_log`).map((r: { chunk: string }) => r.chunk))).toEqual(["a"]);
    // A chunk cannot claim another organization's build.
    expect(await failure(owner`INSERT INTO image_build_log (organization_id, build_id, start_offset, chunk) VALUES ('org_b', 'imb_org_a', 1, 'x')`))
      .toMatch(/foreign key/);
    await owner`DELETE FROM image_build_log`;
  });

  test("a project cannot name another organization's image, nor an organization its default", async () => {
    expect(await failure(owner`UPDATE projects SET runtime_image_id = 'img_org_b' WHERE id = 'prj_a'`)).toMatch(/foreign key/);
    expect(await failure(owner`UPDATE projects SET preview_image_id = 'img_org_b' WHERE id = 'prj_a'`)).toMatch(/foreign key/);
    expect(await failure(owner`UPDATE organizations SET default_image_id = 'img_org_b' WHERE id = 'org_a'`)).toMatch(/foreign key/);
    await owner`UPDATE projects SET runtime_image_id = 'img_org_a' WHERE id = 'prj_a'`;
  });

  test("its queue position counts every organization's jobs, and shows nothing of them", async () => {
    // org_b's job was asked for later: org_a's is first, org_b's has one ahead.
    expect(await inOrg("org_a", async (tx) => (await tx`SELECT image_queue_ahead('imb_org_a') AS n`)[0].n)).toBe(0);
    expect(await inOrg("org_b", async (tx) => (await tx`SELECT image_queue_ahead('imb_org_b') AS n`)[0].n)).toBe(1);
    // Another organization's job is not counted for it.
    expect(await inOrg("org_a", async (tx) => (await tx`SELECT image_queue_ahead('imb_org_b') AS n`)[0].n)).toBe(0);
  });
});

describe("integrity", () => {
  test("one draft per image", async () => {
    await owner`INSERT INTO image_versions (id, organization_id, image_id, containerfile) VALUES ('d1', 'org_a', 'img_org_a', 'FROM x')`;
    expect(await failure(owner`INSERT INTO image_versions (id, organization_id, image_id, containerfile) VALUES ('d2', 'org_a', 'img_org_a', 'FROM y')`))
      .toMatch(/image_versions_one_draft/);
  });

  test("a draft has no number, a queued version has one", async () => {
    expect(await failure(owner`INSERT INTO image_versions (id, organization_id, image_id, containerfile, state) VALUES ('q', 'org_a', 'img_org_a', 'FROM x', 'queued')`))
      .toMatch(/check constraint/);
  });

  test("a published version has its user image", async () => {
    expect(await failure(owner`UPDATE image_versions SET state = 'published' WHERE id = 'imv_org_a'`)).toMatch(/check constraint/);
  });

  test("a Containerfile over 64 KiB, and a build argument that is not a string, are refused", async () => {
    expect(await failure(owner`UPDATE image_versions SET containerfile = ${"x".repeat(65537)} WHERE id = 'imv_org_a'`)).toMatch(/check constraint/);
    expect(await failure(owner`UPDATE image_versions SET build_args = '{"A": 1}' WHERE id = 'imv_org_a'`)).toMatch(/check constraint/);
  });

  test("two finishes of one version with one layer while one is live are one job", async () => {
    await owner`INSERT INTO image_builds (id, organization_id, image_version_id, kind, layer_ref) VALUES ('f1', 'org_a', 'imv_org_a', 'finish', 'L')`;
    expect(await failure(owner`INSERT INTO image_builds (id, organization_id, image_version_id, kind, layer_ref) VALUES ('f2', 'org_a', 'imv_org_a', 'finish', 'L')`))
      .toMatch(/image_builds_one_finish/);
    await owner`UPDATE image_builds SET state = 'failed' WHERE id = 'f1'`;
    await owner`INSERT INTO image_builds (id, organization_id, image_version_id, kind, layer_ref) VALUES ('f3', 'org_a', 'imv_org_a', 'finish', 'L')`;
  });
});

describe("the builder's role", () => {
  test("is not a superuser and does not bypass row-level security", async () => {
    const [r] = await admin`SELECT rolsuper, rolbypassrls, rolcanlogin FROM pg_roles WHERE rolname = 'dude_builder'`;
    expect(r).toEqual({ rolsuper: false, rolbypassrls: false, rolcanlogin: true });
  });

  test("sees every organization's queue, without naming one", async () => {
    const ids = (await builder`SELECT id FROM image_builds WHERE kind = 'build' ORDER BY id`).map((r: { id: string }) => r.id);
    expect(ids).toEqual(["imb_org_a", "imb_org_b"]);
  });

  test("writes image events, and no other", async () => {
    await builder`INSERT INTO events (id, organization_id, event_type, actor_type, actor_id, source)
      VALUES ('evt_b1', 'org_a', 'image.published', 'system', 'dude-image-builder', 'control-plane')`;
    expect(await failure(builder`INSERT INTO events (id, organization_id, event_type, actor_type, actor_id, source)
      VALUES ('evt_b2', 'org_a', 'task.created', 'system', 'x', 'control-plane')`)).toMatch(/row-level security/);
  });

  test("reads nothing else: not projects, people, runs, events or credentials", async () => {
    for (const table of ["projects", "people", "runs", "events", "forge_credentials", "organizations"]) {
      expect(await failure(builder.unsafe(`SELECT 1 FROM ${table} LIMIT 1`))).toMatch(/permission denied/);
    }
  });

  test("cannot delete what it builds", async () => {
    expect(await failure(builder`DELETE FROM image_versions WHERE id = 'imv_org_a'`)).toMatch(/permission denied/);
    expect(await failure(builder`DELETE FROM image_builds WHERE id = 'imb_org_a'`)).toMatch(/permission denied/);
  });

  test("appends, reads and trims any organization's build log, and cannot rewrite a chunk", async () => {
    await builder`INSERT INTO image_build_log (organization_id, build_id, start_offset, chunk)
      VALUES ('org_a', 'imb_org_a', 0, 'one'), ('org_b', 'imb_org_b', 0, 'two')`;
    expect((await builder`SELECT chunk FROM image_build_log ORDER BY chunk`).map((r: { chunk: string }) => r.chunk)).toEqual(["one", "two"]);
    expect(await failure(builder`UPDATE image_build_log SET chunk = 'x'`)).toMatch(/permission denied/);
    await builder`DELETE FROM image_build_log`;
    expect((await owner`SELECT count(*)::int AS n FROM image_build_log`)[0].n).toBe(0);
  });
});

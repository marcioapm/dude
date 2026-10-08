/**
 * The migrations a release carries. bin/dude-migrate embeds migrations/*.sql
 * (scripts/build-migrate.sh) and reads nothing from disk; `bun run migrate`
 * reads the repository's directory. Both must apply the same files and
 * record the same checksums, or a database migrated one way would be
 * refused by the other.
 *
 * Requires DATABASE_URL: a role that can create databases (the owner).
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { createHash } from "node:crypto";
import { mkdtemp, readdir, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { listMigrationFiles, migrate, outsideTransaction, repoMigrationsDir } from "../src/db/migrate.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");

let work: string;
let binary: string;
let admin: SQL;
const databases: string[] = [];
const roles: string[] = [];

function databaseUrl(name: string): string {
  const url = new URL(OWNER_URL);
  url.pathname = `/${name}`;
  return url.toString();
}

async function createDatabase(): Promise<string> {
  const name = `dude_migrate_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
  await admin.unsafe(`CREATE DATABASE "${name}"`);
  databases.push(name);
  return databaseUrl(name);
}

function run(cmd: string[], env: Record<string, string> = {}): { code: number; out: string } {
  const p = Bun.spawnSync(cmd, { cwd: ROOT, env: { ...process.env, ...env }, stdout: "pipe", stderr: "pipe" });
  return { code: p.exitCode, out: p.stdout.toString() + p.stderr.toString() };
}

async function recorded(url: string): Promise<{ version: string | undefined; name: string; checksum: string }[]> {
  const sql = new SQL(url);
  try {
    return await sql`SELECT version, name, checksum FROM schema_migrations ORDER BY version`;
  } finally {
    await sql.end();
  }
}

async function repoSqlFiles(): Promise<string[]> {
  return (await readdir(repoMigrationsDir)).filter((f) => f.endsWith(".sql")).sort();
}

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  work = await mkdtemp(join(tmpdir(), "dude-migrate-"));
  binary = join(work, "dude-migrate");
  const built = run(["scripts/build-migrate.sh", binary]);
  if (built.code !== 0) throw new Error(`build-migrate.sh failed:\n${built.out}`);
}, 120_000);

afterAll(async () => {
  for (const name of databases) await admin.unsafe(`DROP DATABASE IF EXISTS "${name}" WITH (FORCE)`);
  for (const role of roles) {
    await admin.unsafe(`DROP OWNED BY "${role}"`);
    await admin.unsafe(`DROP ROLE IF EXISTS "${role}"`);
  }
  await admin.end();
  await rm(work, { recursive: true, force: true });
  // Dropping a database per test outlasts the default 5 s on a loaded host.
}, 60_000);

test("from source, the runner reads every .sql file in migrations/, in order", async () => {
  const files = await listMigrationFiles();
  expect(files.map((f) => f.name)).toEqual(await repoSqlFiles());
});

test("the binary applies every file in migrations/ with the checksum of its bytes on disk", async () => {
  // From a directory with no SQL in it, so nothing can be read from disk.
  const url = await createDatabase();
  const first = Bun.spawnSync([binary], { cwd: work, env: { ...process.env, DATABASE_URL: url }, stderr: "pipe" });
  expect(first.exitCode, first.stderr.toString()).toBe(0);

  const names = await repoSqlFiles();
  const want = await Promise.all(
    names.map(async (name) => ({
      version: name.split("_")[0],
      name,
      checksum: createHash("sha256").update(await readFile(join(repoMigrationsDir, name))).digest("hex"),
    })),
  );
  expect(await recorded(url)).toEqual(want);

  const status = Bun.spawnSync([binary, "--status"], { cwd: work, env: { ...process.env, DATABASE_URL: url } });
  expect(status.stdout.toString()).toBe(names.map((n) => `applied  ${n}\n`).join(""));
}, 120_000);

test("a database migrated from the repository is up to date for the binary, and the reverse", async () => {
  const fromRepo = await createDatabase();
  const fromBinary = await createDatabase();

  const repo = run(["bun", "run", "migrate"], { DATABASE_URL: fromRepo });
  expect(repo.code, repo.out).toBe(0);
  const bin = run([binary], { DATABASE_URL: fromBinary });
  expect(bin.code, bin.out).toBe(0);
  expect(await recorded(fromBinary)).toEqual(await recorded(fromRepo));

  // Each compares the other's recorded checksums and would refuse a mismatch.
  const binOnRepo = run([binary], { DATABASE_URL: fromRepo });
  expect(binOnRepo).toEqual({ code: 0, out: "up to date\n" });
  const repoOnBin = run(["bun", "run", "migrate"], { DATABASE_URL: fromBinary });
  expect(repoOnBin.code, repoOnBin.out).toBe(0);
  expect(repoOnBin.out).toContain("up to date\n");
}, 120_000);

// As on the aiverse host or a managed Postgres: the owner bypasses row-level
// security and creates roles, and a superuser created vector beforehand.
async function ownedByANonSuperuser(): Promise<string> {
  const owner = `dude_migrate_owner_${Bun.randomUUIDv7("hex").slice(-12)}`;
  const db = `dude_migrate_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
  roles.push(owner);
  await admin.unsafe(`CREATE ROLE "${owner}" LOGIN NOSUPERUSER BYPASSRLS CREATEROLE PASSWORD 'owner'`);
  // On a fresh cluster the owner creates dude_app and dude_sweeper, and so
  // administers them. Here earlier tests may have created them as superuser.
  for (const role of ["dude_app", "dude_sweeper"]) {
    const [exists] = await admin`SELECT 1 FROM pg_roles WHERE rolname = ${role}`;
    if (exists) await admin.unsafe(`GRANT "${role}" TO "${owner}" WITH ADMIN TRUE, INHERIT FALSE, SET FALSE`);
  }
  await admin.unsafe(`CREATE DATABASE "${db}" OWNER "${owner}"`);
  databases.push(db);
  const asAdmin = new SQL(databaseUrl(db));
  try {
    await asAdmin`CREATE EXTENSION vector`;
  } finally {
    await asAdmin.end();
  }
  const url = new URL(databaseUrl(db));
  url.username = owner;
  url.password = "owner";
  return url.toString();
}

test("an owner that is not a superuser applies every migration", async () => {
  const url = await ownedByANonSuperuser();
  const migrated = run([binary], { DATABASE_URL: url });
  expect(migrated.code, migrated.out).toBe(0);
  expect((await recorded(url)).map((r) => r.name)).toEqual(await repoSqlFiles());
  const [app] = await admin`SELECT rolsuper, rolbypassrls, rolcreatedb, rolcreaterole FROM pg_roles WHERE rolname = 'dude_app'`;
  expect(app).toEqual({ rolsuper: false, rolbypassrls: false, rolcreatedb: false, rolcreaterole: false });
}, 120_000);

test("a database migrated by v0.1.0 is still up to date", async () => {
  const url = await createDatabase();
  expect(run([binary], { DATABASE_URL: url }).code).toBe(0);
  const sql = new SQL(url);
  try {
    // What v0.1.0 recorded for 002, before its ALTER ROLE became conditional.
    const released002 = "9629d868e268365046359588f469a4834f20a1418c6f3954f8f56797d649cb86";
    await sql`UPDATE schema_migrations SET checksum = ${released002} WHERE version = '002'`;
    expect(run([binary], { DATABASE_URL: url })).toEqual({ code: 0, out: "up to date\n" });

    // Any other checksum is still an edit made after the migration was applied.
    await sql`UPDATE schema_migrations SET checksum = ${"0".repeat(64)} WHERE version = '002'`;
    const refused = run([binary], { DATABASE_URL: url });
    expect(refused.code).not.toBe(0);
    expect(refused.out).toContain("was modified after it was applied");
  } finally {
    await sql.end();
  }
}, 120_000);

test("002 strips every privilege from a dude_app that already holds them", async () => {
  // dude_app is cluster-wide: 002 finds this one instead of creating its own.
  const [exists] = await admin`SELECT 1 FROM pg_roles WHERE rolname = 'dude_app'`;
  if (!exists) await admin.unsafe(`CREATE ROLE dude_app LOGIN PASSWORD 'dude_app'`);
  await admin.unsafe("ALTER ROLE dude_app SUPERUSER BYPASSRLS CREATEDB CREATEROLE");
  try {
    const url = await createDatabase();
    const migrated = run([binary], { DATABASE_URL: url });
    expect(migrated.code, migrated.out).toBe(0);
    const [app] = await admin`SELECT rolsuper, rolbypassrls, rolcreatedb, rolcreaterole FROM pg_roles WHERE rolname = 'dude_app'`;
    expect(app).toEqual({ rolsuper: false, rolbypassrls: false, rolcreatedb: false, rolcreaterole: false });
  } finally {
    await admin.unsafe("ALTER ROLE dude_app NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE");
  }
}, 120_000);

test("076 builds the Runs index outside a transaction, records it, and runs again after a crash before its record", async () => {
  const url = await createDatabase();
  const sql = new SQL(url);
  try {
    await sql`CREATE TABLE schema_migrations (version text PRIMARY KEY, name text NOT NULL,
      checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`;
    for (const file of (await listMigrationFiles()).filter((f) => f.version < "076")) {
      const contents = await file.contents();
      await sql.begin(async (tx) => {
        await tx.unsafe(contents);
        await tx`INSERT INTO schema_migrations (version, name, checksum)
          VALUES (${file.version}, ${file.name}, ${createHash("sha256").update(contents).digest("hex")})`;
      });
    }
    // A concurrent build refuses a transaction: applied in one, 076 fails.
    const file076 = (await listMigrationFiles()).find((f) => f.version === "076")!;
    expect(outsideTransaction(await file076.contents())).toBe(true);
    await expect(sql.begin(async (tx) => { await tx.unsafe(await file076.contents()); })).rejects.toThrow();

    expect((await migrate(url, { log: () => {} })).applied).toEqual(["076_runs_conductor_run_idx.sql", "077_conductor_steer.sql", "078_events_run_lands_idx.sql", "079_conductor_github.sql", "080_webhook_repair.sql", "081_preview_secrets.sql", "082_conductor_edits.sql", "083_finding_topic.sql", "084_escalation_questions.sql", "085_brainstorm_role.sql", "086_sessions.sql", "087_session_memories.sql", "088_session_filings_idx.sql", "089_session_functions_parallel.sql", "090_stalled_runs.sql", "091_brainstorm_stuck_turn.sql", "092_retire_completed_runs.sql", "093_lux_name.sql", "094_repository_lux_name_unique.sql", "095_project_key_unique.sql", "096_session_names.sql", "097_session_names_validate.sql"]);
    const valid = async () => (await sql`SELECT i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
      WHERE c.relname = 'runs_conductor_run_idx'`).map((r: { indisvalid: boolean }) => r.indisvalid);
    expect(await valid()).toEqual([true]);
    expect((await recorded(url)).find((m) => m.name === "076_runs_conductor_run_idx.sql")).toBeDefined();

    // Built, then the process died before it was recorded: the next migrate builds nothing twice.
    await sql`DELETE FROM schema_migrations WHERE version = '076'`;
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["076_runs_conductor_run_idx.sql"]);
    expect(await valid()).toEqual([true]);
  } finally {
    await sql.end();
  }
}, 120_000);

test("063 makes waiting work due on any clock, and keeps a refusal's backoff", async () => {
  // Seed work under 062 before upgrading as the non-superuser owner.
  const url = await ownedByANonSuperuser();
  const sql = new SQL(url);
  try {
    await sql`CREATE TABLE schema_migrations (version text PRIMARY KEY, name text NOT NULL,
      checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`;
    for (const file of (await listMigrationFiles()).filter((f) => f.version < "063")) {
      const contents = await file.contents();
      await sql.begin(async (tx) => {
        await tx.unsafe(contents);
        await tx`INSERT INTO schema_migrations (version, name, checksum)
          VALUES (${file.version}, ${file.name}, ${createHash("sha256").update(contents).digest("hex")})`;
      });
    }
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_a', 'a', 'a'), ('org_b', 'b', 'b')`;
    // Waiting in two organizations, stamped by a database clock an hour
    // ahead; one refused until 2030; one embedded.
    await sql`INSERT INTO search_documents (source_type, source_id, organization_id, title, body, content_hash, tsv,
        embedding, attempts, next_attempt_at)
      VALUES ('memory', 'waiting_a', 'org_a', 't', 'b', 'h1', ''::tsvector, NULL, 0, now() + interval '1 hour'),
             ('memory', 'waiting_b', 'org_b', 't', 'b', 'h2', ''::tsvector, NULL, 0, now() + interval '1 hour'),
             ('memory', 'refused', 'org_a', 't', 'b', 'h3', ''::tsvector, NULL, 3, '2030-01-01T00:00:00Z'),
             ('memory', 'embedded', 'org_b', 't', 'b', 'h4', ''::tsvector,
               array_fill(0::real, ARRAY[768])::halfvec, 0, '2020-01-01T00:00:00Z')`;

    expect((await migrate(url, { log: () => {} })).applied).toEqual([
      "063_index_due_on_any_clock.sql",
      "064_machine_sizes.sql",
      "065_wakeable_previews.sql",
      "066_preview_start_failures.sql",
      "067_run_resumes.sql",
      "068_image_library.sql",
      "069_model_tiers.sql",
      "070_kept_runs.sql",
      "071_attachments.sql",
      "072_conductor.sql",
      "073_attempt_metrics.sql",
      "074_task_inline_images.sql",
      "075_conductor_decisions.sql",
      "076_runs_conductor_run_idx.sql",
      "077_conductor_steer.sql",
      "078_events_run_lands_idx.sql",
      "079_conductor_github.sql",
      "080_webhook_repair.sql",
      "081_preview_secrets.sql",
      "082_conductor_edits.sql",
      "083_finding_topic.sql",
      "084_escalation_questions.sql",
      "085_brainstorm_role.sql",
      "086_sessions.sql",
      "087_session_memories.sql",
      "088_session_filings_idx.sql",
      "089_session_functions_parallel.sql",
      "090_stalled_runs.sql",
      "091_brainstorm_stuck_turn.sql",
      "092_retire_completed_runs.sql",
      "093_lux_name.sql",
      "094_repository_lux_name_unique.sql",
      "095_project_key_unique.sql",
      "096_session_names.sql",
      "097_session_names_validate.sql",
    ]);

    // Due by the sweep's own test, on a clock behind the database's.
    const due = async (at: string) =>
      (await sql`SELECT source_id FROM search_documents WHERE embedding IS NULL AND next_attempt_at <= ${at}::timestamptz
        ORDER BY source_id`).map((r: { source_id: string }) => r.source_id);
    expect(await due("2026-01-01T00:00:00Z")).toEqual(["waiting_a", "waiting_b"]);
    expect(await due("2029-12-31T23:59:59Z")).toEqual(["waiting_a", "waiting_b"]);
    expect(await due("2030-01-01T00:00:00Z")).toEqual(["refused", "waiting_a", "waiting_b"]);
    const [embedded] = await sql`SELECT attempts, next_attempt_at::text AS next FROM search_documents WHERE source_id = 'embedded'`;
    expect(embedded).toEqual({ attempts: 0, next: "2020-01-01 00:00:00+00" });
  } finally {
    await sql.end();
  }
}, 120_000);

/** A database with every migration before `version` applied, as a release before it left it. */
async function migratedBefore(version: string): Promise<{ url: string; sql: SQL }> {
  const url = await createDatabase();
  const sql = new SQL(url);
  await sql`CREATE TABLE schema_migrations (version text PRIMARY KEY, name text NOT NULL,
    checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`;
  for (const file of (await listMigrationFiles()).filter((f) => f.version < version)) {
    const contents = await file.contents();
    const record = (q: SQL) => q`INSERT INTO schema_migrations (version, name, checksum)
      VALUES (${file.version}, ${file.name}, ${createHash("sha256").update(contents).digest("hex")})`;
    if (outsideTransaction(contents)) {
      await sql.unsafe(contents);
      await record(sql);
      continue;
    }
    await sql.begin(async (tx) => {
      await tx.unsafe(contents);
      await record(tx);
    });
  }
  return { url, sql };
}

test("094 refuses to apply over two repositories of a project checked out under one name, naming them", async () => {
  const { url, sql } = await migratedBefore("094");
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_a', 'a', 'a')`;
    await sql`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_a', 'org_a', 'A', 'a', 'A'), ('prj_b', 'org_a', 'B', 'b', 'B')`;
    // "Web" is checked out as web-29751047, the name of the other; prj_b's "Web" is another project's.
    await sql`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES
      ('repo_1', 'org_a', 'prj_a', 'Web', 'git://x/1'), ('repo_2', 'org_a', 'prj_a', 'web-29751047', 'git://x/2'),
      ('repo_3', 'org_a', 'prj_b', 'Web', 'git://x/3')`;
    await expect(migrate(url, { log: () => {} })).rejects.toThrow(
      "repositories would share a checkout: project prj_a: 'Web' and 'web-29751047' are both checked out as web-29751047; rename one of each, then migrate again");
    expect((await recorded(url)).map((m) => m.version)).not.toContain("094");

    // Renamed: it applies, and the index refuses the same pair from then on.
    await sql`UPDATE repositories SET name = 'web-app' WHERE id = 'repo_2'`;
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["094_repository_lux_name_unique.sql", "095_project_key_unique.sql", "096_session_names.sql", "097_session_names_validate.sql"]);
    const refusal = async (q: () => Promise<unknown>) => q().then(() => "", (err: Error) => err.message);
    expect(await refusal(async () => await sql`UPDATE repositories SET name = 'web-29751047' WHERE id = 'repo_2'`))
      .toContain("repositories_lux_name_idx");
    expect(await refusal(async () => await sql`INSERT INTO repositories (id, organization_id, project_id, name, url)
      VALUES ('repo_4', 'org_a', 'prj_a', 'web-29751047', 'git://x/4')`)).toContain("repositories_lux_name_idx");
    // Per project: prj_b's "Web", the same checkout name as prj_a's, stands.
    expect((await sql`SELECT id FROM repositories WHERE lux_name(name) = 'web-29751047' ORDER BY id`).map((r: { id: string }) => r.id))
      .toEqual(["repo_1", "repo_3"]);
  } finally {
    await sql.end();
  }
}, 120_000);

test("095 refuses to apply over two projects of an organisation under one key, ignoring case, naming them", async () => {
  const { url, sql } = await migratedBefore("095");
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_a', 'Acme', 'acme'), ('org_b', 'Beta', 'beta')`;
    // BILL twice in org_a (one lower-cased); BILL in org_b too, which is another organisation's.
    await sql`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES
      ('prj_api', 'org_a', 'Billing API', 'billing-api', 'BILL'), ('prj_worker', 'org_a', 'Billing Worker', 'billing-worker', 'bill'),
      ('prj_web', 'org_a', 'Web', 'web', 'WEB'), ('prj_other', 'org_b', 'Billing', 'billing', 'BILL')`;
    await expect(migrate(url, { log: () => {} })).rejects.toThrow(
      "projects would share a key: organization 'Acme' (org_a): 'Billing API' (prj_api) and 'Billing Worker' (prj_worker) both have the key BILL; give one of each another key_prefix, then migrate again");
    expect((await recorded(url)).map((m) => m.version)).not.toContain("095");

    // Given another key: it applies, and the index refuses the same pair from then on.
    await sql`UPDATE projects SET key_prefix = 'BW' WHERE id = 'prj_worker'`;
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["095_project_key_unique.sql", "096_session_names.sql", "097_session_names_validate.sql"]);
    const refusal = async (q: () => Promise<unknown>) => q().then(() => "", (err: Error) => err.message);
    expect(await refusal(async () => await sql`UPDATE projects SET key_prefix = 'Bill' WHERE id = 'prj_worker'`))
      .toContain("projects_key_idx");
    expect(await refusal(async () => await sql`INSERT INTO projects (id, organization_id, name, slug, key_prefix)
      VALUES ('prj_x', 'org_a', 'X', 'x', 'web')`)).toContain("projects_key_idx");
    expect((await sql`SELECT id FROM projects WHERE upper(key_prefix) = 'BILL' ORDER BY id`).map((r: { id: string }) => r.id))
      .toEqual(["prj_api", "prj_other"]);
  } finally {
    await sql.end();
  }
}, 120_000);

test("074 gives each task's tray images a place at the end of its goal, so they stay its own", async () => {
  const url = await ownedByANonSuperuser();
  const sql = new SQL(url);
  try {
    await sql`CREATE TABLE schema_migrations (version text PRIMARY KEY, name text NOT NULL,
      checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`;
    for (const file of (await listMigrationFiles()).filter((f) => f.version < "074")) {
      const contents = await file.contents();
      await sql.begin(async (tx) => {
        await tx.unsafe(contents);
        await tx`INSERT INTO schema_migrations (version, name, checksum)
          VALUES (${file.version}, ${file.name}, ${createHash("sha256").update(contents).digest("hex")})`;
      });
    }
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_a', 'a', 'a')`;
    await sql`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_a', 'org_a', 'A', 'a', 'A')`;
    await sql`INSERT INTO tasks (id, organization_id, project_id, number, title, goal)
      VALUES ('wi_tray', 'org_a', 'prj_a', 1, 'T', 'Build it.  '), ('wi_none', 'org_a', 'prj_a', 2, 'N', 'No images.')`;
    const image = (id: string, name: string, prompt: boolean, position: number) => sql`INSERT INTO attachments (id, organization_id, task_id, name,
        content_type, width, height, bytes, sha256, object_key, original_content_type, original_width, original_height, original_bytes,
        original_key, for_prompt, position, attached_at)
      VALUES (${id}, 'org_a', 'wi_tray', ${name}, 'image/png', 1, 1, 1, 'x', ${id}, 'image/png', 1, 1, 1, ${id + ".o"}, ${prompt}, ${position},
        ${prompt ? new Date() : null})`;
    // Ids sort against the tray's order, so only `position` gives it.
    await image("att_a_second", "b [v2].png", true, 1);
    await image("att_z_first", "a.png", true, 0);
    await image("att_unsent", "c.png", false, 0);

    expect((await migrate(url, { log: () => {} })).applied).toEqual(["074_task_inline_images.sql", "075_conductor_decisions.sql", "076_runs_conductor_run_idx.sql", "077_conductor_steer.sql", "078_events_run_lands_idx.sql", "079_conductor_github.sql", "080_webhook_repair.sql", "081_preview_secrets.sql", "082_conductor_edits.sql", "083_finding_topic.sql", "084_escalation_questions.sql", "085_brainstorm_role.sql", "086_sessions.sql", "087_session_memories.sql", "088_session_filings_idx.sql", "089_session_functions_parallel.sql", "090_stalled_runs.sql", "091_brainstorm_stuck_turn.sql", "092_retire_completed_runs.sql", "093_lux_name.sql", "094_repository_lux_name_unique.sql", "095_project_key_unique.sql", "096_session_names.sql", "097_session_names_validate.sql"]);
    const goals = await sql`SELECT id, goal FROM tasks ORDER BY id`;
    expect(goals).toEqual([
      { id: "wi_none", goal: "No images." },
      { id: "wi_tray", goal: "Build it.\n\n![a.png](attachment:att_z_first)\n\n![b (v2).png](attachment:att_a_second)" },
    ]);
    // Still the prompt's, as the text now says.
    const attached = await sql`SELECT id FROM attachments WHERE for_prompt AND attached_at IS NOT NULL ORDER BY position`;
    expect(attached.map((r: { id: string }) => r.id)).toEqual(["att_z_first", "att_a_second"]);
  } finally {
    await sql.end();
  }
}, 120_000);

test("096 keeps every titled session's name as a person's, and lets a new one start untitled", async () => {
  const url = await ownedByANonSuperuser();
  const sql = new SQL(url);
  try {
    await sql`CREATE TABLE schema_migrations (version text PRIMARY KEY, name text NOT NULL,
      checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`;
    for (const file of (await listMigrationFiles()).filter((f) => f.version < "096")) {
      const contents = await file.contents();
      const record = (tx: SQL) => tx`INSERT INTO schema_migrations (version, name, checksum)
        VALUES (${file.version}, ${file.name}, ${createHash("sha256").update(contents).digest("hex")})`;
      if (outsideTransaction(contents)) {
        await sql.unsafe(contents);
        await record(sql);
        continue;
      }
      await sql.begin(async (tx) => {
        await tx.unsafe(contents);
        await record(tx);
      });
    }
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_n', 'n', 'n')`;
    await sql`INSERT INTO sessions (id, organization_id, title) VALUES ('ssn_old', 'org_n', 'Usage-based billing')`;
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["096_session_names.sql", "097_session_names_validate.sql"]);
    // 097 leaves every check 096 added validated, as if made with the table.
    const checks = await sql`SELECT conname, convalidated FROM pg_constraint
      WHERE conrelid = 'sessions'::regclass AND contype = 'c' ORDER BY conname`;
    expect([...checks]).toEqual([
      { conname: "sessions_title_check", convalidated: true },
      { conname: "sessions_titled_by_check", convalidated: true },
      { conname: "sessions_titled_check", convalidated: true },
    ]);
    const titled = async (id: string) => [...await sql`SELECT title, titled_by FROM sessions WHERE id = ${id}`];
    expect(await titled("ssn_old")).toEqual([{ title: "Usage-based billing", titled_by: "person" }]);
    await sql`INSERT INTO sessions (id, organization_id, title, titled_by) VALUES ('ssn_new', 'org_n', NULL, NULL)`;
    expect(await titled("ssn_new")).toEqual([{ title: null, titled_by: null }]);
    // A title is never blank, and is never said to be someone's while there is none.
    for (const [title, by] of [["  ", "person"], [null, "agent"], ["Named", null], ["Named", "bot"]] as const) {
      const outcome = await sql`INSERT INTO sessions (id, organization_id, title, titled_by) VALUES ('ssn_bad', 'org_n', ${title}, ${by})`
        .then(() => "inserted", (e: Error) => e.message);
      expect([title, by, outcome]).toEqual([title, by, expect.stringContaining("violates check constraint")]);
    }
  } finally {
    await sql.end();
  }
}, 120_000);

test("096 holds its exclusive lock on sessions for no scan: an indexed read waits only for its catalog changes", async () => {
  const { url, sql } = await migratedBefore("096");
  const reader = new SQL(url);
  const observer = new SQL(url);
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_n', 'n', 'n')`;
    await sql`INSERT INTO sessions (id, organization_id, title)
      SELECT 'ssn_' || i, 'org_n', 'Usage-based billing for experiment runs ' || i FROM generate_series(1, 500000) i`;
    await sql`ANALYZE sessions`;
    const file = (await listMigrationFiles()).find((f) => f.version === "096")!;
    const contents = await file.contents();
    // As the runner applies it: the whole file in one transaction.
    let done = false;
    const began = performance.now();
    const applied = sql.begin(async (tx) => {
      await tx.unsafe(contents);
    }).then(() => {
      done = true;
      return performance.now() - began;
    });
    // Wait until the migration holds its lock, or has already let it go.
    let locked = false;
    while (!done && !locked) {
      locked = (await observer`SELECT 1 FROM pg_locks
        WHERE relation = 'sessions'::regclass AND mode = 'AccessExclusiveLock' AND granted`).length > 0;
    }
    const asked = performance.now();
    expect([...await reader`SELECT title FROM sessions WHERE id = 'ssn_1'`]).toEqual([{ title: "Usage-based billing for experiment runs 1" }]);
    const waited = performance.now() - asked;
    const held = await applied;
    // Validating the checks under this lock held such a read 330-370 ms on 500k rows on a laptop; 096's catalog changes alone, ~10 ms.
    expect(waited, `the read waited ${Math.round(waited)} ms; 096 took ${Math.round(held)} ms`).toBeLessThan(100);
    // Nothing was scanned: the checks are there, still to be validated by 097.
    expect((await sql`SELECT bool_or(convalidated) AS any FROM pg_constraint
      WHERE conrelid = 'sessions'::regclass AND contype = 'c'`)[0].any).toBe(false);
  } finally {
    await reader.end();
    await observer.end();
    await sql.end();
  }
}, 120_000);

test("097 refuses a session row that breaks 096's checks, naming the check, and applies once it is fixed", async () => {
  const { url, sql } = await migratedBefore("096");
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_n', 'n', 'n')`;
    await sql`INSERT INTO sessions (id, organization_id, title) VALUES ('ssn_ok', 'org_n', 'Billing')`;
    const file096 = (await listMigrationFiles()).find((f) => f.version === "096")!;
    const contents = await file096.contents();
    await sql.begin(async (tx) => {
      await tx.unsafe(contents);
      await tx`INSERT INTO schema_migrations (version, name, checksum)
        VALUES (${file096.version}, ${file096.name}, ${createHash("sha256").update(contents).digest("hex")})`;
    });
    // A row the NOT VALID check never saw: written while it was not there, as by a process that bypassed it.
    await sql`ALTER TABLE sessions DROP CONSTRAINT sessions_titled_check`;
    await sql`INSERT INTO sessions (id, organization_id, title, titled_by) VALUES ('ssn_bad', 'org_n', 'Named', NULL)`;
    await sql`ALTER TABLE sessions ADD CONSTRAINT sessions_titled_check CHECK ((title IS NULL) = (titled_by IS NULL)) NOT VALID`;

    await expect(migrate(url, { log: () => {} })).rejects.toThrow(
      `check constraint "sessions_titled_check" of relation "sessions" is violated by some row`);
    expect((await recorded(url)).map((m) => m.version)).not.toContain("097");

    await sql`UPDATE sessions SET titled_by = 'person' WHERE id = 'ssn_bad'`;
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["097_session_names_validate.sql"]);
    expect([...await sql`SELECT conname FROM pg_constraint
      WHERE conrelid = 'sessions'::regclass AND contype = 'c' AND NOT convalidated`]).toEqual([]);
  } finally {
    await sql.end();
  }
}, 120_000);

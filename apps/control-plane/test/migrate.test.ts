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

    expect((await migrate(url, { log: () => {} })).applied).toEqual(["076_runs_conductor_run_idx.sql", "077_conductor_steer.sql", "078_events_run_lands_idx.sql", "079_conductor_github.sql", "080_webhook_repair.sql", "081_preview_secrets.sql", "082_conductor_edits.sql", "083_finding_topic.sql", "084_escalation_questions.sql", "085_brainstorm_role.sql", "086_sessions.sql", "087_session_memories.sql", "088_session_filings_idx.sql", "089_session_functions_parallel.sql", "090_stalled_runs.sql", "091_brainstorm_stuck_turn.sql", "092_retire_completed_runs.sql", "093_lux_name.sql", "094_repository_lux_name_unique.sql", "095_project_key_unique.sql", "096_session_names.sql", "097_session_names_validate.sql", "098_agent_network.sql", "099_tier_effort.sql", "100_image_containers.sql", "101_run_can_run_containers.sql", "102_artifact_descriptions.sql", "103_harness_state.sql", "104_question_items.sql", "105_session_archived.sql", "106_session_model.sql", "107_sessions_tier_idx.sql", "108_directives_resends_idx.sql", "109_brainstorm_anywhere.sql", "110_repository_github_host.sql", "111_task_relations.sql", "112_people_github.sql", "113_run_children.sql", "114_preview_wake_timeouts.sql", "115_agent_coordination.sql", "116_peer_ended.sql", "117_events_agent_message_idx.sql", "118_accounts.sql", "119_github_owner_oauth.sql", "120_agent_machine_moves.sql", "121_pull_request_rerequests.sql", "122_push_per_organization.sql", "123_acceptance_verdicts.sql", "124_task_sources.sql", "125_task_source_mode.sql", "126_fake_done_model.sql", "127_review_range.sql"]);
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
      "098_agent_network.sql",
      "099_tier_effort.sql",
      "100_image_containers.sql",
      "101_run_can_run_containers.sql",
      "102_artifact_descriptions.sql",
      "103_harness_state.sql",
      "104_question_items.sql",
      "105_session_archived.sql",
      "106_session_model.sql",
      "107_sessions_tier_idx.sql",
      "108_directives_resends_idx.sql",
      "109_brainstorm_anywhere.sql",
      "110_repository_github_host.sql",
      "111_task_relations.sql",
      "112_people_github.sql",
      "113_run_children.sql",
      "114_preview_wake_timeouts.sql",
      "115_agent_coordination.sql",
      "116_peer_ended.sql",
      "117_events_agent_message_idx.sql",
      "118_accounts.sql",
      "119_github_owner_oauth.sql",
      "120_agent_machine_moves.sql",
      "121_pull_request_rerequests.sql",
      "122_push_per_organization.sql",
      "123_acceptance_verdicts.sql",
      "124_task_sources.sql",
      "125_task_source_mode.sql",
      "126_fake_done_model.sql",
      "127_review_range.sql",
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
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["094_repository_lux_name_unique.sql", "095_project_key_unique.sql", "096_session_names.sql", "097_session_names_validate.sql", "098_agent_network.sql", "099_tier_effort.sql", "100_image_containers.sql", "101_run_can_run_containers.sql", "102_artifact_descriptions.sql", "103_harness_state.sql", "104_question_items.sql", "105_session_archived.sql", "106_session_model.sql", "107_sessions_tier_idx.sql", "108_directives_resends_idx.sql", "109_brainstorm_anywhere.sql", "110_repository_github_host.sql", "111_task_relations.sql", "112_people_github.sql", "113_run_children.sql", "114_preview_wake_timeouts.sql", "115_agent_coordination.sql", "116_peer_ended.sql", "117_events_agent_message_idx.sql", "118_accounts.sql", "119_github_owner_oauth.sql", "120_agent_machine_moves.sql", "121_pull_request_rerequests.sql", "122_push_per_organization.sql", "123_acceptance_verdicts.sql", "124_task_sources.sql", "125_task_source_mode.sql", "126_fake_done_model.sql", "127_review_range.sql"]);
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
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["095_project_key_unique.sql", "096_session_names.sql", "097_session_names_validate.sql", "098_agent_network.sql", "099_tier_effort.sql", "100_image_containers.sql", "101_run_can_run_containers.sql", "102_artifact_descriptions.sql", "103_harness_state.sql", "104_question_items.sql", "105_session_archived.sql", "106_session_model.sql", "107_sessions_tier_idx.sql", "108_directives_resends_idx.sql", "109_brainstorm_anywhere.sql", "110_repository_github_host.sql", "111_task_relations.sql", "112_people_github.sql", "113_run_children.sql", "114_preview_wake_timeouts.sql", "115_agent_coordination.sql", "116_peer_ended.sql", "117_events_agent_message_idx.sql", "118_accounts.sql", "119_github_owner_oauth.sql", "120_agent_machine_moves.sql", "121_pull_request_rerequests.sql", "122_push_per_organization.sql", "123_acceptance_verdicts.sql", "124_task_sources.sql", "125_task_source_mode.sql", "126_fake_done_model.sql", "127_review_range.sql"]);
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

    expect((await migrate(url, { log: () => {} })).applied).toEqual(["074_task_inline_images.sql", "075_conductor_decisions.sql", "076_runs_conductor_run_idx.sql", "077_conductor_steer.sql", "078_events_run_lands_idx.sql", "079_conductor_github.sql", "080_webhook_repair.sql", "081_preview_secrets.sql", "082_conductor_edits.sql", "083_finding_topic.sql", "084_escalation_questions.sql", "085_brainstorm_role.sql", "086_sessions.sql", "087_session_memories.sql", "088_session_filings_idx.sql", "089_session_functions_parallel.sql", "090_stalled_runs.sql", "091_brainstorm_stuck_turn.sql", "092_retire_completed_runs.sql", "093_lux_name.sql", "094_repository_lux_name_unique.sql", "095_project_key_unique.sql", "096_session_names.sql", "097_session_names_validate.sql", "098_agent_network.sql", "099_tier_effort.sql", "100_image_containers.sql", "101_run_can_run_containers.sql", "102_artifact_descriptions.sql", "103_harness_state.sql", "104_question_items.sql", "105_session_archived.sql", "106_session_model.sql", "107_sessions_tier_idx.sql", "108_directives_resends_idx.sql", "109_brainstorm_anywhere.sql", "110_repository_github_host.sql", "111_task_relations.sql", "112_people_github.sql", "113_run_children.sql", "114_preview_wake_timeouts.sql", "115_agent_coordination.sql", "116_peer_ended.sql", "117_events_agent_message_idx.sql", "118_accounts.sql", "119_github_owner_oauth.sql", "120_agent_machine_moves.sql", "121_pull_request_rerequests.sql", "122_push_per_organization.sql", "123_acceptance_verdicts.sql", "124_task_sources.sql", "125_task_source_mode.sql", "126_fake_done_model.sql", "127_review_range.sql"]);
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
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["096_session_names.sql", "097_session_names_validate.sql", "098_agent_network.sql", "099_tier_effort.sql", "100_image_containers.sql", "101_run_can_run_containers.sql", "102_artifact_descriptions.sql", "103_harness_state.sql", "104_question_items.sql", "105_session_archived.sql", "106_session_model.sql", "107_sessions_tier_idx.sql", "108_directives_resends_idx.sql", "109_brainstorm_anywhere.sql", "110_repository_github_host.sql", "111_task_relations.sql", "112_people_github.sql", "113_run_children.sql", "114_preview_wake_timeouts.sql", "115_agent_coordination.sql", "116_peer_ended.sql", "117_events_agent_message_idx.sql", "118_accounts.sql", "119_github_owner_oauth.sql", "120_agent_machine_moves.sql", "121_pull_request_rerequests.sql", "122_push_per_organization.sql", "123_acceptance_verdicts.sql", "124_task_sources.sql", "125_task_source_mode.sql", "126_fake_done_model.sql", "127_review_range.sql"]);
    // 097 leaves every check 096 added validated, as if made with the table.
    const checks = await sql`SELECT conname, convalidated FROM pg_constraint
      WHERE conrelid = 'sessions'::regclass AND contype = 'c' AND conname LIKE 'sessions_title%' ORDER BY conname`;
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

test("096 validates nothing under its exclusive lock on sessions: its checks are added unvalidated", async () => {
  const { sql } = await migratedBefore("096");
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_n', 'n', 'n')`;
    await sql`INSERT INTO sessions (id, organization_id, title) VALUES ('ssn_1', 'org_n', 'Usage-based billing')`;
    const file = (await listMigrationFiles()).find((f) => f.version === "096")!;
    const contents = await file.contents();
    // As the runner applies it: the whole file in one transaction, observed before it commits.
    const seen = await sql.begin(async (tx) => {
      await tx.unsafe(contents);
      const [lock] = await tx`SELECT count(*)::int AS n FROM pg_locks WHERE relation = 'sessions'::regclass
        AND pid = pg_backend_pid() AND mode = 'AccessExclusiveLock' AND granted`;
      const checks = await tx`SELECT conname, convalidated FROM pg_constraint
        WHERE conrelid = 'sessions'::regclass AND contype = 'c' ORDER BY conname`;
      return { locked: lock.n, checks: [...checks] };
    });
    // A check added without NOT VALID scans every row under this lock and is left convalidated = true.
    expect(seen).toEqual({
      locked: 1,
      checks: [
        { conname: "sessions_title_check", convalidated: false },
        { conname: "sessions_titled_by_check", convalidated: false },
        { conname: "sessions_titled_check", convalidated: false },
      ],
    });
  } finally {
    await sql.end();
  }
}, 120_000);

test("097 validates while another transaction holds a row lock on sessions: it never asks for a lock that blocks writes", async () => {
  const { url, sql } = await migratedBefore("096");
  const holder = new SQL(url);
  let release = () => {};
  let holding: Promise<unknown> = Promise.resolve();
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_n', 'n', 'n')`;
    await sql`INSERT INTO sessions (id, organization_id, title) VALUES ('ssn_1', 'org_n', 'Billing'), ('ssn_2', 'org_n', 'Metering')`;
    const file096 = (await listMigrationFiles()).find((f) => f.version === "096")!;
    const contents = await file096.contents();
    await sql.begin(async (tx) => {
      await tx.unsafe(contents);
      await tx`INSERT INTO schema_migrations (version, name, checksum)
        VALUES (${file096.version}, ${file096.name}, ${createHash("sha256").update(contents).digest("hex")})`;
    });
    // An open write transaction: ROW EXCLUSIVE on sessions until released.
    const updated = Promise.withResolvers<void>();
    const released = new Promise<void>((resolve) => { release = resolve; });
    holding = holder.begin(async (tx) => {
      await tx`UPDATE sessions SET title = 'Billing v2' WHERE id = 'ssn_1'`;
      updated.resolve();
      await released;
    });
    await updated.promise;
    const heldLock = sql`SELECT count(*)::int AS n FROM pg_locks WHERE relation = 'sessions'::regclass
      AND mode = 'RowExclusiveLock' AND granted`;
    expect((await heldLock)[0].n).toBe(1);

    // VALIDATE takes SHARE UPDATE EXCLUSIVE, which does not conflict with ROW EXCLUSIVE; a lock that
    // did (SHARE and up) would wait for the holder. 097 commits on its own, so it is recorded while
    // the holder still writes; later migrations that alter sessions (106) wait for its release. The
    // timer only ends a hang: no duration is asserted.
    const migrating = migrate(url, { log: () => {} });
    migrating.catch(() => {});
    let timer: ReturnType<typeof setTimeout> | undefined;
    let waiting = true;
    const recorded = (async () => {
      while (waiting) {
        const [row] = await sql`SELECT count(*)::int AS n FROM schema_migrations WHERE version = '097'`;
        if (row.n === 1) return "097 applied";
        await Bun.sleep(50);
      }
      return "stopped";
    })();
    const outcome = await Promise.race([
      recorded,
      new Promise<string>((resolve) => { timer = setTimeout(() => resolve("097 still waiting after 30 s"), 30_000); }),
    ]);
    waiting = false;
    clearTimeout(timer);
    expect(outcome).toBe("097 applied");
    expect((await heldLock)[0].n).toBe(1);
    release();
    expect(await migrating.then((r) => r.applied)).toEqual(["097_session_names_validate.sql", "098_agent_network.sql", "099_tier_effort.sql", "100_image_containers.sql", "101_run_can_run_containers.sql", "102_artifact_descriptions.sql", "103_harness_state.sql", "104_question_items.sql", "105_session_archived.sql", "106_session_model.sql", "107_sessions_tier_idx.sql", "108_directives_resends_idx.sql", "109_brainstorm_anywhere.sql", "110_repository_github_host.sql", "111_task_relations.sql", "112_people_github.sql", "113_run_children.sql", "114_preview_wake_timeouts.sql", "115_agent_coordination.sql", "116_peer_ended.sql", "117_events_agent_message_idx.sql", "118_accounts.sql", "119_github_owner_oauth.sql", "120_agent_machine_moves.sql", "121_pull_request_rerequests.sql", "122_push_per_organization.sql", "123_acceptance_verdicts.sql", "124_task_sources.sql", "125_task_source_mode.sql", "126_fake_done_model.sql", "127_review_range.sql"]);
  } finally {
    release();
    await holding.catch(() => {});
    await holder.end();
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
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["097_session_names_validate.sql", "098_agent_network.sql", "099_tier_effort.sql", "100_image_containers.sql", "101_run_can_run_containers.sql", "102_artifact_descriptions.sql", "103_harness_state.sql", "104_question_items.sql", "105_session_archived.sql", "106_session_model.sql", "107_sessions_tier_idx.sql", "108_directives_resends_idx.sql", "109_brainstorm_anywhere.sql", "110_repository_github_host.sql", "111_task_relations.sql", "112_people_github.sql", "113_run_children.sql", "114_preview_wake_timeouts.sql", "115_agent_coordination.sql", "116_peer_ended.sql", "117_events_agent_message_idx.sql", "118_accounts.sql", "119_github_owner_oauth.sql", "120_agent_machine_moves.sql", "121_pull_request_rerequests.sql", "122_push_per_organization.sql", "123_acceptance_verdicts.sql", "124_task_sources.sql", "125_task_source_mode.sql", "126_fake_done_model.sql", "127_review_range.sql"]);
    // 096's checks; 106's are NOT VALID on purpose, as 103's.
    expect([...await sql`SELECT conname FROM pg_constraint
      WHERE conrelid = 'sessions'::regclass AND contype = 'c' AND NOT convalidated AND conname LIKE 'sessions_title%'`]).toEqual([]);
  } finally {
    await sql.end();
  }
}, 120_000);

test("106 validates nothing under its exclusive lock on sessions: its foreign key and check are added unvalidated", async () => {
  const { sql } = await migratedBefore("106");
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_n', 'n', 'n')`;
    await sql`INSERT INTO sessions (id, organization_id, title) VALUES ('ssn_1', 'org_n', 'Usage-based billing')`;
    const file = (await listMigrationFiles()).find((f) => f.version === "106")!;
    const contents = await file.contents();
    // As the runner applies it: the whole file in one transaction, observed before it commits.
    const seen = await sql.begin(async (tx) => {
      await tx.unsafe(contents);
      const [lock] = await tx`SELECT count(*)::int AS n FROM pg_locks WHERE relation = 'sessions'::regclass
        AND pid = pg_backend_pid() AND mode = 'AccessExclusiveLock' AND granted`;
      const constraints = await tx`SELECT conname, convalidated FROM pg_constraint
        WHERE conrelid = 'sessions'::regclass AND conname IN ('sessions_tier_fkey', 'sessions_harness_check') ORDER BY conname`;
      return { locked: lock.n, constraints: [...constraints] };
    });
    // A constraint added without NOT VALID scans every row under this lock and is left convalidated = true.
    expect(seen).toEqual({
      locked: 1,
      constraints: [
        { conname: "sessions_harness_check", convalidated: false },
        { conname: "sessions_tier_fkey", convalidated: false },
      ],
    });
  } finally {
    await sql.end();
  }
}, 120_000);

test("091 stores an open call's time at +00:00 on a database whose zone is not UTC", async () => {
  const { url, sql } = await migratedBefore("091");
  try {
    // New sessions on this database default to Asia/Kolkata (+05:30); this one already began in UTC.
    await sql.unsafe(`ALTER DATABASE "${new URL(url).pathname.slice(1)}" SET timezone TO 'Asia/Kolkata'`);
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_z', 'z', 'z')`;
    await sql`INSERT INTO sessions (id, organization_id, title) VALUES ('ssn_z', 'org_z', 'z')`;
    await sql`INSERT INTO runs (id, organization_id, session_id, attempt, role, status, open_tool_calls, started_at)
      VALUES ('run_z', 'org_z', 'ssn_z', 1, 'brainstorm', 'running', ARRAY['call_1'], '2026-10-09 12:00:00+00')`;
    expect((await migrate(url, { log: () => {} })).applied[0]).toBe("091_brainstorm_stuck_turn.sql");
    const [run] = await sql`SELECT open_tool_calls_at::text AS at FROM runs WHERE id = 'run_z'`;
    expect(run.at).toBe(`{"call_1": "2026-10-09T12:00:00+00:00"}`);
  } finally {
    await sql.end();
  }
}, 120_000);

test("107 leaves a valid index on the sessions that chose a tier, and only those", async () => {
  const url = await createDatabase();
  const migrated = run([binary], { DATABASE_URL: url });
  expect(migrated.code, migrated.out).toBe(0);
  const sql = new SQL(url);
  try {
    const index = await sql`SELECT i.indisvalid AS valid, pg_get_expr(i.indpred, i.indrelid) AS predicate,
      pg_get_indexdef(i.indexrelid, 1, true) AS key FROM pg_index i WHERE i.indexrelid = 'sessions_tier_idx'::regclass AND i.indrelid = 'sessions'::regclass`;
    expect([...index]).toEqual([{ valid: true, predicate: "(tier IS NOT NULL)", key: "tier" }]);
  } finally {
    await sql.end();
  }
}, 120_000);

test("108 leaves a valid index on the directives that resend another, and only those, outside a transaction", async () => {
  const file = (await listMigrationFiles()).find((f) => f.version === "108");
  expect(file && outsideTransaction(await file.contents())).toBe(true);
  const url = await createDatabase();
  const migrated = run([binary], { DATABASE_URL: url });
  expect(migrated.code, migrated.out).toBe(0);
  const sql = new SQL(url);
  try {
    const index = await sql`SELECT i.indisvalid AS valid, pg_get_expr(i.indpred, i.indrelid) AS predicate,
      pg_get_indexdef(i.indexrelid, 1, true) AS key FROM pg_index i WHERE i.indexrelid = 'directives_resends_idx'::regclass AND i.indrelid = 'directives'::regclass`;
    expect([...index]).toEqual([{ valid: true, predicate: "(resends IS NOT NULL)", key: "resends" }]);
  } finally {
    await sql.end();
  }
}, 120_000);

test("110 stores a repository at https://www.github.com/ at https://github.com/, and leaves every other URL as it was", async () => {
  const { url, sql } = await migratedBefore("110");
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_g', 'g', 'g')`;
    await sql`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_g', 'org_g', 'G', 'g', 'G')`;
    await sql`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES
      ('repo_www', 'org_g', 'prj_g', 'billing', 'https://www.github.com/acme/billing'),
      ('repo_case', 'org_g', 'prj_g', 'sdk', 'https://WWW.GitHub.com/acme/sdk.git'),
      ('repo_plain', 'org_g', 'prj_g', 'web', 'https://github.com/acme/web'),
      ('repo_other', 'org_g', 'prj_g', 'co', 'https://www.github.company.com/a/b'),
      ('repo_ssh', 'org_g', 'prj_g', 'ssh', 'git@www.github.com:a/b.git')`;
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["110_repository_github_host.sql", "111_task_relations.sql", "112_people_github.sql", "113_run_children.sql", "114_preview_wake_timeouts.sql", "115_agent_coordination.sql", "116_peer_ended.sql", "117_events_agent_message_idx.sql", "118_accounts.sql", "119_github_owner_oauth.sql", "120_agent_machine_moves.sql", "121_pull_request_rerequests.sql", "122_push_per_organization.sql", "123_acceptance_verdicts.sql", "124_task_sources.sql", "125_task_source_mode.sql", "126_fake_done_model.sql", "127_review_range.sql"]);
    const rows = await sql`SELECT id, url FROM repositories ORDER BY id`;
    expect([...rows]).toEqual([
      { id: "repo_case", url: "https://github.com/acme/sdk.git" },
      { id: "repo_other", url: "https://www.github.company.com/a/b" },
      { id: "repo_plain", url: "https://github.com/acme/web" },
      { id: "repo_ssh", url: "git@www.github.com:a/b.git" },
      { id: "repo_www", url: "https://github.com/acme/billing" },
    ]);
  } finally {
    await sql.end();
  }
}, 120_000);

test("111 adds task_relations and task_auto_starts under forced RLS, and task_blocked_by releases a blocker only once it merged", async () => {
  const { url, sql } = await migratedBefore("111");
  try {
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["111_task_relations.sql", "112_people_github.sql", "113_run_children.sql", "114_preview_wake_timeouts.sql", "115_agent_coordination.sql", "116_peer_ended.sql", "117_events_agent_message_idx.sql", "118_accounts.sql", "119_github_owner_oauth.sql", "120_agent_machine_moves.sql", "121_pull_request_rerequests.sql", "122_push_per_organization.sql", "123_acceptance_verdicts.sql", "124_task_sources.sql", "125_task_source_mode.sql", "126_fake_done_model.sql", "127_review_range.sql"]);
    const rls = await sql`SELECT relname, relrowsecurity AS enabled, relforcerowsecurity AS forced FROM pg_class
      WHERE relname IN ('task_relations', 'task_auto_starts') AND relkind = 'r' ORDER BY relname`;
    expect([...rls]).toEqual([
      { relname: "task_auto_starts", enabled: true, forced: true },
      { relname: "task_relations", enabled: true, forced: true },
    ]);

    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_r', 'r', 'r')`;
    await sql`INSERT INTO people (id, organization_id, name) VALUES ('per_r', 'org_r', 'Ana')`;
    await sql`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_r', 'org_r', 'CP', 'cp', 'CP')`;
    await sql`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_r', 'org_r', 'prj_r', 'cp', 'https://github.com/acme/cp')`;
    await sql`INSERT INTO tasks (id, organization_id, project_id, number, title, status) VALUES
      ('wi_merged', 'org_r', 'prj_r', 41, 'Meter', 'done'),
      ('wi_unmerged', 'org_r', 'prj_r', 37, 'Spike', 'done'),
      ('wi_review', 'org_r', 'prj_r', 43, 'Sync', 'review'),
      ('wi_blocked', 'org_r', 'prj_r', 44, 'Billing page', 'received')`;
    await sql`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch,
        base_branch, head_sha, title, state)
      VALUES ('pr_merged', 'org_r', 'prj_r', 'wi_merged', 'repo_r', 431, 'u', 'h', 'main', 's', 't', 'merged'),
        ('pr_closed', 'org_r', 'prj_r', 'wi_unmerged', 'repo_r', 432, 'u', 'h2', 'main', 's', 't', 'closed')`;
    await sql`INSERT INTO task_relations (organization_id, from_task_id, to_task_id, kind, created_by_person) VALUES
      ('org_r', 'wi_merged', 'wi_blocked', 'blocks', 'per_r'),
      ('org_r', 'wi_unmerged', 'wi_blocked', 'blocks', 'per_r'),
      ('org_r', 'wi_review', 'wi_blocked', 'blocks', 'per_r')`;

    const open = await sql`SELECT key, status FROM task_blocked_by('wi_blocked')`;
    expect([...open]).toEqual([{ key: "CP-37", status: "done" }, { key: "CP-43", status: "review" }]);
  } finally {
    await sql.end();
  }
}, 120_000);

test("112 adds a person's GitHub login, id and commit email, empty, without rewriting people", async () => {
  const { url, sql } = await migratedBefore("111");
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_p', 'p', 'p')`;
    await sql`INSERT INTO people (id, organization_id, name, email) VALUES ('per_a', 'org_p', 'Ana', 'Ana@Example.com')`;
    const [before] = await sql`SELECT pg_relation_filenode('people') AS node`;
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["111_task_relations.sql", "112_people_github.sql", "113_run_children.sql", "114_preview_wake_timeouts.sql", "115_agent_coordination.sql", "116_peer_ended.sql", "117_events_agent_message_idx.sql", "118_accounts.sql", "119_github_owner_oauth.sql", "120_agent_machine_moves.sql", "121_pull_request_rerequests.sql", "122_push_per_organization.sql", "123_acceptance_verdicts.sql", "124_task_sources.sql", "125_task_source_mode.sql", "126_fake_done_model.sql", "127_review_range.sql"]);
    const [after] = await sql`SELECT pg_relation_filenode('people') AS node`;
    expect(after.node).toBe(before.node);
    expect([...await sql`SELECT github_login, github_user_id, commit_email FROM people`])
      .toEqual([{ github_login: null, github_user_id: null, commit_email: null }]);
    // commit_email compares as email does, ignoring case.
    await sql`UPDATE people SET commit_email = 'Ana@Users.Example' WHERE id = 'per_a'`;
    expect([...await sql`SELECT id FROM people WHERE commit_email = 'ana@users.example'`]).toEqual([{ id: "per_a" }]);
  } finally {
    await sql.end();
  }
}, 120_000);

test("a database main left at 114 takes 115 to 117, keeps its relations, and task_peers stays on the epic", async () => {
  const { url, sql } = await migratedBefore("115");
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_u', 'u', 'u')`;
    await sql`INSERT INTO people (id, organization_id, name) VALUES ('per_u', 'org_u', 'Ana')`;
    await sql`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_u', 'org_u', 'U', 'u', 'U')`;
    await sql`INSERT INTO epics (id, organization_id, project_id, title) VALUES ('epc_u', 'org_u', 'prj_u', 'E')`;
    await sql`INSERT INTO tasks (id, organization_id, project_id, epic_id, number, title) VALUES
      ('wi_a', 'org_u', 'prj_u', 'epc_u', 1, 'A'), ('wi_b', 'org_u', 'prj_u', 'epc_u', 2, 'B'),
      ('wi_off', 'org_u', 'prj_u', NULL, 3, 'Off')`;
    // Edges both ways between an epic task and one off the epic: none makes a peer.
    await sql`INSERT INTO task_relations (organization_id, from_task_id, to_task_id, kind, created_by_person) VALUES
      ('org_u', 'wi_off', 'wi_a', 'blocks', 'per_u'), ('org_u', 'wi_a', 'wi_off', 'related', 'per_u')`;

    expect((await migrate(url, { log: () => {} })).applied).toEqual(["115_agent_coordination.sql", "116_peer_ended.sql", "117_events_agent_message_idx.sql", "118_accounts.sql", "119_github_owner_oauth.sql", "120_agent_machine_moves.sql", "121_pull_request_rerequests.sql", "122_push_per_organization.sql", "123_acceptance_verdicts.sql", "124_task_sources.sql", "125_task_source_mode.sql", "126_fake_done_model.sql", "127_review_range.sql"]);
    expect((await recorded(url)).map((m) => m.name).slice(-14)).toEqual(["114_preview_wake_timeouts.sql",
      "115_agent_coordination.sql", "116_peer_ended.sql", "117_events_agent_message_idx.sql", "118_accounts.sql", "119_github_owner_oauth.sql", "120_agent_machine_moves.sql", "121_pull_request_rerequests.sql", "122_push_per_organization.sql", "123_acceptance_verdicts.sql", "124_task_sources.sql", "125_task_source_mode.sql", "126_fake_done_model.sql", "127_review_range.sql"]);
    expect([...await sql`SELECT from_task_id, to_task_id, kind::text FROM task_relations ORDER BY kind`]).toEqual([
      { from_task_id: "wi_off", to_task_id: "wi_a", kind: "blocks" },
      { from_task_id: "wi_a", to_task_id: "wi_off", kind: "related" },
    ]);
    const peers = async (id: string) => (await sql`SELECT peer_id, via FROM task_peers(${id}) ORDER BY peer_id`).map((r: object) => ({ ...r }));
    expect(await peers("wi_a")).toEqual([{ peer_id: "wi_b", via: "epic" }]);
    expect(await peers("wi_off")).toEqual([]);
    const [index] = await sql`SELECT i.indisvalid AS valid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
      WHERE c.relname = 'events_agent_message_idx'`;
    expect(index).toEqual({ valid: true });
    // 116 rebuilds the tier model check whole: it keeps 113's subagent models beside fake/peer.
    const tier = (id: string, model: string) =>
      sql`INSERT INTO model_tiers (id, organization_id, name, model) VALUES (${id}, 'org_u', ${id}, ${model})`;
    for (const [id, model] of [["t_sub", "fake/subagent"], ["t_ask", "fake/subagent-ask"], ["t_peer", "fake/peer"]]) await tier(id!, model!);
    expect(await tier("t_bad", "fake/nonesuch").then(() => "inserted", () => "refused")).toBe("refused");
  } finally {
    await sql.end();
  }
}, 120_000);

test("109 rewrites no organisation and leaves every one's brainstorms on its list", async () => {
  const { sql } = await migratedBefore("109");
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_b', 'b', 'b')`;
    const [before] = await sql`SELECT pg_relation_filenode('organizations') AS node`;
    const contents = await (await listMigrationFiles()).find((f) => f.version === "109")!.contents();
    await sql.begin((tx) => tx.unsafe(contents));
    // A rewrite (a volatile default, or a type change) gives the table a new file.
    const [after] = await sql`SELECT pg_relation_filenode('organizations') AS node`;
    expect(after.node).toBe(before.node);
    expect([...await sql`SELECT id, brainstorm_anywhere FROM organizations`]).toEqual([{ id: "org_b", brainstorm_anywhere: false }]);
  } finally {
    await sql.end();
  }
}, 120_000);

test("120 widens the pause and resume checks to a machine move without scanning runs: they bind new rows, unvalidated", async () => {
  const { url, sql } = await migratedBefore("120");
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_m', 'm', 'm')`;
    await sql`INSERT INTO sessions (id, organization_id, title) VALUES ('ssn_m', 'org_m', 'm')`;
    await sql`INSERT INTO runs (id, organization_id, session_id, attempt, role, status, dude_pause)
      VALUES ('run_m', 'org_m', 'ssn_m', 1, 'brainstorm', 'paused', 'idle')`;
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["120_agent_machine_moves.sql", "121_pull_request_rerequests.sql", "122_push_per_organization.sql", "123_acceptance_verdicts.sql", "124_task_sources.sql", "125_task_source_mode.sql", "126_fake_done_model.sql", "127_review_range.sql"]);
    await sql`UPDATE runs SET dude_pause = 'machine' WHERE id = 'run_m'`;
    expect((await sql`SELECT dude_pause FROM runs WHERE id = 'run_m'`)[0].dude_pause).toBe("machine");
    const refused = await sql`UPDATE runs SET dude_pause = 'elsewhere' WHERE id = 'run_m'`.then(() => "", (e: Error) => e.message);
    expect(refused).toContain("runs_dude_pause_check");
    const checks = await sql`SELECT conname, convalidated FROM pg_constraint
      WHERE conname IN ('runs_dude_pause_check', 'run_resumes_cause_check') ORDER BY conname`;
    expect([...checks]).toEqual([
      { conname: "run_resumes_cause_check", convalidated: false },
      { conname: "runs_dude_pause_check", convalidated: false },
    ]);
    // 120 rebuilds the tier model check whole: it keeps 116's fake/peer and 113's subagent models beside fake/machine.
    const tier = (id: string, model: string) =>
      sql`INSERT INTO model_tiers (id, organization_id, name, model) VALUES (${id}, 'org_m', ${id}, ${model})`;
    for (const [id, model] of [["t_sub", "fake/subagent"], ["t_ask", "fake/subagent-ask"], ["t_peer", "fake/peer"], ["t_machine", "fake/machine"]]) await tier(id!, model!);
    // 123 adds the scripted tester's outcomes, keeping the rest.
    for (const [id, model] of [["t_fail", "fake/test-fail"], ["t_blocked", "fake/test-blocked"], ["t_silent", "fake/test-silent"]]) await tier(id!, model!);
    expect(await tier("t_bad", "fake/nonesuch").then(() => "inserted", () => "refused")).toBe("refused");
  } finally {
    await sql.end();
  }
}, 120_000);

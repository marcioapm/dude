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
import { type AddressInfo, connect, createServer, type Socket } from "node:net";
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

test("212 takes incoming triggers, finds a hook across organisations, and keeps no body on a refused delivery", async () => {
  const { url, sql } = await migratedBefore("212");
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_a', 'Acme', 'acme'), ('org_b', 'Other', 'other')`;
    await sql`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_a', 'org_a', 'platform', 'platform', 'PLAT')`;
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["212_incoming_webhooks.sql", "213_chat_link_offers.sql",
      "214_chat_request_failure.sql", "215_chat_request_failure_backfill.sql", "216_attachment_storage_keys.sql", "217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
    const refusal = async (q: () => Promise<unknown>) => q().then(() => "", (err: Error) => err.message);
    const incoming = { kind: "incoming", hookId: "hk_abcdefghijkl0123", verify: { mode: "header", header: "Authorization" }, ratePerHour: 12 };
    await sql`INSERT INTO automations (id, organization_id, project_id, name, kind, trigger, agent, incoming_secret, incoming_hint, project_ids)
      VALUES ('aut_in', 'org_a', NULL, 'In', 'agent', ${incoming}::jsonb, ${{ own: { prompt: "" } }}::jsonb, 'whsec_x1234', '1234', '{prj_a}')`;
    // The same hook twice, a hook of the wrong shape, and projects on a project automation are refused.
    expect(await refusal(async () => await sql`INSERT INTO automations (id, organization_id, name, kind, trigger, agent)
      VALUES ('aut_dup', 'org_b', 'Dup', 'agent', ${incoming}::jsonb, ${{ own: { prompt: "" } }}::jsonb)`)).toContain("automations_hook_idx");
    expect(await refusal(async () => await sql`INSERT INTO automations (id, organization_id, name, kind, trigger, agent)
      VALUES ('aut_bad', 'org_a', 'Bad', 'agent', ${{ kind: "incoming", hookId: "nope" }}::jsonb, ${{ own: { prompt: "" } }}::jsonb)`)).toContain("automations_incoming_hook");
    expect(await refusal(async () => await sql`INSERT INTO automations (id, organization_id, project_id, name, kind, trigger, agent, project_ids)
      VALUES ('aut_p', 'org_a', 'prj_a', 'P', 'agent', ${{ ...incoming, hookId: "hk_000000000000aaaa" }}::jsonb, ${{ own: { prompt: "" } }}::jsonb, '{prj_a}')`))
      .toContain("automations_project_ids_org_incoming");

    // The public route reads a hook before it knows the organisation, as dude_app with no tenant set.
    const hook = await sql.begin(async (tx) => {
      await tx`SET LOCAL ROLE dude_app`;
      return [...await tx`SELECT * FROM incoming_hook('hk_abcdefghijkl0123')`];
    });
    expect(hook).toEqual([{ automation_id: "aut_in", organization_id: "org_a", enabled: false,
      verify: { mode: "header", header: "Authorization" }, secret: "whsec_x1234", rate_per_hour: 12 }]);

    expect(await refusal(async () => await sql`INSERT INTO incoming_deliveries (id, organization_id, automation_id, verdict, reason, bytes, payload)
      VALUES ('dlv_r', 'org_a', 'aut_in', 'refused', 'bad_signature', 10, '{"a":1}')`)).toContain("incoming_deliveries_refused_bodyless");
    expect(await refusal(async () => await sql`INSERT INTO incoming_deliveries (id, organization_id, automation_id, verdict, reason, bytes)
      VALUES ('dlv_r', 'org_a', 'aut_in', 'refused', 'nope', 10)`)).toContain("incoming_deliveries_refusal_code");
    await sql`INSERT INTO incoming_deliveries (id, organization_id, automation_id, verdict, reason, bytes)
      VALUES ('dlv_r', 'org_a', 'aut_in', 'refused', 'bad_signature', 10)`;
    // Tenant isolation.
    const seen = await sql.begin(async (tx) => {
      await tx`SET LOCAL ROLE dude_app`;
      await tx`SELECT set_config('app.organization_id', 'org_b', true)`;
      return [...await tx`SELECT id FROM incoming_deliveries`];
    });
    expect(seen).toEqual([]);
  } finally {
    await sql.end();
  }
}, 120_000);

test("214 adds a failed request's cause and what it made, empty on every row, each bounded", async () => {
  const { sql } = await migratedBefore("214");
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_a', 'Acme', 'acme')`;
    await sql`INSERT INTO chat_requests (id, organization_id, provider, event_id, external_workspace_id, conversation_id, external_user_id, route, reasoning)
      VALUES ('chr_old', 'org_a', 'slack', 'chr_old', 'T1', 'C1', 'U1', 'failed', 'Slack did not answer after 6 tries')`;
    expect(await applyBefore(sql, "215")).toEqual(["214_chat_request_failure.sql"]);
    expect([...await sql`SELECT id, failure FROM chat_requests`]).toEqual([{ id: "chr_old", failure: null }]);
    const refusal = async (q: () => Promise<unknown>) => q().then(() => "", (err: Error) => err.message);
    expect(await refusal(async () => await sql`UPDATE chat_requests SET failure = ${"x".repeat(2000)} WHERE id = 'chr_old'`)).toBe("");
    expect(await refusal(async () => await sql`UPDATE chat_requests SET failure = ${"x".repeat(2001)} WHERE id = 'chr_old'`))
      .toContain("chat_requests_failure_check");
    // made_route is a route that makes something, or nothing.
    for (const ok of ["brainstorm", "task", "epic", "resume", "task_link", null]) {
      expect(await refusal(async () => await sql`UPDATE chat_requests SET made_route = ${ok} WHERE id = 'chr_old'`)).toBe("");
    }
    for (const bad of ["failed", "clarify", "not_member"]) {
      expect(await refusal(async () => await sql`UPDATE chat_requests SET made_route = ${bad} WHERE id = 'chr_old'`))
        .toContain("chat_requests_made_route_check");
    }
  } finally {
    await sql.end();
  }
}, 120_000);

test("215 gives each failed request of the last 30 days its cause apart from the model's reasoning, and none to the rest", async () => {
  const { url, sql } = await migratedBefore("215");
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_a', 'Acme', 'acme')`;
    const rows: Array<[string, string, string]> = [
      // Stored unredacted before 214: never copied.
      ["chr_route", "failed", "the routing call failed: the LLM proxy answered 400: bad request"],
      ["chr_route_reply", "failed", "the routing call failed: the LLM proxy answered 400: sk-x (its reply was not posted: slack chat.postMessage: invalid_auth)"],
      ["chr_gave", "failed", "dude could not act on it: slack conversations.info: missing_scope"],
      ["chr_gave_pg", "failed", "dude could not act on it: record: ERROR: x (SQLSTATE 23505)"],
      ["chr_tries", "failed", "Slack did not answer after 5 tries"],
      ["chr_reply", "failed", "A clear task. (its reply was not posted: slack chat.postMessage: invalid_blocks (blocks[0].text))"],
      ["chr_reply_gave", "failed", "A clear task. (its reply was not posted: Slack did not answer after 6 tries)"],
      ["chr_make", "failed", `An epic of three (with parts). (ERROR: insert or update on table "tasks" violates foreign key constraint "tasks_epic_id_fkey" (SQLSTATE 23503))`],
      ["chr_make_twice", "failed", `A task. (ERROR: a (SQLSTATE 23503)) (ERROR: b (SQLSTATE 23505))`],
      // The model's words and an unknown clause: nothing to copy.
      ["chr_words", "failed", "A task (project gone) (no such project)"],
      ["chr_ok", "task", "A clear task (with parentheses)"],
      ["chr_empty", "failed", ""],
      ["chr_aged", "failed", "Slack did not answer after 6 tries"],
    ];
    for (const [id, route, reasoning] of rows) {
      await sql`INSERT INTO chat_requests (id, organization_id, provider, event_id, external_workspace_id, conversation_id, external_user_id, route, reasoning)
        VALUES (${id}, 'org_a', 'slack', ${id}, 'T1', 'C1', 'U1', ${route}, ${reasoning})`;
    }
    // Older than retention keeps: left as it was.
    await sql`UPDATE chat_requests SET created_at = now() - interval '31 days' WHERE id = 'chr_aged'`;
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["215_chat_request_failure_backfill.sql",
      "216_attachment_storage_keys.sql",
      "217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
    const failures = Object.fromEntries((await sql`SELECT id, failure FROM chat_requests ORDER BY id`)
      .map((r: { id: string; failure: string | null }) => [r.id, r.failure]));
    expect(failures).toEqual({
      chr_route: null,
      chr_route_reply: null,
      chr_gave: "dude could not act on it: slack conversations.info: missing_scope",
      chr_gave_pg: "dude could not act on it: record: ERROR: x (SQLSTATE 23505)",
      chr_tries: "Slack did not answer after 5 tries",
      chr_reply: "its reply was not posted: slack chat.postMessage: invalid_blocks (blocks[0].text)",
      chr_reply_gave: "its reply was not posted: Slack did not answer after 6 tries",
      chr_make: `ERROR: insert or update on table "tasks" violates foreign key constraint "tasks_epic_id_fkey" (SQLSTATE 23503)`,
      chr_make_twice: "ERROR: b (SQLSTATE 23505)",
      chr_words: null,
      chr_ok: null,
      chr_empty: null,
      chr_aged: null,
    });
    for (const f of Object.values(failures)) {
      expect(String(f)).not.toMatch(/A clear task|An epic of three|A task|sk-x/);
    }
    // What a failed row made, only where its result and asked route say it alone.
    const made: Array<[string, string, string | null, string | null, string | null]> = [
      ["chr_m_fallback", "failed", null, "session", "brainstorm"],
      ["chr_m_brainstorm", "failed", "brainstorm", "session", "brainstorm"],
      ["chr_m_resume", "failed", "resume", "session", null],
      ["chr_m_task", "failed", "task", "task", "task"],
      ["chr_m_epic", "failed", "epic", "epic", "epic"],
      ["chr_m_link_task", "failed", "task_link", "task", "task_link"],
      ["chr_m_link_epic", "failed", "task_link", "epic", "task_link"],
      ["chr_m_none", "failed", "task", null, null],
      ["chr_m_fine", "task", null, "task", null],
    ];
    await sql`UPDATE chat_requests SET failure = NULL, made_route = NULL`;
    await sql`DELETE FROM schema_migrations WHERE version = '215'`;
    for (const [id, route, asked, kind] of made) {
      await sql`INSERT INTO chat_requests (id, organization_id, provider, event_id, external_workspace_id, conversation_id, external_user_id,
          route, asked_route, result_kind, result_id, reasoning)
        VALUES (${id}, 'org_a', 'slack', ${id}, 'T1', 'C1', 'U1', ${route}, ${asked}, ${kind}, ${kind ? `x_${id}` : null}, 'x')`;
    }
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["215_chat_request_failure_backfill.sql"]);
    expect(Object.fromEntries((await sql`SELECT id, made_route FROM chat_requests WHERE starts_with(id, 'chr_m_')`)
      .map((r: { id: string; made_route: string | null }) => [r.id, r.made_route])))
      .toEqual(Object.fromEntries(made.map(([id, , , , want]) => [id, want])));
  } finally {
    await sql.end();
  }
}, 120_000);

test("217 keys an automation run's activity to it, alone, moves the tool server's old rows, and purges them with the log", async () => {
  const { url, sql } = await migratedBefore("217");
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_a', 'Acme', 'acme'), ('org_b', 'Other', 'other')`;
    await sql`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_a', 'org_a', 'platform', 'platform', 'PLAT')`;
    await sql`INSERT INTO automations (id, organization_id, name, kind, trigger, agent)
      VALUES ('aut_a', 'org_a', 'Digest', 'agent', ${{ kind: "schedule", cron: "0 15 * * 5", tz: "Europe/Lisbon" }}::jsonb, ${{ own: { prompt: "" } }}::jsonb)`;
    await sql`INSERT INTO automation_runs (id, organization_id, automation_id, status, source, log_tail, created_at) VALUES
      ('arn_old', 'org_a', 'aut_a', 'succeeded', 'schedule', 'done', now() - interval '100 days'),
      ('arn_new', 'org_a', 'aut_a', 'running', 'schedule', 'so far', now())`;
    // What the tool server wrote before: a call's record under run_id, and create_task's task.created on its task.
    await sql`INSERT INTO events (id, organization_id, event_type, run_id, task_id, actor_type, actor_id, source, payload) VALUES
      ('evt_call', 'org_a', 'agent.tool.dude', 'arn_old', NULL, 'agent', 'arn_old', 'control-plane', '{"tool":"post_to_chat"}'),
      ('evt_task', 'org_a', 'task.created', 'arn_old', 'tsk_1', 'agent', 'arn_old', 'control-plane', '{}')`;
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
    expect([...await sql`SELECT id, run_id, task_id, automation_run_id FROM events ORDER BY id`]).toEqual([
      { id: "evt_call", run_id: null, task_id: null, automation_run_id: "arn_old" },
      { id: "evt_task", run_id: null, task_id: "tsk_1", automation_run_id: null },
    ]);
    const refusal = async (q: () => Promise<unknown>) => q().then(() => "", (err: Error) => err.message);
    for (const other of ["run_id", "session_id", "task_id"]) {
      expect(await refusal(async () => await sql.unsafe(`INSERT INTO events (id, organization_id, event_type, automation_run_id, ${other},
        actor_type, actor_id, source) VALUES ('evt_x', 'org_a', 'agent.message', 'arn_new', 'x', 'agent', 'a', 'runner')`))).toContain("events_automation_run_scope");
    }
    expect(await refusal(async () => await sql`INSERT INTO events (id, organization_id, event_type, automation_run_id, actor_type, actor_id, source)
      VALUES ('evt_y', 'org_a', 'agent.message', 'arn_none', 'agent', 'a', 'runner')`)).toContain("events_automation_run_fk");
    await sql`INSERT INTO events (id, organization_id, event_type, automation_run_id, actor_type, actor_id, source) VALUES
      ('evt_live', 'org_a', 'agent.message', 'arn_new', 'agent', 'arn_new', 'runner')`;
    expect([...await sql`SELECT indexdef FROM pg_indexes WHERE indexname = 'events_automation_run_cursor_idx'`]).toHaveLength(1);
    // Another organisation's reader sees none of it.
    const seen = await sql.begin(async (tx) => {
      await tx`SET LOCAL ROLE dude_app`;
      await tx`SELECT set_config('app.organization_id', 'org_b', true)`;
      return [...await tx`SELECT id FROM events WHERE automation_run_id IS NOT NULL`];
    });
    expect(seen).toEqual([]);
    // Retention, as the sweeper: only the old run's rows, which it may not delete directly.
    expect(await refusal(async () => await sql.begin(async (tx) => {
      await tx`SET LOCAL ROLE dude_sweeper`;
      await tx`DELETE FROM events WHERE automation_run_id = 'arn_old'`;
    }))).toContain("permission denied");
    const purge = () => sql.begin(async (tx) => {
      await tx`SET LOCAL ROLE dude_sweeper`;
      return Number([...await tx`SELECT purge_automation_run_events(now() - interval '90 days', 100) AS n`][0].n);
    });
    // One activity row deleted and its run marked empty; the next pass has nothing due.
    expect(await purge()).toBe(2);
    expect(await purge()).toBe(0);
    expect([...await sql`SELECT id, activity_purged_at IS NOT NULL AS purged FROM automation_runs ORDER BY id`])
      .toEqual([{ id: "arn_new", purged: false }, { id: "arn_old", purged: true }]);
    expect([...await sql`SELECT id FROM events WHERE automation_run_id IS NOT NULL ORDER BY id`]).toEqual([{ id: "evt_live" }]);
    // Deleting the automation takes its runs' activity with it.
    await sql`DELETE FROM automations WHERE id = 'aut_a'`;
    expect([...await sql`SELECT id FROM events ORDER BY id`]).toEqual([{ id: "evt_task" }]);
  } finally {
    await sql.end();
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

    expect((await migrate(url, { log: () => {} })).applied).toEqual(["076_runs_conductor_run_idx.sql", "077_conductor_steer.sql", "078_events_run_lands_idx.sql", "079_conductor_github.sql", "080_webhook_repair.sql", "081_preview_secrets.sql", "082_conductor_edits.sql", "083_finding_topic.sql", "084_escalation_questions.sql", "085_brainstorm_role.sql", "086_sessions.sql", "087_session_memories.sql", "088_session_filings_idx.sql", "089_session_functions_parallel.sql", "090_stalled_runs.sql", "091_brainstorm_stuck_turn.sql", "092_retire_completed_runs.sql", "093_lux_name.sql", "094_repository_lux_name_unique.sql", "095_project_key_unique.sql", "096_session_names.sql", "097_session_names_validate.sql", "098_agent_network.sql", "099_tier_effort.sql", "100_image_containers.sql", "101_run_can_run_containers.sql", "102_artifact_descriptions.sql", "103_harness_state.sql", "104_question_items.sql", "105_session_archived.sql", "106_session_model.sql", "107_sessions_tier_idx.sql", "108_directives_resends_idx.sql", "109_brainstorm_anywhere.sql", "110_repository_github_host.sql", "111_task_relations.sql", "112_people_github.sql", "113_run_children.sql", "114_preview_wake_timeouts.sql", "115_agent_coordination.sql", "116_peer_ended.sql", "117_events_agent_message_idx.sql", "118_accounts.sql", "119_github_owner_oauth.sql", "120_agent_machine_moves.sql", "121_pull_request_rerequests.sql", "122_push_per_organization.sql", "123_acceptance_verdicts.sql", "124_task_sources.sql", "125_task_source_mode.sql", "126_fake_done_model.sql", "127_feedback.sql", "128_project_secret_recipients.sql", "129_run_project_secrets.sql", "130_run_secret_values.sql", "131_fake_secret_models.sql", "132_run_synced_bases.sql", "133_feedback_pictures_kept.sql", "134_review_range.sql", "135_events_session_github_reads_idx.sql", "136_events_task_github_reads_idx.sql", "137_events_fix_merge_attempted_idx.sql", "138_events_git_merge_attempted_idx.sql", "139_run_retry_backoff.sql", "140_tasks_id_org_idx.sql", "141_model_tiers_id_org_idx.sql", "142_model_calls.sql", "143_checks_pending_since.sql", "144_image_layers.sql", "145_repository_setup_script.sql", "146_run_sync_history.sql", "147_events_session_github_reads_idx.sql", "148_events_session_github_reads_idx.sql", "149_events_task_github_reads_idx.sql", "150_events_task_github_reads_idx.sql", "151_recover_github_read_indexes.sql", "152_child_admission.sql", "153_child_results.sql", "154_child_transport.sql", "155_child_output_fence.sql", "156_child_results_child_idx.sql", "157_task_lifecycle.sql", "158_task_lifecycle_rules.sql", "159_session_attachments.sql", "160_session_child_results.sql", "161_runs_session_creation_key_idx.sql", "162_recover_runs_session_creation_key_idx.sql", "163_events_session_github_reads_v2_idx.sql", "164_events_task_github_reads_v2_idx.sql", "165_drop_events_session_github_reads_idx.sql", "166_drop_events_task_github_reads_idx.sql", "167_recover_github_read_v2_indexes.sql", "168_events_run_chat_idx.sql", "169_recover_events_run_chat_idx.sql", "170_child_results_verify.sql", "171_run_web.sql", "172_fake_checks_models.sql", "173_run_continues.sql", "174_finding_acceptance.sql", "175_review_questions.sql", "176_guide_sections.sql", "177_fake_checks_waiver_models.sql", "178_events_run_checks_decided_idx.sql", "179_escalation_wake_lines.sql", "180_run_check_logs.sql", "181_acceptance_carried.sql", "182_events_task_test_skipped_idx.sql", "183_automations.sql", "184_events_asks_v2_idx.sql", "185_drop_events_asks_idx.sql", "186_events_automation_github_reads_idx.sql", "190_org_sessions.sql", "191_events_session_mentioned_idx.sql", "192_session_owner_sweep.sql", "193_events_session_owner_changes_idx.sql", "194_session_owner_attempts.sql", "195_chat.sql", "196_chat_channel_projects.sql", "197_events_session_tool_attempts_idx.sql", "198_events_task_tool_attempts_idx.sql", "199_events_automation_tool_attempts_idx.sql", "200_drop_events_session_github_reads_v2_idx.sql", "201_drop_events_task_github_reads_v2_idx.sql", "202_drop_events_automation_github_reads_idx.sql", "203_task_pull_request.sql", "210_organization_secrets.sql", "211_fake_checks_base_shown_model.sql", "212_incoming_webhooks.sql", "213_chat_link_offers.sql", "214_chat_request_failure.sql", "215_chat_request_failure_backfill.sql", "216_attachment_storage_keys.sql", "217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
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
      "127_feedback.sql",
      "128_project_secret_recipients.sql",
      "129_run_project_secrets.sql",
      "130_run_secret_values.sql",
      "131_fake_secret_models.sql",
      "132_run_synced_bases.sql",
      "133_feedback_pictures_kept.sql", "134_review_range.sql", "135_events_session_github_reads_idx.sql", "136_events_task_github_reads_idx.sql", "137_events_fix_merge_attempted_idx.sql", "138_events_git_merge_attempted_idx.sql", "139_run_retry_backoff.sql", "140_tasks_id_org_idx.sql", "141_model_tiers_id_org_idx.sql", "142_model_calls.sql", "143_checks_pending_since.sql", "144_image_layers.sql", "145_repository_setup_script.sql", "146_run_sync_history.sql", "147_events_session_github_reads_idx.sql", "148_events_session_github_reads_idx.sql", "149_events_task_github_reads_idx.sql", "150_events_task_github_reads_idx.sql", "151_recover_github_read_indexes.sql", "152_child_admission.sql", "153_child_results.sql", "154_child_transport.sql", "155_child_output_fence.sql", "156_child_results_child_idx.sql", "157_task_lifecycle.sql", "158_task_lifecycle_rules.sql", "159_session_attachments.sql",
      "160_session_child_results.sql", "161_runs_session_creation_key_idx.sql", "162_recover_runs_session_creation_key_idx.sql", "163_events_session_github_reads_v2_idx.sql", "164_events_task_github_reads_v2_idx.sql", "165_drop_events_session_github_reads_idx.sql", "166_drop_events_task_github_reads_idx.sql", "167_recover_github_read_v2_indexes.sql", "168_events_run_chat_idx.sql", "169_recover_events_run_chat_idx.sql",
      "170_child_results_verify.sql", "171_run_web.sql", "172_fake_checks_models.sql", "173_run_continues.sql", "174_finding_acceptance.sql", "175_review_questions.sql", "176_guide_sections.sql", "177_fake_checks_waiver_models.sql", "178_events_run_checks_decided_idx.sql", "179_escalation_wake_lines.sql", "180_run_check_logs.sql", "181_acceptance_carried.sql", "182_events_task_test_skipped_idx.sql", "183_automations.sql", "184_events_asks_v2_idx.sql", "185_drop_events_asks_idx.sql", "186_events_automation_github_reads_idx.sql", "190_org_sessions.sql", "191_events_session_mentioned_idx.sql", "192_session_owner_sweep.sql", "193_events_session_owner_changes_idx.sql", "194_session_owner_attempts.sql", "195_chat.sql", "196_chat_channel_projects.sql", "197_events_session_tool_attempts_idx.sql", "198_events_task_tool_attempts_idx.sql", "199_events_automation_tool_attempts_idx.sql", "200_drop_events_session_github_reads_v2_idx.sql", "201_drop_events_task_github_reads_v2_idx.sql", "202_drop_events_automation_github_reads_idx.sql", "203_task_pull_request.sql", "210_organization_secrets.sql", "211_fake_checks_base_shown_model.sql", "212_incoming_webhooks.sql", "213_chat_link_offers.sql", "214_chat_request_failure.sql", "215_chat_request_failure_backfill.sql", "216_attachment_storage_keys.sql", "217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql",
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

// Keep the original test ID; the upgrade now preserves shipped 221 and 222 before applying 223.
test("main's shipped 221 keeps its checksum while 222 adds child cohorts", async () => {
  const { url, sql } = await migratedBefore("223");
  try {
    const before = (await recorded(url)).filter((m) => m.version === "221" || m.version === "222");
    expect(before.map((m) => m.name)).toEqual(["221_branch_previews.sql", "222_chat_notices.sql"]);
    expect([...await sql`SELECT to_regclass('chat_notices')::text AS notices,
      to_regclass('child_cohorts')::text AS cohorts`]).toEqual([{ notices: "chat_notices", cohorts: null }]);
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
    expect((await recorded(url)).filter((m) => m.version === "221" || m.version === "222")).toEqual(before);
    expect([...await sql`SELECT to_regclass('child_cohorts')::text AS cohorts,
      to_regclass('child_cohort_members')::text AS members`]).toEqual([
      { cohorts: "child_cohorts", members: "child_cohort_members" },
    ]);
    expect((await migrate(url, { log: () => {} })).applied).toEqual([]);
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
  await applyBefore(sql, version);
  return { url, sql };
}

// Applies the unrecorded migrations below version, as migrate() would, and
// names them: a test of an older migration stops before later ones drop what it built.
async function applyBefore(sql: SQL, version: string): Promise<string[]> {
  const recorded = new Set((await sql`SELECT version FROM schema_migrations`).map((r: { version: string }) => r.version));
  const applied: string[] = [];
  for (const file of (await listMigrationFiles()).filter((f) => f.version < version && !recorded.has(f.version))) {
    const contents = await file.contents();
    const record = (q: SQL) => q`INSERT INTO schema_migrations (version, name, checksum)
      VALUES (${file.version}, ${file.name}, ${createHash("sha256").update(contents).digest("hex")})`;
    applied.push(file.name);
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
  return applied;
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
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["094_repository_lux_name_unique.sql", "095_project_key_unique.sql", "096_session_names.sql", "097_session_names_validate.sql", "098_agent_network.sql", "099_tier_effort.sql", "100_image_containers.sql", "101_run_can_run_containers.sql", "102_artifact_descriptions.sql", "103_harness_state.sql", "104_question_items.sql", "105_session_archived.sql", "106_session_model.sql", "107_sessions_tier_idx.sql", "108_directives_resends_idx.sql", "109_brainstorm_anywhere.sql", "110_repository_github_host.sql", "111_task_relations.sql", "112_people_github.sql", "113_run_children.sql", "114_preview_wake_timeouts.sql", "115_agent_coordination.sql", "116_peer_ended.sql", "117_events_agent_message_idx.sql", "118_accounts.sql", "119_github_owner_oauth.sql", "120_agent_machine_moves.sql", "121_pull_request_rerequests.sql", "122_push_per_organization.sql", "123_acceptance_verdicts.sql", "124_task_sources.sql", "125_task_source_mode.sql", "126_fake_done_model.sql", "127_feedback.sql", "128_project_secret_recipients.sql", "129_run_project_secrets.sql", "130_run_secret_values.sql", "131_fake_secret_models.sql", "132_run_synced_bases.sql", "133_feedback_pictures_kept.sql", "134_review_range.sql", "135_events_session_github_reads_idx.sql", "136_events_task_github_reads_idx.sql", "137_events_fix_merge_attempted_idx.sql", "138_events_git_merge_attempted_idx.sql", "139_run_retry_backoff.sql", "140_tasks_id_org_idx.sql", "141_model_tiers_id_org_idx.sql", "142_model_calls.sql", "143_checks_pending_since.sql", "144_image_layers.sql", "145_repository_setup_script.sql", "146_run_sync_history.sql", "147_events_session_github_reads_idx.sql", "148_events_session_github_reads_idx.sql", "149_events_task_github_reads_idx.sql", "150_events_task_github_reads_idx.sql", "151_recover_github_read_indexes.sql", "152_child_admission.sql", "153_child_results.sql", "154_child_transport.sql", "155_child_output_fence.sql", "156_child_results_child_idx.sql", "157_task_lifecycle.sql", "158_task_lifecycle_rules.sql", "159_session_attachments.sql", "160_session_child_results.sql", "161_runs_session_creation_key_idx.sql", "162_recover_runs_session_creation_key_idx.sql", "163_events_session_github_reads_v2_idx.sql", "164_events_task_github_reads_v2_idx.sql", "165_drop_events_session_github_reads_idx.sql", "166_drop_events_task_github_reads_idx.sql", "167_recover_github_read_v2_indexes.sql", "168_events_run_chat_idx.sql", "169_recover_events_run_chat_idx.sql", "170_child_results_verify.sql", "171_run_web.sql", "172_fake_checks_models.sql", "173_run_continues.sql", "174_finding_acceptance.sql", "175_review_questions.sql", "176_guide_sections.sql", "177_fake_checks_waiver_models.sql", "178_events_run_checks_decided_idx.sql", "179_escalation_wake_lines.sql", "180_run_check_logs.sql", "181_acceptance_carried.sql", "182_events_task_test_skipped_idx.sql", "183_automations.sql", "184_events_asks_v2_idx.sql", "185_drop_events_asks_idx.sql", "186_events_automation_github_reads_idx.sql", "190_org_sessions.sql", "191_events_session_mentioned_idx.sql", "192_session_owner_sweep.sql", "193_events_session_owner_changes_idx.sql", "194_session_owner_attempts.sql", "195_chat.sql", "196_chat_channel_projects.sql", "197_events_session_tool_attempts_idx.sql", "198_events_task_tool_attempts_idx.sql", "199_events_automation_tool_attempts_idx.sql", "200_drop_events_session_github_reads_v2_idx.sql", "201_drop_events_task_github_reads_v2_idx.sql", "202_drop_events_automation_github_reads_idx.sql", "203_task_pull_request.sql", "210_organization_secrets.sql", "211_fake_checks_base_shown_model.sql", "212_incoming_webhooks.sql", "213_chat_link_offers.sql", "214_chat_request_failure.sql", "215_chat_request_failure_backfill.sql", "216_attachment_storage_keys.sql", "217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
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
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["095_project_key_unique.sql", "096_session_names.sql", "097_session_names_validate.sql", "098_agent_network.sql", "099_tier_effort.sql", "100_image_containers.sql", "101_run_can_run_containers.sql", "102_artifact_descriptions.sql", "103_harness_state.sql", "104_question_items.sql", "105_session_archived.sql", "106_session_model.sql", "107_sessions_tier_idx.sql", "108_directives_resends_idx.sql", "109_brainstorm_anywhere.sql", "110_repository_github_host.sql", "111_task_relations.sql", "112_people_github.sql", "113_run_children.sql", "114_preview_wake_timeouts.sql", "115_agent_coordination.sql", "116_peer_ended.sql", "117_events_agent_message_idx.sql", "118_accounts.sql", "119_github_owner_oauth.sql", "120_agent_machine_moves.sql", "121_pull_request_rerequests.sql", "122_push_per_organization.sql", "123_acceptance_verdicts.sql", "124_task_sources.sql", "125_task_source_mode.sql", "126_fake_done_model.sql", "127_feedback.sql", "128_project_secret_recipients.sql", "129_run_project_secrets.sql", "130_run_secret_values.sql", "131_fake_secret_models.sql", "132_run_synced_bases.sql", "133_feedback_pictures_kept.sql", "134_review_range.sql", "135_events_session_github_reads_idx.sql", "136_events_task_github_reads_idx.sql", "137_events_fix_merge_attempted_idx.sql", "138_events_git_merge_attempted_idx.sql", "139_run_retry_backoff.sql", "140_tasks_id_org_idx.sql", "141_model_tiers_id_org_idx.sql", "142_model_calls.sql", "143_checks_pending_since.sql", "144_image_layers.sql", "145_repository_setup_script.sql", "146_run_sync_history.sql", "147_events_session_github_reads_idx.sql", "148_events_session_github_reads_idx.sql", "149_events_task_github_reads_idx.sql", "150_events_task_github_reads_idx.sql", "151_recover_github_read_indexes.sql", "152_child_admission.sql", "153_child_results.sql", "154_child_transport.sql", "155_child_output_fence.sql", "156_child_results_child_idx.sql", "157_task_lifecycle.sql", "158_task_lifecycle_rules.sql", "159_session_attachments.sql", "160_session_child_results.sql", "161_runs_session_creation_key_idx.sql", "162_recover_runs_session_creation_key_idx.sql", "163_events_session_github_reads_v2_idx.sql", "164_events_task_github_reads_v2_idx.sql", "165_drop_events_session_github_reads_idx.sql", "166_drop_events_task_github_reads_idx.sql", "167_recover_github_read_v2_indexes.sql", "168_events_run_chat_idx.sql", "169_recover_events_run_chat_idx.sql", "170_child_results_verify.sql", "171_run_web.sql", "172_fake_checks_models.sql", "173_run_continues.sql", "174_finding_acceptance.sql", "175_review_questions.sql", "176_guide_sections.sql", "177_fake_checks_waiver_models.sql", "178_events_run_checks_decided_idx.sql", "179_escalation_wake_lines.sql", "180_run_check_logs.sql", "181_acceptance_carried.sql", "182_events_task_test_skipped_idx.sql", "183_automations.sql", "184_events_asks_v2_idx.sql", "185_drop_events_asks_idx.sql", "186_events_automation_github_reads_idx.sql", "190_org_sessions.sql", "191_events_session_mentioned_idx.sql", "192_session_owner_sweep.sql", "193_events_session_owner_changes_idx.sql", "194_session_owner_attempts.sql", "195_chat.sql", "196_chat_channel_projects.sql", "197_events_session_tool_attempts_idx.sql", "198_events_task_tool_attempts_idx.sql", "199_events_automation_tool_attempts_idx.sql", "200_drop_events_session_github_reads_v2_idx.sql", "201_drop_events_task_github_reads_v2_idx.sql", "202_drop_events_automation_github_reads_idx.sql", "203_task_pull_request.sql", "210_organization_secrets.sql", "211_fake_checks_base_shown_model.sql", "212_incoming_webhooks.sql", "213_chat_link_offers.sql", "214_chat_request_failure.sql", "215_chat_request_failure_backfill.sql", "216_attachment_storage_keys.sql", "217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
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

    expect((await migrate(url, { log: () => {} })).applied).toEqual(["074_task_inline_images.sql", "075_conductor_decisions.sql", "076_runs_conductor_run_idx.sql", "077_conductor_steer.sql", "078_events_run_lands_idx.sql", "079_conductor_github.sql", "080_webhook_repair.sql", "081_preview_secrets.sql", "082_conductor_edits.sql", "083_finding_topic.sql", "084_escalation_questions.sql", "085_brainstorm_role.sql", "086_sessions.sql", "087_session_memories.sql", "088_session_filings_idx.sql", "089_session_functions_parallel.sql", "090_stalled_runs.sql", "091_brainstorm_stuck_turn.sql", "092_retire_completed_runs.sql", "093_lux_name.sql", "094_repository_lux_name_unique.sql", "095_project_key_unique.sql", "096_session_names.sql", "097_session_names_validate.sql", "098_agent_network.sql", "099_tier_effort.sql", "100_image_containers.sql", "101_run_can_run_containers.sql", "102_artifact_descriptions.sql", "103_harness_state.sql", "104_question_items.sql", "105_session_archived.sql", "106_session_model.sql", "107_sessions_tier_idx.sql", "108_directives_resends_idx.sql", "109_brainstorm_anywhere.sql", "110_repository_github_host.sql", "111_task_relations.sql", "112_people_github.sql", "113_run_children.sql", "114_preview_wake_timeouts.sql", "115_agent_coordination.sql", "116_peer_ended.sql", "117_events_agent_message_idx.sql", "118_accounts.sql", "119_github_owner_oauth.sql", "120_agent_machine_moves.sql", "121_pull_request_rerequests.sql", "122_push_per_organization.sql", "123_acceptance_verdicts.sql", "124_task_sources.sql", "125_task_source_mode.sql", "126_fake_done_model.sql", "127_feedback.sql", "128_project_secret_recipients.sql", "129_run_project_secrets.sql", "130_run_secret_values.sql", "131_fake_secret_models.sql", "132_run_synced_bases.sql", "133_feedback_pictures_kept.sql", "134_review_range.sql", "135_events_session_github_reads_idx.sql", "136_events_task_github_reads_idx.sql", "137_events_fix_merge_attempted_idx.sql", "138_events_git_merge_attempted_idx.sql", "139_run_retry_backoff.sql", "140_tasks_id_org_idx.sql", "141_model_tiers_id_org_idx.sql", "142_model_calls.sql", "143_checks_pending_since.sql", "144_image_layers.sql", "145_repository_setup_script.sql", "146_run_sync_history.sql", "147_events_session_github_reads_idx.sql", "148_events_session_github_reads_idx.sql", "149_events_task_github_reads_idx.sql", "150_events_task_github_reads_idx.sql", "151_recover_github_read_indexes.sql", "152_child_admission.sql", "153_child_results.sql", "154_child_transport.sql", "155_child_output_fence.sql", "156_child_results_child_idx.sql", "157_task_lifecycle.sql", "158_task_lifecycle_rules.sql", "159_session_attachments.sql", "160_session_child_results.sql", "161_runs_session_creation_key_idx.sql", "162_recover_runs_session_creation_key_idx.sql", "163_events_session_github_reads_v2_idx.sql", "164_events_task_github_reads_v2_idx.sql", "165_drop_events_session_github_reads_idx.sql", "166_drop_events_task_github_reads_idx.sql", "167_recover_github_read_v2_indexes.sql", "168_events_run_chat_idx.sql", "169_recover_events_run_chat_idx.sql", "170_child_results_verify.sql", "171_run_web.sql", "172_fake_checks_models.sql", "173_run_continues.sql", "174_finding_acceptance.sql", "175_review_questions.sql", "176_guide_sections.sql", "177_fake_checks_waiver_models.sql", "178_events_run_checks_decided_idx.sql", "179_escalation_wake_lines.sql", "180_run_check_logs.sql", "181_acceptance_carried.sql", "182_events_task_test_skipped_idx.sql", "183_automations.sql", "184_events_asks_v2_idx.sql", "185_drop_events_asks_idx.sql", "186_events_automation_github_reads_idx.sql", "190_org_sessions.sql", "191_events_session_mentioned_idx.sql", "192_session_owner_sweep.sql", "193_events_session_owner_changes_idx.sql", "194_session_owner_attempts.sql", "195_chat.sql", "196_chat_channel_projects.sql", "197_events_session_tool_attempts_idx.sql", "198_events_task_tool_attempts_idx.sql", "199_events_automation_tool_attempts_idx.sql", "200_drop_events_session_github_reads_v2_idx.sql", "201_drop_events_task_github_reads_v2_idx.sql", "202_drop_events_automation_github_reads_idx.sql", "203_task_pull_request.sql", "210_organization_secrets.sql", "211_fake_checks_base_shown_model.sql", "212_incoming_webhooks.sql", "213_chat_link_offers.sql", "214_chat_request_failure.sql", "215_chat_request_failure_backfill.sql", "216_attachment_storage_keys.sql", "217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
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
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["096_session_names.sql", "097_session_names_validate.sql", "098_agent_network.sql", "099_tier_effort.sql", "100_image_containers.sql", "101_run_can_run_containers.sql", "102_artifact_descriptions.sql", "103_harness_state.sql", "104_question_items.sql", "105_session_archived.sql", "106_session_model.sql", "107_sessions_tier_idx.sql", "108_directives_resends_idx.sql", "109_brainstorm_anywhere.sql", "110_repository_github_host.sql", "111_task_relations.sql", "112_people_github.sql", "113_run_children.sql", "114_preview_wake_timeouts.sql", "115_agent_coordination.sql", "116_peer_ended.sql", "117_events_agent_message_idx.sql", "118_accounts.sql", "119_github_owner_oauth.sql", "120_agent_machine_moves.sql", "121_pull_request_rerequests.sql", "122_push_per_organization.sql", "123_acceptance_verdicts.sql", "124_task_sources.sql", "125_task_source_mode.sql", "126_fake_done_model.sql", "127_feedback.sql", "128_project_secret_recipients.sql", "129_run_project_secrets.sql", "130_run_secret_values.sql", "131_fake_secret_models.sql", "132_run_synced_bases.sql", "133_feedback_pictures_kept.sql", "134_review_range.sql", "135_events_session_github_reads_idx.sql", "136_events_task_github_reads_idx.sql", "137_events_fix_merge_attempted_idx.sql", "138_events_git_merge_attempted_idx.sql", "139_run_retry_backoff.sql", "140_tasks_id_org_idx.sql", "141_model_tiers_id_org_idx.sql", "142_model_calls.sql", "143_checks_pending_since.sql", "144_image_layers.sql", "145_repository_setup_script.sql", "146_run_sync_history.sql", "147_events_session_github_reads_idx.sql", "148_events_session_github_reads_idx.sql", "149_events_task_github_reads_idx.sql", "150_events_task_github_reads_idx.sql", "151_recover_github_read_indexes.sql", "152_child_admission.sql", "153_child_results.sql", "154_child_transport.sql", "155_child_output_fence.sql", "156_child_results_child_idx.sql", "157_task_lifecycle.sql", "158_task_lifecycle_rules.sql", "159_session_attachments.sql", "160_session_child_results.sql", "161_runs_session_creation_key_idx.sql", "162_recover_runs_session_creation_key_idx.sql", "163_events_session_github_reads_v2_idx.sql", "164_events_task_github_reads_v2_idx.sql", "165_drop_events_session_github_reads_idx.sql", "166_drop_events_task_github_reads_idx.sql", "167_recover_github_read_v2_indexes.sql", "168_events_run_chat_idx.sql", "169_recover_events_run_chat_idx.sql", "170_child_results_verify.sql", "171_run_web.sql", "172_fake_checks_models.sql", "173_run_continues.sql", "174_finding_acceptance.sql", "175_review_questions.sql", "176_guide_sections.sql", "177_fake_checks_waiver_models.sql", "178_events_run_checks_decided_idx.sql", "179_escalation_wake_lines.sql", "180_run_check_logs.sql", "181_acceptance_carried.sql", "182_events_task_test_skipped_idx.sql", "183_automations.sql", "184_events_asks_v2_idx.sql", "185_drop_events_asks_idx.sql", "186_events_automation_github_reads_idx.sql", "190_org_sessions.sql", "191_events_session_mentioned_idx.sql", "192_session_owner_sweep.sql", "193_events_session_owner_changes_idx.sql", "194_session_owner_attempts.sql", "195_chat.sql", "196_chat_channel_projects.sql", "197_events_session_tool_attempts_idx.sql", "198_events_task_tool_attempts_idx.sql", "199_events_automation_tool_attempts_idx.sql", "200_drop_events_session_github_reads_v2_idx.sql", "201_drop_events_task_github_reads_v2_idx.sql", "202_drop_events_automation_github_reads_idx.sql", "203_task_pull_request.sql", "210_organization_secrets.sql", "211_fake_checks_base_shown_model.sql", "212_incoming_webhooks.sql", "213_chat_link_offers.sql", "214_chat_request_failure.sql", "215_chat_request_failure_backfill.sql", "216_attachment_storage_keys.sql", "217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
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
    expect(await migrating.then((r) => r.applied)).toEqual(["097_session_names_validate.sql", "098_agent_network.sql", "099_tier_effort.sql", "100_image_containers.sql", "101_run_can_run_containers.sql", "102_artifact_descriptions.sql", "103_harness_state.sql", "104_question_items.sql", "105_session_archived.sql", "106_session_model.sql", "107_sessions_tier_idx.sql", "108_directives_resends_idx.sql", "109_brainstorm_anywhere.sql", "110_repository_github_host.sql", "111_task_relations.sql", "112_people_github.sql", "113_run_children.sql", "114_preview_wake_timeouts.sql", "115_agent_coordination.sql", "116_peer_ended.sql", "117_events_agent_message_idx.sql", "118_accounts.sql", "119_github_owner_oauth.sql", "120_agent_machine_moves.sql", "121_pull_request_rerequests.sql", "122_push_per_organization.sql", "123_acceptance_verdicts.sql", "124_task_sources.sql", "125_task_source_mode.sql", "126_fake_done_model.sql", "127_feedback.sql", "128_project_secret_recipients.sql", "129_run_project_secrets.sql", "130_run_secret_values.sql", "131_fake_secret_models.sql", "132_run_synced_bases.sql", "133_feedback_pictures_kept.sql", "134_review_range.sql", "135_events_session_github_reads_idx.sql", "136_events_task_github_reads_idx.sql", "137_events_fix_merge_attempted_idx.sql", "138_events_git_merge_attempted_idx.sql", "139_run_retry_backoff.sql", "140_tasks_id_org_idx.sql", "141_model_tiers_id_org_idx.sql", "142_model_calls.sql", "143_checks_pending_since.sql", "144_image_layers.sql", "145_repository_setup_script.sql", "146_run_sync_history.sql", "147_events_session_github_reads_idx.sql", "148_events_session_github_reads_idx.sql", "149_events_task_github_reads_idx.sql", "150_events_task_github_reads_idx.sql", "151_recover_github_read_indexes.sql", "152_child_admission.sql", "153_child_results.sql", "154_child_transport.sql", "155_child_output_fence.sql", "156_child_results_child_idx.sql", "157_task_lifecycle.sql", "158_task_lifecycle_rules.sql", "159_session_attachments.sql", "160_session_child_results.sql", "161_runs_session_creation_key_idx.sql", "162_recover_runs_session_creation_key_idx.sql", "163_events_session_github_reads_v2_idx.sql", "164_events_task_github_reads_v2_idx.sql", "165_drop_events_session_github_reads_idx.sql", "166_drop_events_task_github_reads_idx.sql", "167_recover_github_read_v2_indexes.sql", "168_events_run_chat_idx.sql", "169_recover_events_run_chat_idx.sql", "170_child_results_verify.sql", "171_run_web.sql", "172_fake_checks_models.sql", "173_run_continues.sql", "174_finding_acceptance.sql", "175_review_questions.sql", "176_guide_sections.sql", "177_fake_checks_waiver_models.sql", "178_events_run_checks_decided_idx.sql", "179_escalation_wake_lines.sql", "180_run_check_logs.sql", "181_acceptance_carried.sql", "182_events_task_test_skipped_idx.sql", "183_automations.sql", "184_events_asks_v2_idx.sql", "185_drop_events_asks_idx.sql", "186_events_automation_github_reads_idx.sql", "190_org_sessions.sql", "191_events_session_mentioned_idx.sql", "192_session_owner_sweep.sql", "193_events_session_owner_changes_idx.sql", "194_session_owner_attempts.sql", "195_chat.sql", "196_chat_channel_projects.sql", "197_events_session_tool_attempts_idx.sql", "198_events_task_tool_attempts_idx.sql", "199_events_automation_tool_attempts_idx.sql", "200_drop_events_session_github_reads_v2_idx.sql", "201_drop_events_task_github_reads_v2_idx.sql", "202_drop_events_automation_github_reads_idx.sql", "203_task_pull_request.sql", "210_organization_secrets.sql", "211_fake_checks_base_shown_model.sql", "212_incoming_webhooks.sql", "213_chat_link_offers.sql", "214_chat_request_failure.sql", "215_chat_request_failure_backfill.sql", "216_attachment_storage_keys.sql", "217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
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
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["097_session_names_validate.sql", "098_agent_network.sql", "099_tier_effort.sql", "100_image_containers.sql", "101_run_can_run_containers.sql", "102_artifact_descriptions.sql", "103_harness_state.sql", "104_question_items.sql", "105_session_archived.sql", "106_session_model.sql", "107_sessions_tier_idx.sql", "108_directives_resends_idx.sql", "109_brainstorm_anywhere.sql", "110_repository_github_host.sql", "111_task_relations.sql", "112_people_github.sql", "113_run_children.sql", "114_preview_wake_timeouts.sql", "115_agent_coordination.sql", "116_peer_ended.sql", "117_events_agent_message_idx.sql", "118_accounts.sql", "119_github_owner_oauth.sql", "120_agent_machine_moves.sql", "121_pull_request_rerequests.sql", "122_push_per_organization.sql", "123_acceptance_verdicts.sql", "124_task_sources.sql", "125_task_source_mode.sql", "126_fake_done_model.sql", "127_feedback.sql", "128_project_secret_recipients.sql", "129_run_project_secrets.sql", "130_run_secret_values.sql", "131_fake_secret_models.sql", "132_run_synced_bases.sql", "133_feedback_pictures_kept.sql", "134_review_range.sql", "135_events_session_github_reads_idx.sql", "136_events_task_github_reads_idx.sql", "137_events_fix_merge_attempted_idx.sql", "138_events_git_merge_attempted_idx.sql", "139_run_retry_backoff.sql", "140_tasks_id_org_idx.sql", "141_model_tiers_id_org_idx.sql", "142_model_calls.sql", "143_checks_pending_since.sql", "144_image_layers.sql", "145_repository_setup_script.sql", "146_run_sync_history.sql", "147_events_session_github_reads_idx.sql", "148_events_session_github_reads_idx.sql", "149_events_task_github_reads_idx.sql", "150_events_task_github_reads_idx.sql", "151_recover_github_read_indexes.sql", "152_child_admission.sql", "153_child_results.sql", "154_child_transport.sql", "155_child_output_fence.sql", "156_child_results_child_idx.sql", "157_task_lifecycle.sql", "158_task_lifecycle_rules.sql", "159_session_attachments.sql", "160_session_child_results.sql", "161_runs_session_creation_key_idx.sql", "162_recover_runs_session_creation_key_idx.sql", "163_events_session_github_reads_v2_idx.sql", "164_events_task_github_reads_v2_idx.sql", "165_drop_events_session_github_reads_idx.sql", "166_drop_events_task_github_reads_idx.sql", "167_recover_github_read_v2_indexes.sql", "168_events_run_chat_idx.sql", "169_recover_events_run_chat_idx.sql", "170_child_results_verify.sql", "171_run_web.sql", "172_fake_checks_models.sql", "173_run_continues.sql", "174_finding_acceptance.sql", "175_review_questions.sql", "176_guide_sections.sql", "177_fake_checks_waiver_models.sql", "178_events_run_checks_decided_idx.sql", "179_escalation_wake_lines.sql", "180_run_check_logs.sql", "181_acceptance_carried.sql", "182_events_task_test_skipped_idx.sql", "183_automations.sql", "184_events_asks_v2_idx.sql", "185_drop_events_asks_idx.sql", "186_events_automation_github_reads_idx.sql", "190_org_sessions.sql", "191_events_session_mentioned_idx.sql", "192_session_owner_sweep.sql", "193_events_session_owner_changes_idx.sql", "194_session_owner_attempts.sql", "195_chat.sql", "196_chat_channel_projects.sql", "197_events_session_tool_attempts_idx.sql", "198_events_task_tool_attempts_idx.sql", "199_events_automation_tool_attempts_idx.sql", "200_drop_events_session_github_reads_v2_idx.sql", "201_drop_events_task_github_reads_v2_idx.sql", "202_drop_events_automation_github_reads_idx.sql", "203_task_pull_request.sql", "210_organization_secrets.sql", "211_fake_checks_base_shown_model.sql", "212_incoming_webhooks.sql", "213_chat_link_offers.sql", "214_chat_request_failure.sql", "215_chat_request_failure_backfill.sql", "216_attachment_storage_keys.sql", "217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
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

test("140 and 141 build tasks' and tiers' (id, organization_id) keys outside a transaction; 142 keys model_calls and the judgement tier on them", async () => {
  for (const version of ["140", "141"]) {
    const file = (await listMigrationFiles()).find((f) => f.version === version);
    expect(file && outsideTransaction(await file.contents())).toBe(true);
  }
  const { url, sql } = await migratedBefore("140");
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_a', 'a', 'a'), ('org_b', 'b', 'b')`;
    await sql`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_b', 'org_b', 'B', 'b', 'B')`;
    await sql`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('tsk_b', 'org_b', 'prj_b', 1, 'T', 'G')`;
    const [tierB] = await sql`SELECT id FROM model_tiers WHERE organization_id = 'org_b' LIMIT 1`;
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["140_tasks_id_org_idx.sql", "141_model_tiers_id_org_idx.sql", "142_model_calls.sql", "143_checks_pending_since.sql", "144_image_layers.sql", "145_repository_setup_script.sql", "146_run_sync_history.sql", "147_events_session_github_reads_idx.sql", "148_events_session_github_reads_idx.sql", "149_events_task_github_reads_idx.sql", "150_events_task_github_reads_idx.sql", "151_recover_github_read_indexes.sql", "152_child_admission.sql", "153_child_results.sql", "154_child_transport.sql", "155_child_output_fence.sql", "156_child_results_child_idx.sql", "157_task_lifecycle.sql", "158_task_lifecycle_rules.sql", "159_session_attachments.sql", "160_session_child_results.sql", "161_runs_session_creation_key_idx.sql", "162_recover_runs_session_creation_key_idx.sql", "163_events_session_github_reads_v2_idx.sql", "164_events_task_github_reads_v2_idx.sql", "165_drop_events_session_github_reads_idx.sql", "166_drop_events_task_github_reads_idx.sql", "167_recover_github_read_v2_indexes.sql", "168_events_run_chat_idx.sql", "169_recover_events_run_chat_idx.sql", "170_child_results_verify.sql", "171_run_web.sql", "172_fake_checks_models.sql", "173_run_continues.sql", "174_finding_acceptance.sql", "175_review_questions.sql", "176_guide_sections.sql", "177_fake_checks_waiver_models.sql", "178_events_run_checks_decided_idx.sql", "179_escalation_wake_lines.sql", "180_run_check_logs.sql", "181_acceptance_carried.sql", "182_events_task_test_skipped_idx.sql", "183_automations.sql", "184_events_asks_v2_idx.sql", "185_drop_events_asks_idx.sql", "186_events_automation_github_reads_idx.sql", "190_org_sessions.sql", "191_events_session_mentioned_idx.sql", "192_session_owner_sweep.sql", "193_events_session_owner_changes_idx.sql", "194_session_owner_attempts.sql", "195_chat.sql", "196_chat_channel_projects.sql", "197_events_session_tool_attempts_idx.sql", "198_events_task_tool_attempts_idx.sql", "199_events_automation_tool_attempts_idx.sql", "200_drop_events_session_github_reads_v2_idx.sql", "201_drop_events_task_github_reads_v2_idx.sql", "202_drop_events_automation_github_reads_idx.sql", "203_task_pull_request.sql", "210_organization_secrets.sql", "211_fake_checks_base_shown_model.sql", "212_incoming_webhooks.sql", "213_chat_link_offers.sql", "214_chat_request_failure.sql", "215_chat_request_failure_backfill.sql", "216_attachment_storage_keys.sql", "217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
    const keys = await sql`SELECT c.conrelid::regclass::text AS tbl, i.indisvalid AS valid FROM pg_constraint c JOIN pg_index i ON i.indexrelid = c.conindid
      WHERE c.contype = 'u' AND c.conname IN ('tasks_id_organization_id_key', 'model_tiers_id_organization_id_key') ORDER BY 1`;
    expect([...keys]).toEqual([{ tbl: "model_tiers", valid: true }, { tbl: "tasks", valid: true }]);
    const insert = (task: string | null, tier: string | null) => sql`INSERT INTO model_calls (id, organization_id, purpose, asker, task_id, tier_id,
      tier_name, model, question, request, status, latency_ms) VALUES (${`mcl_${task}_${tier}`}, 'org_a', 'judgement', 'a', ${task}, ${tier}, 'x', 'm', 'q', '{}', 'ok', 0)`;
    // A postgres.js query runs only once awaited: expect(query).rejects never starts it.
    const refusal = async (q: () => Promise<unknown>) => q().then(() => "", (err: Error) => err.message);
    expect(await refusal(async () => await insert("tsk_b", null))).toContain("foreign key");
    expect(await refusal(async () => await insert(null, tierB.id))).toContain("foreign key");
    expect(await refusal(async () => await sql`UPDATE organizations SET judgement_tier_id = ${tierB.id} WHERE id = 'org_a'`)).toContain("foreign key");
    await insert(null, null);
  } finally {
    await sql.end();
  }
}, 120_000);

test("142 rebuilds a key an interrupted 140 left invalid, and 140 and 141 run again after a crash before their record", async () => {
  const { url, sql } = await migratedBefore("142");
  const builder = await sql.reserve();
  const holder = await sql.reserve();
  try {
    // A concurrent build waits out every older snapshot; cancelled while it
    // waits, it leaves its index in the catalog, INVALID, as a killed 140 does.
    await sql`DROP INDEX tasks_id_organization_id_key`;
    await holder`BEGIN ISOLATION LEVEL REPEATABLE READ`;
    await holder`SELECT count(*) FROM tasks`;
    await builder`SET statement_timeout = '500ms'`;
    const refusal = async (q: () => Promise<unknown>) => q().then(() => "", (err: Error) => err.message);
    expect(await refusal(async () => await builder`CREATE UNIQUE INDEX CONCURRENTLY tasks_id_organization_id_key ON tasks (id, organization_id)`))
      .toContain("statement timeout");
    await holder`COMMIT`;
    const validity = async () => [...await sql`SELECT c.relname AS name, i.indisvalid AS valid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
      WHERE c.relname IN ('tasks_id_organization_id_key', 'model_tiers_id_organization_id_key') ORDER BY 1`];
    expect(await validity()).toEqual([{ name: "model_tiers_id_organization_id_key", valid: true }, { name: "tasks_id_organization_id_key", valid: false }]);

    expect((await migrate(url, { log: () => {} })).applied).toEqual(["142_model_calls.sql", "143_checks_pending_since.sql", "144_image_layers.sql", "145_repository_setup_script.sql", "146_run_sync_history.sql", "147_events_session_github_reads_idx.sql", "148_events_session_github_reads_idx.sql", "149_events_task_github_reads_idx.sql", "150_events_task_github_reads_idx.sql", "151_recover_github_read_indexes.sql", "152_child_admission.sql", "153_child_results.sql", "154_child_transport.sql", "155_child_output_fence.sql", "156_child_results_child_idx.sql", "157_task_lifecycle.sql", "158_task_lifecycle_rules.sql", "159_session_attachments.sql", "160_session_child_results.sql", "161_runs_session_creation_key_idx.sql", "162_recover_runs_session_creation_key_idx.sql", "163_events_session_github_reads_v2_idx.sql", "164_events_task_github_reads_v2_idx.sql", "165_drop_events_session_github_reads_idx.sql", "166_drop_events_task_github_reads_idx.sql", "167_recover_github_read_v2_indexes.sql", "168_events_run_chat_idx.sql", "169_recover_events_run_chat_idx.sql", "170_child_results_verify.sql", "171_run_web.sql", "172_fake_checks_models.sql", "173_run_continues.sql", "174_finding_acceptance.sql", "175_review_questions.sql", "176_guide_sections.sql", "177_fake_checks_waiver_models.sql", "178_events_run_checks_decided_idx.sql", "179_escalation_wake_lines.sql", "180_run_check_logs.sql", "181_acceptance_carried.sql", "182_events_task_test_skipped_idx.sql", "183_automations.sql", "184_events_asks_v2_idx.sql", "185_drop_events_asks_idx.sql", "186_events_automation_github_reads_idx.sql", "190_org_sessions.sql", "191_events_session_mentioned_idx.sql", "192_session_owner_sweep.sql", "193_events_session_owner_changes_idx.sql", "194_session_owner_attempts.sql", "195_chat.sql", "196_chat_channel_projects.sql", "197_events_session_tool_attempts_idx.sql", "198_events_task_tool_attempts_idx.sql", "199_events_automation_tool_attempts_idx.sql", "200_drop_events_session_github_reads_v2_idx.sql", "201_drop_events_task_github_reads_v2_idx.sql", "202_drop_events_automation_github_reads_idx.sql", "203_task_pull_request.sql", "210_organization_secrets.sql", "211_fake_checks_base_shown_model.sql", "212_incoming_webhooks.sql", "213_chat_link_offers.sql", "214_chat_request_failure.sql", "215_chat_request_failure_backfill.sql", "216_attachment_storage_keys.sql", "217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
    const keys = async () => [...await sql`SELECT c.conname AS name, i.indisvalid AS valid FROM pg_constraint c JOIN pg_index i ON i.indexrelid = c.conindid
      WHERE c.contype = 'u' AND c.conname IN ('tasks_id_organization_id_key', 'model_tiers_id_organization_id_key') ORDER BY 1`];
    const attached = [{ name: "model_tiers_id_organization_id_key", valid: true }, { name: "tasks_id_organization_id_key", valid: true }];
    expect(await keys()).toEqual(attached);

    await sql`DELETE FROM schema_migrations WHERE version IN ('140', '141')`;
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["140_tasks_id_org_idx.sql", "141_model_tiers_id_org_idx.sql"]);
    expect(await keys()).toEqual(attached);
  } finally {
    builder.release();
    holder.release();
    await sql.end();
  }
}, 120_000);

test("162 rebuilds an invalid 161 index and replays a completed 161 build", async () => {
  const { url, sql } = await migratedBefore("161");
  const builder = await sql.reserve();
  const holder = await sql.reserve();
  try {
    await holder`BEGIN ISOLATION LEVEL REPEATABLE READ`;
    await holder`SELECT count(*) FROM runs`;
    await builder`SET statement_timeout = '500ms'`;
    const refusal = async (q: () => Promise<unknown>) => q().then(() => "", (err: Error) => err.message);
    expect(await refusal(async () => await builder`CREATE UNIQUE INDEX CONCURRENTLY runs_session_creation_key_idx
      ON runs (session_id, creation_key) WHERE session_id IS NOT NULL AND creation_key IS NOT NULL`))
      .toContain("statement timeout");
    await holder`COMMIT`;

    const validity = async () => [...await sql`SELECT i.indisvalid AS valid, i.indisunique AS unique_index
      FROM pg_index i WHERE i.indexrelid = to_regclass('runs_session_creation_key_idx')`];
    expect(await validity()).toEqual([{ valid: false, unique_index: true }]);

    expect((await migrate(url, { log: () => {} })).applied).toEqual([
      "161_runs_session_creation_key_idx.sql", "162_recover_runs_session_creation_key_idx.sql",
      "163_events_session_github_reads_v2_idx.sql", "164_events_task_github_reads_v2_idx.sql", "165_drop_events_session_github_reads_idx.sql",
      "166_drop_events_task_github_reads_idx.sql", "167_recover_github_read_v2_indexes.sql", "168_events_run_chat_idx.sql",
"169_recover_events_run_chat_idx.sql", "170_child_results_verify.sql", "171_run_web.sql", "172_fake_checks_models.sql", "173_run_continues.sql", "174_finding_acceptance.sql", "175_review_questions.sql", "176_guide_sections.sql", "177_fake_checks_waiver_models.sql", "178_events_run_checks_decided_idx.sql", "179_escalation_wake_lines.sql", "180_run_check_logs.sql", "181_acceptance_carried.sql", "182_events_task_test_skipped_idx.sql", "183_automations.sql", "184_events_asks_v2_idx.sql", "185_drop_events_asks_idx.sql", "186_events_automation_github_reads_idx.sql", "190_org_sessions.sql", "191_events_session_mentioned_idx.sql", "192_session_owner_sweep.sql", "193_events_session_owner_changes_idx.sql", "194_session_owner_attempts.sql", "195_chat.sql", "196_chat_channel_projects.sql", "197_events_session_tool_attempts_idx.sql", "198_events_task_tool_attempts_idx.sql", "199_events_automation_tool_attempts_idx.sql", "200_drop_events_session_github_reads_v2_idx.sql", "201_drop_events_task_github_reads_v2_idx.sql", "202_drop_events_automation_github_reads_idx.sql", "203_task_pull_request.sql", "210_organization_secrets.sql", "211_fake_checks_base_shown_model.sql", "212_incoming_webhooks.sql", "213_chat_link_offers.sql", "214_chat_request_failure.sql", "215_chat_request_failure_backfill.sql", "216_attachment_storage_keys.sql", "217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql",
    ]);
    expect(await validity()).toEqual([{ valid: true, unique_index: true }]);

    await sql`DELETE FROM schema_migrations WHERE version = '161'`;
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["161_runs_session_creation_key_idx.sql"]);
    expect(await validity()).toEqual([{ valid: true, unique_index: true }]);
  } finally {
    await holder`ROLLBACK`;
    builder.release();
    holder.release();
    await sql.end();
  }
}, 120_000);

test("145 adds a nullable, size-checked setup script to existing repositories", async () => {
  const { url, sql } = await migratedBefore("145");
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_145', 'org 145', 'org-145')`;
    await sql`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_145', 'org_145', 'P', 'p-145', 'P')`;
    await sql`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_145', 'org_145', 'prj_145', 'api', 'https://github.com/acme/api.git')`;
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["145_repository_setup_script.sql", "146_run_sync_history.sql", "147_events_session_github_reads_idx.sql", "148_events_session_github_reads_idx.sql", "149_events_task_github_reads_idx.sql", "150_events_task_github_reads_idx.sql", "151_recover_github_read_indexes.sql", "152_child_admission.sql", "153_child_results.sql", "154_child_transport.sql", "155_child_output_fence.sql", "156_child_results_child_idx.sql", "157_task_lifecycle.sql", "158_task_lifecycle_rules.sql", "159_session_attachments.sql", "160_session_child_results.sql", "161_runs_session_creation_key_idx.sql", "162_recover_runs_session_creation_key_idx.sql", "163_events_session_github_reads_v2_idx.sql", "164_events_task_github_reads_v2_idx.sql", "165_drop_events_session_github_reads_idx.sql", "166_drop_events_task_github_reads_idx.sql", "167_recover_github_read_v2_indexes.sql", "168_events_run_chat_idx.sql", "169_recover_events_run_chat_idx.sql", "170_child_results_verify.sql", "171_run_web.sql", "172_fake_checks_models.sql", "173_run_continues.sql", "174_finding_acceptance.sql", "175_review_questions.sql", "176_guide_sections.sql", "177_fake_checks_waiver_models.sql", "178_events_run_checks_decided_idx.sql", "179_escalation_wake_lines.sql", "180_run_check_logs.sql", "181_acceptance_carried.sql", "182_events_task_test_skipped_idx.sql", "183_automations.sql", "184_events_asks_v2_idx.sql", "185_drop_events_asks_idx.sql", "186_events_automation_github_reads_idx.sql", "190_org_sessions.sql", "191_events_session_mentioned_idx.sql", "192_session_owner_sweep.sql", "193_events_session_owner_changes_idx.sql", "194_session_owner_attempts.sql", "195_chat.sql", "196_chat_channel_projects.sql", "197_events_session_tool_attempts_idx.sql", "198_events_task_tool_attempts_idx.sql", "199_events_automation_tool_attempts_idx.sql", "200_drop_events_session_github_reads_v2_idx.sql", "201_drop_events_task_github_reads_v2_idx.sql", "202_drop_events_automation_github_reads_idx.sql", "203_task_pull_request.sql", "210_organization_secrets.sql", "211_fake_checks_base_shown_model.sql", "212_incoming_webhooks.sql", "213_chat_link_offers.sql", "214_chat_request_failure.sql", "215_chat_request_failure_backfill.sql", "216_attachment_storage_keys.sql", "217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
    expect((await sql`SELECT setup_script FROM repositories WHERE id = 'repo_145'`)[0]?.setup_script).toBeNull();
    const atLimit = "x".repeat(16_384);
    await sql`UPDATE repositories SET setup_script = ${atLimit} WHERE id = 'repo_145'`;
    expect((await sql`SELECT octet_length(setup_script) AS bytes FROM repositories WHERE id = 'repo_145'`)[0]?.bytes).toBe(16_384);
    await expect((async () => sql`UPDATE repositories SET setup_script = ${atLimit + "x"} WHERE id = 'repo_145'`)())
      .rejects.toThrow(/repositories_setup_script_size/);
  } finally {
    await sql.end();
  }
}, 120_000);

test("GitHub read migrations recover interrupted concurrent builds and replay completed builds", async () => {
  for (const [version, name] of [["148", "events_session_github_reads_idx"], ["150", "events_task_github_reads_idx"]] as const) {
    const { url, sql } = await migratedBefore(version);
    const builder = await sql.reserve();
    const holder = await sql.reserve();
    try {
      const file = (await listMigrationFiles()).find((f) => f.version === version)!;
      await holder`BEGIN ISOLATION LEVEL REPEATABLE READ`;
      await holder`SELECT count(*) FROM events`;
      await builder`SET statement_timeout = '500ms'`;
      const refusal = async (q: () => Promise<unknown>) => q().then(() => "", (err: Error) => err.message);
      expect(await refusal(async () => await builder.unsafe(await file.contents()))).toContain("statement timeout");
      await holder`COMMIT`;
      const validity = async () => [...await sql`SELECT indisvalid AS valid FROM pg_index WHERE indexrelid = to_regclass(${name})`];
      expect(await validity()).toEqual([{ valid: false }]);

      await applyBefore(sql, "152");
      expect(await validity()).toEqual([{ valid: true }]);
      await sql`DELETE FROM schema_migrations WHERE version = ${version}`;
      expect(await applyBefore(sql, "152")).toEqual([file.name]);
      const replayName = version === "148" ? "events_session_github_reads_idx" : "events_task_github_reads_idx";
      const replayIndex = async () => [...await sql`SELECT indisvalid AS valid FROM pg_index WHERE indexrelid = to_regclass(${replayName})`];
      expect(await replayIndex()).toEqual([{ valid: true }]);
    } finally {
      await holder`ROLLBACK`;
      builder.release();
      holder.release();
      await sql.end();
    }
  }
}, 120_000);

test("163, 164 and 168 build their events indexes outside a transaction", async () => {
  for (const version of ["163", "164", "168", "197", "198", "199"]) {
    const file = (await listMigrationFiles()).find((f) => f.version === version);
    expect(file && outsideTransaction(await file.contents())).toBe(true);
  }
});

test("a concurrent index build cancelled under the migrator is dropped and not recorded; the next migrate builds it, then the old index goes", async () => {
  const { url, sql } = await migratedBefore("197");
  const holder = await sql.reserve();
  const indexes = async () => [...await sql`SELECT c.relname AS name, i.indisvalid AS valid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
    WHERE c.relname IN ('events_session_tool_attempts_idx', 'events_session_github_reads_v2_idx') ORDER BY 1`];
  try {
    // An open snapshot holds the concurrent build in its wait; it is
    // cancelled there, as a deploy's timeout would.
    await holder`BEGIN ISOLATION LEVEL REPEATABLE READ`;
    await holder`SELECT count(*) FROM events`;
    const migrating = migrate(url, { log: () => {} }).then(() => "", (err: Error) => err.message);
    let pid: number | undefined;
    for (let i = 0; i < 200 && pid === undefined; i++) {
      const [row] = await admin`SELECT pid FROM pg_stat_activity WHERE datname = ${new URL(url).pathname.slice(1)}
        AND query LIKE '%CREATE INDEX CONCURRENTLY%events_session_tool_attempts_idx%'`;
      pid = row?.pid;
      if (pid === undefined) await Bun.sleep(50);
    }
    expect(pid).toBeDefined();
    await admin`SELECT pg_cancel_backend(${pid!})`;
    // The cancelled build's invalid index is dropped concurrently, which
    // waits out the snapshot too.
    await Bun.sleep(200);
    await holder`COMMIT`;
    expect(await migrating).toContain("canceling statement");
    expect((await recorded(url)).map((m) => m.version)).not.toContain("197");
    expect(await indexes()).toEqual([{ name: "events_session_github_reads_v2_idx", valid: true }]);

    expect((await migrate(url, { log: () => {} })).applied).toEqual(["197_events_session_tool_attempts_idx.sql",
      "198_events_task_tool_attempts_idx.sql", "199_events_automation_tool_attempts_idx.sql", "200_drop_events_session_github_reads_v2_idx.sql",
      "201_drop_events_task_github_reads_v2_idx.sql", "202_drop_events_automation_github_reads_idx.sql", "203_task_pull_request.sql", "210_organization_secrets.sql", "211_fake_checks_base_shown_model.sql", "212_incoming_webhooks.sql", "213_chat_link_offers.sql", "214_chat_request_failure.sql", "215_chat_request_failure_backfill.sql", "216_attachment_storage_keys.sql", "217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
    expect(await indexes()).toEqual([{ name: "events_session_tool_attempts_idx", valid: true }]);
  } finally {
    holder.release();
    await sql.end();
  }
}, 120_000);

test("an invalid index an earlier interrupted build left is dropped and built afresh, never skipped by IF NOT EXISTS", async () => {
  const { url, sql } = await migratedBefore("197");
  const builder = await sql.reserve();
  const holder = await sql.reserve();
  try {
    const file = (await listMigrationFiles()).find((f) => f.version === "198")!;
    await holder`BEGIN ISOLATION LEVEL REPEATABLE READ`;
    await holder`SELECT count(*) FROM events`;
    await builder`SET statement_timeout = '500ms'`;
    const refusal = async (q: () => Promise<unknown>) => q().then(() => "", (err: Error) => err.message);
    expect(await refusal(async () => await builder.unsafe(await file.contents()))).toContain("statement timeout");
    await holder`COMMIT`;
    const validity = async () => [...await sql`SELECT indisvalid AS valid FROM pg_index WHERE indexrelid = to_regclass('events_task_tool_attempts_idx')`];
    expect(await validity()).toEqual([{ valid: false }]);

    expect((await migrate(url, { log: () => {} })).applied).toContain("198_events_task_tool_attempts_idx.sql");
    expect(await validity()).toEqual([{ valid: true }]);
    expect([...await sql`SELECT count(*)::int AS n FROM pg_class WHERE relname IN ('events_session_github_reads_v2_idx',
      'events_task_github_reads_v2_idx', 'events_automation_github_reads_idx')`]).toEqual([{ n: 0 }]);
  } finally {
    await holder`ROLLBACK`;
    builder.release();
    holder.release();
    await sql.end();
  }
}, 120_000);

test("a migrate retried while an earlier build of its index still runs refuses, dropping nothing; once it finishes, migrate records it", async () => {
  const { url, sql } = await migratedBefore("198");
  const builder = await sql.reserve();
  const holder = await sql.reserve();
  const validity = async () => [...await sql`SELECT indisvalid AS valid FROM pg_index WHERE indexrelid = to_regclass('events_task_tool_attempts_idx')`];
  try {
    // An open snapshot holds the build, as a killed client's backend that
    // builds on until it next writes to its socket.
    await holder`BEGIN ISOLATION LEVEL REPEATABLE READ`;
    await holder`SELECT count(*) FROM events`;
    const file = (await listMigrationFiles()).find((f) => f.version === "198")!;
    const building = builder.unsafe(await file.contents()).then(() => "", (err: Error) => err.message);
    let running = false;
    for (let i = 0; i < 200 && !running; i++) {
      const [row] = await sql`SELECT EXISTS (SELECT 1 FROM pg_stat_progress_create_index
        WHERE index_relid = to_regclass('events_task_tool_attempts_idx')) AS running`;
      running = row?.running === true;
      if (!running) await Bun.sleep(50);
    }
    expect(running).toBe(true);
    const refusal = await migrate(url, { log: () => {} }).then(() => "", (err: Error) => err.message);
    expect(refusal).toBe("a build of events_task_tool_attempts_idx is still running; migrate again when it finishes");
    expect(await validity()).toEqual([{ valid: false }]);
    expect((await recorded(url)).map((m) => m.version)).not.toContain("198");

    await holder`COMMIT`;
    expect(await building).toBe("");
    expect(await validity()).toEqual([{ valid: true }]);
    expect((await migrate(url, { log: () => {} })).applied).toContain("198_events_task_tool_attempts_idx.sql");
    expect(await validity()).toEqual([{ valid: true }]);
  } finally {
    builder.release();
    holder.release();
    await sql.end();
  }
}, 120_000);

test("a migrate whose connection drops while its backend still builds the index leaves the build alone and records nothing", async () => {
  const { url, sql } = await migratedBefore("198");
  // The migrator reaches Postgres through a relay. The building statement's
  // client side is cut while its server side stays open: Postgres keeps
  // building, as a backend does whose client vanished without a FIN reaching it.
  const pairs: { client: Socket; server: Socket; building: boolean }[] = [];
  const target = new URL(url);
  const relay = createServer((client) => {
    const server = connect(Number(target.port || 5432), target.hostname);
    const pair = { client, server, building: false };
    client.on("data", (b: Buffer) => { pair.building ||= b.includes("CREATE INDEX CONCURRENTLY"); });
    client.pipe(server);
    server.pipe(client);
    client.on("error", () => {});
    server.on("error", () => {});
    pairs.push(pair);
  });
  await new Promise<void>((resolve) => relay.listen(0, "127.0.0.1", resolve));
  const relayed = new URL(url);
  relayed.host = `127.0.0.1:${(relay.address() as AddressInfo).port}`;
  const holder = await sql.reserve();
  const validity = async () => [...await sql`SELECT indisvalid AS valid FROM pg_index WHERE indexrelid = to_regclass('events_task_tool_attempts_idx')`];
  try {
    // An open snapshot holds the build in its wait.
    await holder`BEGIN ISOLATION LEVEL REPEATABLE READ`;
    await holder`SELECT count(*) FROM events`;
    const migrating = migrate(relayed.toString(), { log: () => {} }).then(() => "", (err: Error) => err.message);
    let builder: number | undefined;
    for (let i = 0; i < 200 && builder === undefined; i++) {
      const [row] = await sql`SELECT EXISTS (SELECT 1 FROM pg_stat_progress_create_index
        WHERE index_relid = to_regclass('events_task_tool_attempts_idx')) AS running`;
      if (row?.running === true) builder = pairs.findIndex((p) => p.building);
      if (builder === undefined) await Bun.sleep(50);
    }
    expect(builder).toBeGreaterThanOrEqual(0);
    // Only the building statement's connection is cut; the migrator's
    // other pooled connections stay up for what it does next.
    const cut = pairs[builder!]!;
    cut.server.unpipe(cut.client);
    cut.client.destroy();
    // A drop would wait on the held build; the migrator must settle without one.
    const settled = await Promise.race([migrating, Bun.sleep(10_000).then(() => null)]);
    expect(settled).not.toBeNull();
    expect(settled).not.toBe("");
    expect(await validity()).toEqual([{ valid: false }]);
    expect([...await sql`SELECT EXISTS (SELECT 1 FROM pg_stat_progress_create_index
      WHERE index_relid = to_regclass('events_task_tool_attempts_idx')) AS running`]).toEqual([{ running: true }]);
    expect((await recorded(url)).map((m) => m.version)).not.toContain("198");

    await holder`COMMIT`;
    for (let i = 0; i < 200 && (await validity())[0]?.valid !== true; i++) await Bun.sleep(50);
    expect(await validity()).toEqual([{ valid: true }]);
    expect((await migrate(url, { log: () => {} })).applied).toContain("198_events_task_tool_attempts_idx.sql");
    expect(await validity()).toEqual([{ valid: true }]);
  } finally {
    holder.release();
    for (const { client, server } of pairs) {
      client.destroy();
      server.destroy();
    }
    relay.close();
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
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["110_repository_github_host.sql", "111_task_relations.sql", "112_people_github.sql", "113_run_children.sql", "114_preview_wake_timeouts.sql", "115_agent_coordination.sql", "116_peer_ended.sql", "117_events_agent_message_idx.sql", "118_accounts.sql", "119_github_owner_oauth.sql", "120_agent_machine_moves.sql", "121_pull_request_rerequests.sql", "122_push_per_organization.sql", "123_acceptance_verdicts.sql", "124_task_sources.sql", "125_task_source_mode.sql", "126_fake_done_model.sql", "127_feedback.sql", "128_project_secret_recipients.sql", "129_run_project_secrets.sql", "130_run_secret_values.sql", "131_fake_secret_models.sql", "132_run_synced_bases.sql", "133_feedback_pictures_kept.sql", "134_review_range.sql", "135_events_session_github_reads_idx.sql", "136_events_task_github_reads_idx.sql", "137_events_fix_merge_attempted_idx.sql", "138_events_git_merge_attempted_idx.sql", "139_run_retry_backoff.sql", "140_tasks_id_org_idx.sql", "141_model_tiers_id_org_idx.sql", "142_model_calls.sql", "143_checks_pending_since.sql", "144_image_layers.sql", "145_repository_setup_script.sql", "146_run_sync_history.sql", "147_events_session_github_reads_idx.sql", "148_events_session_github_reads_idx.sql", "149_events_task_github_reads_idx.sql", "150_events_task_github_reads_idx.sql", "151_recover_github_read_indexes.sql", "152_child_admission.sql", "153_child_results.sql", "154_child_transport.sql", "155_child_output_fence.sql", "156_child_results_child_idx.sql", "157_task_lifecycle.sql", "158_task_lifecycle_rules.sql", "159_session_attachments.sql", "160_session_child_results.sql", "161_runs_session_creation_key_idx.sql", "162_recover_runs_session_creation_key_idx.sql", "163_events_session_github_reads_v2_idx.sql", "164_events_task_github_reads_v2_idx.sql", "165_drop_events_session_github_reads_idx.sql", "166_drop_events_task_github_reads_idx.sql", "167_recover_github_read_v2_indexes.sql", "168_events_run_chat_idx.sql", "169_recover_events_run_chat_idx.sql", "170_child_results_verify.sql", "171_run_web.sql", "172_fake_checks_models.sql", "173_run_continues.sql", "174_finding_acceptance.sql", "175_review_questions.sql", "176_guide_sections.sql", "177_fake_checks_waiver_models.sql", "178_events_run_checks_decided_idx.sql", "179_escalation_wake_lines.sql", "180_run_check_logs.sql", "181_acceptance_carried.sql", "182_events_task_test_skipped_idx.sql", "183_automations.sql", "184_events_asks_v2_idx.sql", "185_drop_events_asks_idx.sql", "186_events_automation_github_reads_idx.sql", "190_org_sessions.sql", "191_events_session_mentioned_idx.sql", "192_session_owner_sweep.sql", "193_events_session_owner_changes_idx.sql", "194_session_owner_attempts.sql", "195_chat.sql", "196_chat_channel_projects.sql", "197_events_session_tool_attempts_idx.sql", "198_events_task_tool_attempts_idx.sql", "199_events_automation_tool_attempts_idx.sql", "200_drop_events_session_github_reads_v2_idx.sql", "201_drop_events_task_github_reads_v2_idx.sql", "202_drop_events_automation_github_reads_idx.sql", "203_task_pull_request.sql", "210_organization_secrets.sql", "211_fake_checks_base_shown_model.sql", "212_incoming_webhooks.sql", "213_chat_link_offers.sql", "214_chat_request_failure.sql", "215_chat_request_failure_backfill.sql", "216_attachment_storage_keys.sql", "217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
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
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["111_task_relations.sql", "112_people_github.sql", "113_run_children.sql", "114_preview_wake_timeouts.sql", "115_agent_coordination.sql", "116_peer_ended.sql", "117_events_agent_message_idx.sql", "118_accounts.sql", "119_github_owner_oauth.sql", "120_agent_machine_moves.sql", "121_pull_request_rerequests.sql", "122_push_per_organization.sql", "123_acceptance_verdicts.sql", "124_task_sources.sql", "125_task_source_mode.sql", "126_fake_done_model.sql", "127_feedback.sql", "128_project_secret_recipients.sql", "129_run_project_secrets.sql", "130_run_secret_values.sql", "131_fake_secret_models.sql", "132_run_synced_bases.sql", "133_feedback_pictures_kept.sql", "134_review_range.sql", "135_events_session_github_reads_idx.sql", "136_events_task_github_reads_idx.sql", "137_events_fix_merge_attempted_idx.sql", "138_events_git_merge_attempted_idx.sql", "139_run_retry_backoff.sql", "140_tasks_id_org_idx.sql", "141_model_tiers_id_org_idx.sql", "142_model_calls.sql", "143_checks_pending_since.sql", "144_image_layers.sql", "145_repository_setup_script.sql", "146_run_sync_history.sql", "147_events_session_github_reads_idx.sql", "148_events_session_github_reads_idx.sql", "149_events_task_github_reads_idx.sql", "150_events_task_github_reads_idx.sql", "151_recover_github_read_indexes.sql", "152_child_admission.sql", "153_child_results.sql", "154_child_transport.sql", "155_child_output_fence.sql", "156_child_results_child_idx.sql", "157_task_lifecycle.sql", "158_task_lifecycle_rules.sql", "159_session_attachments.sql", "160_session_child_results.sql", "161_runs_session_creation_key_idx.sql", "162_recover_runs_session_creation_key_idx.sql", "163_events_session_github_reads_v2_idx.sql", "164_events_task_github_reads_v2_idx.sql", "165_drop_events_session_github_reads_idx.sql", "166_drop_events_task_github_reads_idx.sql", "167_recover_github_read_v2_indexes.sql", "168_events_run_chat_idx.sql", "169_recover_events_run_chat_idx.sql", "170_child_results_verify.sql", "171_run_web.sql", "172_fake_checks_models.sql", "173_run_continues.sql", "174_finding_acceptance.sql", "175_review_questions.sql", "176_guide_sections.sql", "177_fake_checks_waiver_models.sql", "178_events_run_checks_decided_idx.sql", "179_escalation_wake_lines.sql", "180_run_check_logs.sql", "181_acceptance_carried.sql", "182_events_task_test_skipped_idx.sql", "183_automations.sql", "184_events_asks_v2_idx.sql", "185_drop_events_asks_idx.sql", "186_events_automation_github_reads_idx.sql", "190_org_sessions.sql", "191_events_session_mentioned_idx.sql", "192_session_owner_sweep.sql", "193_events_session_owner_changes_idx.sql", "194_session_owner_attempts.sql", "195_chat.sql", "196_chat_channel_projects.sql", "197_events_session_tool_attempts_idx.sql", "198_events_task_tool_attempts_idx.sql", "199_events_automation_tool_attempts_idx.sql", "200_drop_events_session_github_reads_v2_idx.sql", "201_drop_events_task_github_reads_v2_idx.sql", "202_drop_events_automation_github_reads_idx.sql", "203_task_pull_request.sql", "210_organization_secrets.sql", "211_fake_checks_base_shown_model.sql", "212_incoming_webhooks.sql", "213_chat_link_offers.sql", "214_chat_request_failure.sql", "215_chat_request_failure_backfill.sql", "216_attachment_storage_keys.sql", "217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
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
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["111_task_relations.sql", "112_people_github.sql", "113_run_children.sql", "114_preview_wake_timeouts.sql", "115_agent_coordination.sql", "116_peer_ended.sql", "117_events_agent_message_idx.sql", "118_accounts.sql", "119_github_owner_oauth.sql", "120_agent_machine_moves.sql", "121_pull_request_rerequests.sql", "122_push_per_organization.sql", "123_acceptance_verdicts.sql", "124_task_sources.sql", "125_task_source_mode.sql", "126_fake_done_model.sql", "127_feedback.sql", "128_project_secret_recipients.sql", "129_run_project_secrets.sql", "130_run_secret_values.sql", "131_fake_secret_models.sql", "132_run_synced_bases.sql", "133_feedback_pictures_kept.sql", "134_review_range.sql", "135_events_session_github_reads_idx.sql", "136_events_task_github_reads_idx.sql", "137_events_fix_merge_attempted_idx.sql", "138_events_git_merge_attempted_idx.sql", "139_run_retry_backoff.sql", "140_tasks_id_org_idx.sql", "141_model_tiers_id_org_idx.sql", "142_model_calls.sql", "143_checks_pending_since.sql", "144_image_layers.sql", "145_repository_setup_script.sql", "146_run_sync_history.sql", "147_events_session_github_reads_idx.sql", "148_events_session_github_reads_idx.sql", "149_events_task_github_reads_idx.sql", "150_events_task_github_reads_idx.sql", "151_recover_github_read_indexes.sql", "152_child_admission.sql", "153_child_results.sql", "154_child_transport.sql", "155_child_output_fence.sql", "156_child_results_child_idx.sql", "157_task_lifecycle.sql", "158_task_lifecycle_rules.sql", "159_session_attachments.sql", "160_session_child_results.sql", "161_runs_session_creation_key_idx.sql", "162_recover_runs_session_creation_key_idx.sql", "163_events_session_github_reads_v2_idx.sql", "164_events_task_github_reads_v2_idx.sql", "165_drop_events_session_github_reads_idx.sql", "166_drop_events_task_github_reads_idx.sql", "167_recover_github_read_v2_indexes.sql", "168_events_run_chat_idx.sql", "169_recover_events_run_chat_idx.sql", "170_child_results_verify.sql", "171_run_web.sql", "172_fake_checks_models.sql", "173_run_continues.sql", "174_finding_acceptance.sql", "175_review_questions.sql", "176_guide_sections.sql", "177_fake_checks_waiver_models.sql", "178_events_run_checks_decided_idx.sql", "179_escalation_wake_lines.sql", "180_run_check_logs.sql", "181_acceptance_carried.sql", "182_events_task_test_skipped_idx.sql", "183_automations.sql", "184_events_asks_v2_idx.sql", "185_drop_events_asks_idx.sql", "186_events_automation_github_reads_idx.sql", "190_org_sessions.sql", "191_events_session_mentioned_idx.sql", "192_session_owner_sweep.sql", "193_events_session_owner_changes_idx.sql", "194_session_owner_attempts.sql", "195_chat.sql", "196_chat_channel_projects.sql", "197_events_session_tool_attempts_idx.sql", "198_events_task_tool_attempts_idx.sql", "199_events_automation_tool_attempts_idx.sql", "200_drop_events_session_github_reads_v2_idx.sql", "201_drop_events_task_github_reads_v2_idx.sql", "202_drop_events_automation_github_reads_idx.sql", "203_task_pull_request.sql", "210_organization_secrets.sql", "211_fake_checks_base_shown_model.sql", "212_incoming_webhooks.sql", "213_chat_link_offers.sql", "214_chat_request_failure.sql", "215_chat_request_failure_backfill.sql", "216_attachment_storage_keys.sql", "217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
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

    expect((await migrate(url, { log: () => {} })).applied).toEqual(["115_agent_coordination.sql", "116_peer_ended.sql", "117_events_agent_message_idx.sql", "118_accounts.sql", "119_github_owner_oauth.sql", "120_agent_machine_moves.sql", "121_pull_request_rerequests.sql", "122_push_per_organization.sql", "123_acceptance_verdicts.sql", "124_task_sources.sql", "125_task_source_mode.sql", "126_fake_done_model.sql", "127_feedback.sql", "128_project_secret_recipients.sql", "129_run_project_secrets.sql", "130_run_secret_values.sql", "131_fake_secret_models.sql", "132_run_synced_bases.sql", "133_feedback_pictures_kept.sql", "134_review_range.sql", "135_events_session_github_reads_idx.sql", "136_events_task_github_reads_idx.sql", "137_events_fix_merge_attempted_idx.sql", "138_events_git_merge_attempted_idx.sql", "139_run_retry_backoff.sql", "140_tasks_id_org_idx.sql", "141_model_tiers_id_org_idx.sql", "142_model_calls.sql", "143_checks_pending_since.sql", "144_image_layers.sql", "145_repository_setup_script.sql", "146_run_sync_history.sql", "147_events_session_github_reads_idx.sql", "148_events_session_github_reads_idx.sql", "149_events_task_github_reads_idx.sql", "150_events_task_github_reads_idx.sql", "151_recover_github_read_indexes.sql", "152_child_admission.sql", "153_child_results.sql", "154_child_transport.sql", "155_child_output_fence.sql", "156_child_results_child_idx.sql", "157_task_lifecycle.sql", "158_task_lifecycle_rules.sql", "159_session_attachments.sql", "160_session_child_results.sql", "161_runs_session_creation_key_idx.sql", "162_recover_runs_session_creation_key_idx.sql", "163_events_session_github_reads_v2_idx.sql", "164_events_task_github_reads_v2_idx.sql", "165_drop_events_session_github_reads_idx.sql", "166_drop_events_task_github_reads_idx.sql", "167_recover_github_read_v2_indexes.sql", "168_events_run_chat_idx.sql", "169_recover_events_run_chat_idx.sql", "170_child_results_verify.sql", "171_run_web.sql", "172_fake_checks_models.sql", "173_run_continues.sql", "174_finding_acceptance.sql", "175_review_questions.sql", "176_guide_sections.sql", "177_fake_checks_waiver_models.sql", "178_events_run_checks_decided_idx.sql", "179_escalation_wake_lines.sql", "180_run_check_logs.sql", "181_acceptance_carried.sql", "182_events_task_test_skipped_idx.sql", "183_automations.sql", "184_events_asks_v2_idx.sql", "185_drop_events_asks_idx.sql", "186_events_automation_github_reads_idx.sql", "190_org_sessions.sql", "191_events_session_mentioned_idx.sql", "192_session_owner_sweep.sql", "193_events_session_owner_changes_idx.sql", "194_session_owner_attempts.sql", "195_chat.sql", "196_chat_channel_projects.sql", "197_events_session_tool_attempts_idx.sql", "198_events_task_tool_attempts_idx.sql", "199_events_automation_tool_attempts_idx.sql", "200_drop_events_session_github_reads_v2_idx.sql", "201_drop_events_task_github_reads_v2_idx.sql", "202_drop_events_automation_github_reads_idx.sql", "203_task_pull_request.sql", "210_organization_secrets.sql", "211_fake_checks_base_shown_model.sql", "212_incoming_webhooks.sql", "213_chat_link_offers.sql", "214_chat_request_failure.sql", "215_chat_request_failure_backfill.sql", "216_attachment_storage_keys.sql", "217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
    expect((await recorded(url)).map((m) => m.name).slice(-92)).toEqual([
      "128_project_secret_recipients.sql", "129_run_project_secrets.sql", "130_run_secret_values.sql", "131_fake_secret_models.sql", "132_run_synced_bases.sql", "133_feedback_pictures_kept.sql", "134_review_range.sql", "135_events_session_github_reads_idx.sql", "136_events_task_github_reads_idx.sql", "137_events_fix_merge_attempted_idx.sql", "138_events_git_merge_attempted_idx.sql", "139_run_retry_backoff.sql", "140_tasks_id_org_idx.sql", "141_model_tiers_id_org_idx.sql", "142_model_calls.sql", "143_checks_pending_since.sql", "144_image_layers.sql", "145_repository_setup_script.sql", "146_run_sync_history.sql", "147_events_session_github_reads_idx.sql", "148_events_session_github_reads_idx.sql", "149_events_task_github_reads_idx.sql", "150_events_task_github_reads_idx.sql", "151_recover_github_read_indexes.sql", "152_child_admission.sql", "153_child_results.sql", "154_child_transport.sql", "155_child_output_fence.sql", "156_child_results_child_idx.sql", "157_task_lifecycle.sql", "158_task_lifecycle_rules.sql", "159_session_attachments.sql", "160_session_child_results.sql", "161_runs_session_creation_key_idx.sql", "162_recover_runs_session_creation_key_idx.sql", "163_events_session_github_reads_v2_idx.sql", "164_events_task_github_reads_v2_idx.sql", "165_drop_events_session_github_reads_idx.sql", "166_drop_events_task_github_reads_idx.sql", "167_recover_github_read_v2_indexes.sql", "168_events_run_chat_idx.sql", "169_recover_events_run_chat_idx.sql", "170_child_results_verify.sql", "171_run_web.sql", "172_fake_checks_models.sql", "173_run_continues.sql", "174_finding_acceptance.sql", "175_review_questions.sql", "176_guide_sections.sql", "177_fake_checks_waiver_models.sql", "178_events_run_checks_decided_idx.sql", "179_escalation_wake_lines.sql", "180_run_check_logs.sql", "181_acceptance_carried.sql", "182_events_task_test_skipped_idx.sql", "183_automations.sql", "184_events_asks_v2_idx.sql", "185_drop_events_asks_idx.sql", "186_events_automation_github_reads_idx.sql", "190_org_sessions.sql", "191_events_session_mentioned_idx.sql", "192_session_owner_sweep.sql", "193_events_session_owner_changes_idx.sql", "194_session_owner_attempts.sql", "195_chat.sql", "196_chat_channel_projects.sql", "197_events_session_tool_attempts_idx.sql", "198_events_task_tool_attempts_idx.sql", "199_events_automation_tool_attempts_idx.sql", "200_drop_events_session_github_reads_v2_idx.sql", "201_drop_events_task_github_reads_v2_idx.sql", "202_drop_events_automation_github_reads_idx.sql", "203_task_pull_request.sql", "210_organization_secrets.sql", "211_fake_checks_base_shown_model.sql", "212_incoming_webhooks.sql", "213_chat_link_offers.sql", "214_chat_request_failure.sql", "215_chat_request_failure_backfill.sql", "216_attachment_storage_keys.sql", "217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
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
    const [childIndex] = await sql`SELECT i.indisvalid AS valid, a.attname AS column FROM pg_index i
      JOIN pg_class c ON c.oid = i.indexrelid JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = i.indkey[0]
      WHERE c.relname = 'child_results_child_idx'`;
    expect(childIndex).toEqual({ valid: true, column: "child_run_id" });
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
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["120_agent_machine_moves.sql", "121_pull_request_rerequests.sql", "122_push_per_organization.sql", "123_acceptance_verdicts.sql", "124_task_sources.sql", "125_task_source_mode.sql", "126_fake_done_model.sql", "127_feedback.sql", "128_project_secret_recipients.sql", "129_run_project_secrets.sql", "130_run_secret_values.sql", "131_fake_secret_models.sql", "132_run_synced_bases.sql", "133_feedback_pictures_kept.sql", "134_review_range.sql", "135_events_session_github_reads_idx.sql", "136_events_task_github_reads_idx.sql", "137_events_fix_merge_attempted_idx.sql", "138_events_git_merge_attempted_idx.sql", "139_run_retry_backoff.sql", "140_tasks_id_org_idx.sql", "141_model_tiers_id_org_idx.sql", "142_model_calls.sql", "143_checks_pending_since.sql", "144_image_layers.sql", "145_repository_setup_script.sql", "146_run_sync_history.sql", "147_events_session_github_reads_idx.sql", "148_events_session_github_reads_idx.sql", "149_events_task_github_reads_idx.sql", "150_events_task_github_reads_idx.sql", "151_recover_github_read_indexes.sql", "152_child_admission.sql", "153_child_results.sql", "154_child_transport.sql", "155_child_output_fence.sql", "156_child_results_child_idx.sql", "157_task_lifecycle.sql", "158_task_lifecycle_rules.sql", "159_session_attachments.sql", "160_session_child_results.sql", "161_runs_session_creation_key_idx.sql", "162_recover_runs_session_creation_key_idx.sql", "163_events_session_github_reads_v2_idx.sql", "164_events_task_github_reads_v2_idx.sql", "165_drop_events_session_github_reads_idx.sql", "166_drop_events_task_github_reads_idx.sql", "167_recover_github_read_v2_indexes.sql", "168_events_run_chat_idx.sql", "169_recover_events_run_chat_idx.sql", "170_child_results_verify.sql", "171_run_web.sql", "172_fake_checks_models.sql", "173_run_continues.sql", "174_finding_acceptance.sql", "175_review_questions.sql", "176_guide_sections.sql", "177_fake_checks_waiver_models.sql", "178_events_run_checks_decided_idx.sql", "179_escalation_wake_lines.sql", "180_run_check_logs.sql", "181_acceptance_carried.sql", "182_events_task_test_skipped_idx.sql", "183_automations.sql", "184_events_asks_v2_idx.sql", "185_drop_events_asks_idx.sql", "186_events_automation_github_reads_idx.sql", "190_org_sessions.sql", "191_events_session_mentioned_idx.sql", "192_session_owner_sweep.sql", "193_events_session_owner_changes_idx.sql", "194_session_owner_attempts.sql", "195_chat.sql", "196_chat_channel_projects.sql", "197_events_session_tool_attempts_idx.sql", "198_events_task_tool_attempts_idx.sql", "199_events_automation_tool_attempts_idx.sql", "200_drop_events_session_github_reads_v2_idx.sql", "201_drop_events_task_github_reads_v2_idx.sql", "202_drop_events_automation_github_reads_idx.sql", "203_task_pull_request.sql", "210_organization_secrets.sql", "211_fake_checks_base_shown_model.sql", "212_incoming_webhooks.sql", "213_chat_link_offers.sql", "214_chat_request_failure.sql", "215_chat_request_failure_backfill.sql", "216_attachment_storage_keys.sql", "217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
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

test("179 lets an escalation's wake line run to 1200 characters, keeping every other kind's bound and the rows before it", async () => {
  const { url, sql } = await migratedBefore("179");
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_w', 'w', 'w')`;
    await sql`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_w', 'org_w', 'W', 'w', 'W')`;
    await sql`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('wi_w', 'org_w', 'prj_w', 1, 'T')`;
    const wake = (key: string, kind: string, chars: number) =>
      sql`INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line)
        VALUES (${"cwk_" + key}, 'org_w', 'wi_w', ${kind}, ${key}, ${"é".repeat(chars)})`.then(() => "inserted", (e: Error) => e.message);
    expect(await wake("old", "escalation", 300)).toBe("inserted");
    expect(await wake("old-long", "escalation", 301)).toContain("conductor_wakes_line_check");

    // 179's bound, over 179 up to 219, which lifts it for escalations.
    expect(await applyBefore(sql, "219")).toEqual(["179_escalation_wake_lines.sql", "180_run_check_logs.sql", "181_acceptance_carried.sql", "182_events_task_test_skipped_idx.sql", "183_automations.sql", "184_events_asks_v2_idx.sql", "185_drop_events_asks_idx.sql", "186_events_automation_github_reads_idx.sql", "190_org_sessions.sql", "191_events_session_mentioned_idx.sql", "192_session_owner_sweep.sql", "193_events_session_owner_changes_idx.sql", "194_session_owner_attempts.sql", "195_chat.sql", "196_chat_channel_projects.sql", "197_events_session_tool_attempts_idx.sql", "198_events_task_tool_attempts_idx.sql", "199_events_automation_tool_attempts_idx.sql", "200_drop_events_session_github_reads_v2_idx.sql", "201_drop_events_task_github_reads_v2_idx.sql", "202_drop_events_automation_github_reads_idx.sql", "203_task_pull_request.sql", "210_organization_secrets.sql", "211_fake_checks_base_shown_model.sql", "212_incoming_webhooks.sql", "213_chat_link_offers.sql", "214_chat_request_failure.sql", "215_chat_request_failure_backfill.sql", "216_attachment_storage_keys.sql", "217_automation_run_events.sql", "218_events_automation_run_idx.sql"]);
    expect([...await sql`SELECT key, length(line) AS chars FROM conductor_wakes`]).toEqual([{ key: "old", chars: 300 }]);
    expect(await wake("at-bound", "escalation", 1200)).toBe("inserted");
    expect(await wake("past-bound", "escalation", 1201)).toContain("conductor_wakes_line_check");
    expect(await wake("decision", "decision", 301)).toContain("conductor_wakes_line_check");
    expect(await wake("stalled", "stalled", 2000)).toBe("inserted");
    expect(await wake("stalled-long", "stalled", 2001)).toContain("conductor_wakes_line_check");
    // Added unvalidated: the bound only widens 090's, so no row before it can break it.
    expect([...await sql`SELECT convalidated FROM pg_constraint WHERE conname = 'conductor_wakes_line_check'`])
      .toEqual([{ convalidated: false }]);
    // 219 carries a person's complete words: no bound on an escalation's
    // line, the other kinds' as they were.
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
    expect(await wake("unbounded", "escalation", 5000)).toBe("inserted");
    expect(await wake("decision-after", "decision", 301)).toContain("conductor_wakes_line_check");
    expect(await wake("stalled-after", "stalled", 2001)).toContain("conductor_wakes_line_check");
  } finally {
    await sql.end();
  }
}, 120_000);

test("232 adds conductor waits, one open per task, and a wait's wake line of up to 2000 characters", async () => {
  const { url, sql } = await migratedBefore("232");
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_cw', 'cw', 'cw')`;
    await sql`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_cw', 'org_cw', 'C', 'c', 'C')`;
    await sql`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('wi_cw', 'org_cw', 'prj_cw', 1, 'T')`;
    await sql`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role) VALUES ('run_cw', 'org_cw', 'prj_cw', 'wi_cw', 1, 'running', 'conductor')`;
    const wake = (key: string, kind: string, chars: number) =>
      sql`INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line)
        VALUES (${"cwk_" + key}, 'org_cw', 'wi_cw', ${kind}, ${key}, ${"é".repeat(chars)})`.then(() => "inserted", (e: Error) => e.message);
    expect(await wake("wait-before", "wait", 10)).toContain("conductor_wakes_kind_check");

    expect((await migrate(url, { log: () => {} })).applied).toEqual(["232_conductor_waits.sql", "233_run_recoveries.sql"]);
    expect(await wake("wait", "wait", 2000)).toBe("inserted");
    expect(await wake("wait-long", "wait", 2001)).toContain("conductor_wakes_line_check");
    expect(await wake("decision", "decision", 301)).toContain("conductor_wakes_line_check");
    expect(await wake("stalled", "stalled", 2001)).toContain("conductor_wakes_line_check");
    expect(await wake("unbounded", "escalation", 5000)).toBe("inserted");

    const wait = (id: string, key: string, ended: boolean) =>
      sql`INSERT INTO conductor_waits (id, organization_id, task_id, conductor_run_id, attempt, conditions, deadline_at, since_cursor, call_key, ended_at, ended_by)
        VALUES (${id}, 'org_cw', 'wi_cw', 'run_cw', 1, '[]', now() + interval '1 hour', 0, ${key},
          ${ended ? new Date() : null}, ${ended ? "replaced" : null})`.then(() => "inserted", (e: Error) => e.message);
    expect(await wait("cwt_1", "call_1", true)).toBe("inserted");
    expect(await wait("cwt_2", "call_2", false)).toBe("inserted");
    // One open wait per task; a call key once per conductor.
    expect(await wait("cwt_3", "call_3", false)).toContain("conductor_waits_open_idx");
    expect(await wait("cwt_4", "call_1", true)).toContain("conductor_waits_conductor_run_id_call_key_key");
    expect([...await sql`SELECT relrowsecurity AS rls, relforcerowsecurity AS forced FROM pg_class WHERE relname = 'conductor_waits'`])
      .toEqual([{ rls: true, forced: true }]);
    expect([...await sql`SELECT has_table_privilege('dude_sweeper', 'conductor_waits', 'SELECT') AS sweeper,
      has_table_privilege('dude_app', 'conductor_waits', 'UPDATE') AS app`]).toEqual([{ sweeper: true, app: true }]);
  } finally {
    await sql.end();
  }
}, 120_000);

test("233 adds run recoveries, one per Run and branch, and keeps every conductor wake kind with recovered", async () => {
  const { url, sql } = await migratedBefore("233");
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_rc', 'rc', 'rc')`;
    await sql`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_rc', 'org_rc', 'R', 'r', 'R')`;
    await sql`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('wi_rc', 'org_rc', 'prj_rc', 1, 'T')`;
    await sql`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role)
      VALUES ('run_rc', 'org_rc', 'prj_rc', 'wi_rc', 1, 'failed', 'implement', 'implementer')`;
    const wake = (key: string, kind: string) =>
      sql`INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line)
        VALUES (${"cwk_" + key}, 'org_rc', 'wi_rc', ${kind}, ${key}, 'x')`.then(() => "inserted", (e: Error) => e.message);
    expect(await wake("recovered-before", "recovered")).toContain("conductor_wakes_kind_check");

    expect((await migrate(url, { log: () => {} })).applied).toEqual(["233_run_recoveries.sql"]);
    for (const kind of ["decision", "escalation", "question", "safety", "steer_read", "steer_failed", "pr_merged", "pr_closed",
      "published", "publish_refused", "publish_stalled", "checkout", "stalled", "unblocked", "feedback", "wait", "recovered"]) {
      expect(await wake(kind, kind)).toBe("inserted");
    }
    expect(await wake("nonsense", "nonsense")).toContain("conductor_wakes_kind_check");

    const recovery = (id: string, branch: string, status = "requested") =>
      sql`INSERT INTO run_recoveries (id, organization_id, task_id, run_id, branch, status)
        VALUES (${id}, 'org_rc', 'wi_rc', 'run_rc', ${branch}, ${status})`.then(() => "inserted", (e: Error) => e.message);
    expect(await recovery("rcv_1", "dude/wi_rc/recovered-run_rc")).toBe("inserted");
    expect(await recovery("rcv_2", "dude/wi_rc/recovered-run_rc")).toContain("run_recoveries_run_id_branch_key");
    expect(await recovery("rcv_3", "dude/wi_rc/recovered-other", "running")).toContain("run_recoveries_status_check");
    expect([...await sql`SELECT relrowsecurity AS rls, relforcerowsecurity AS forced FROM pg_class WHERE relname = 'run_recoveries'`])
      .toEqual([{ rls: true, forced: true }]);
    expect([...await sql`SELECT has_table_privilege('dude_sweeper', 'run_recoveries', 'SELECT') AS sweeper,
      has_table_privilege('dude_sweeper', 'run_recoveries', 'INSERT') AS sweeper_inserts,
      has_table_privilege('dude_app', 'run_recoveries', 'UPDATE') AS app`]).toEqual([{ sweeper: true, sweeper_inserts: false, app: true }]);
  } finally {
    await sql.end();
  }
}, 120_000);

test("183 adds automations: expiry 1..30 on resumable only, repo filter on org ones only, reviewer on pull_request events", async () => {
  const { url, sql } = await migratedBefore("183");
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_au', 'au', 'au')`;
    await sql`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_au', 'org_au', 'A', 'a', 'A')`;
    await sql`INSERT INTO webhook_deliveries (id, organization_id, event, payload) VALUES ('d-old', 'org_au', 'pull_request', '{}')`;

    expect((await migrate(url, { log: () => {} })).applied).toEqual(["183_automations.sql", "184_events_asks_v2_idx.sql",
      "185_drop_events_asks_idx.sql", "186_events_automation_github_reads_idx.sql", "190_org_sessions.sql",
      "191_events_session_mentioned_idx.sql", "192_session_owner_sweep.sql", "193_events_session_owner_changes_idx.sql",
      "194_session_owner_attempts.sql", "195_chat.sql", "196_chat_channel_projects.sql", "197_events_session_tool_attempts_idx.sql", "198_events_task_tool_attempts_idx.sql", "199_events_automation_tool_attempts_idx.sql", "200_drop_events_session_github_reads_v2_idx.sql", "201_drop_events_task_github_reads_v2_idx.sql", "202_drop_events_automation_github_reads_idx.sql", "203_task_pull_request.sql", "210_organization_secrets.sql", "211_fake_checks_base_shown_model.sql", "212_incoming_webhooks.sql", "213_chat_link_offers.sql", "214_chat_request_failure.sql", "215_chat_request_failure_backfill.sql", "216_attachment_storage_keys.sql", "217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
    // The asks index is rebuilt with automation.failed, concurrently (184/185): one index, the notifier's predicate.
    expect([...await sql`SELECT indexname, indexdef LIKE '%automation.failed%' AS failed FROM pg_indexes WHERE indexname LIKE 'events_asks%'`])
      .toEqual([{ indexname: "events_asks_v2_idx", failed: true }]);
    // A delivery from before counts as read by the matcher: an old event fires nothing new.
    expect([...await sql`SELECT automations_at IS NOT NULL AS read FROM webhook_deliveries`]).toEqual([{ read: true }]);

    let n = 0;
    const add = (over: Record<string, unknown>) => {
      const a = { project_id: null, kind: "agent", trigger: { kind: "schedule", cron: "0 3 * * *", tz: "UTC" },
        session_mode: "ephemeral", expiry_days: null, concurrency: "queue", repo_filter: null, agent: null, ...over };
      return sql`INSERT INTO automations (id, organization_id, project_id, name, kind, trigger, session_mode, expiry_days,
          concurrency, repo_filter, agent)
        VALUES (${`aut_${++n}`}, 'org_au', ${a.project_id}, 'A', ${a.kind}, ${a.trigger}::jsonb, ${a.session_mode},
          ${a.expiry_days}, ${a.concurrency}, ${a.repo_filter}::jsonb, ${a.agent}::jsonb)`.then(() => "inserted", (e: Error) => e.message);
    };
    expect(await add({})).toBe("inserted");
    expect(await add({ session_mode: "resumable", expiry_days: 30 })).toBe("inserted");
    expect(await add({ session_mode: "resumable", expiry_days: 1 })).toBe("inserted");
    expect(await add({ session_mode: "resumable", expiry_days: 31 })).toContain("automations_expiry_days_check");
    expect(await add({ session_mode: "resumable", expiry_days: 0 })).toContain("automations_expiry_days_check");
    expect(await add({ session_mode: "resumable" })).toContain("automations_expiry_resumable");
    expect(await add({ expiry_days: 7 })).toContain("automations_expiry_resumable");

    const filter = { mode: "only", repos: ["acme/svc-*"] };
    expect(await add({ repo_filter: filter })).toBe("inserted");
    expect(await add({ project_id: "prj_au", repo_filter: filter })).toContain("automations_repo_filter_org_only");

    const reviewer = { preset: { role: "reviewer", overrides: {} }, brief: "" };
    const github = (event: string) => ({ kind: "github", event, actions: [], filters: {} });
    expect(await add({ agent: reviewer })).toContain("automations_reviewer_pull_request");
    expect(await add({ agent: reviewer, trigger: github("issues") })).toContain("automations_reviewer_pull_request");
    expect(await add({ agent: reviewer, trigger: github("pull_request_review") })).toBe("inserted");
    expect(await add({ kind: "server" })).toContain("automations_server_events");
    expect(await add({ concurrency: "send" })).toContain("automations_send_resumable_agent");
    expect(await add({ concurrency: "send", session_mode: "resumable", expiry_days: 3 })).toBe("inserted");
    // Only an agent's or a server's session is kept and resumed.
    const onEvent = { kind: "github", event: "push", actions: [], filters: {} };
    expect(await add({ kind: "command", session_mode: "resumable", expiry_days: 3 })).toContain("automations_resumable_kinds");
    expect(await add({ kind: "webhook", session_mode: "resumable", expiry_days: 3 })).toContain("automations_resumable_kinds");
    expect(await add({ kind: "server", trigger: onEvent, session_mode: "resumable", expiry_days: 3 })).toBe("inserted");
    expect(await add({ kind: "command" })).toBe("inserted");

    // An automation run's tool calls are redacted with its automation's project secrets.
    await sql`INSERT INTO project_secrets (organization_id, project_id, name, value, hint) VALUES ('org_au', 'prj_au', 'KEY', 'v-secret', 'cret'),
      ('org_au', 'prj_au', 'OTHER', 'v-other', 'ther')`;
    await sql`INSERT INTO automations (id, organization_id, project_id, name, kind, trigger, secrets)
      VALUES ('aut_sec', 'org_au', 'prj_au', 'S', 'agent', '{"kind":"schedule","cron":"0 3 * * *","tz":"UTC"}', '[{"source":"project","name":"KEY"}]')`;
    await sql`INSERT INTO automation_runs (id, organization_id, automation_id, status, source) VALUES ('arn_sec', 'org_au', 'aut_sec', 'running', 'schedule')`;
    expect([...await sql`SELECT name, value FROM automation_run_secret_values('arn_sec')`]).toEqual([{ name: "KEY", value: "v-secret" }]);

    // A run of a deleted automation goes with it.
    await sql`INSERT INTO automation_runs (id, organization_id, automation_id, status, source, mcp_token_hash, tools_scope)
      VALUES ('arn_1', 'org_au', 'aut_1', 'queued', 'schedule', 'automation-hash', '{"repos":["acme/web"]}')`;
    expect([...await sql`SELECT run_id, organization_id, automation_id FROM lookup_automation_by_mcp_token('automation-hash')`])
      .toEqual([{ run_id: 'arn_1', organization_id: 'org_au', automation_id: 'aut_1' }]);
    await sql`UPDATE automation_runs SET status = 'succeeded' WHERE id = 'arn_1'`;
    expect([...await sql`SELECT run_id FROM lookup_automation_by_mcp_token('automation-hash')`]).toEqual([]);
    // A prompt's versions are numbered once each and go with their automation.
    await sql`INSERT INTO automation_prompt_versions (id, organization_id, automation_id, number, body) VALUES ('apv_1', 'org_au', 'aut_1', 1, 'v1')`;
    expect(await sql`INSERT INTO automation_prompt_versions (id, organization_id, automation_id, number, body)
      VALUES ('apv_2', 'org_au', 'aut_1', 1, 'again')`.then(() => "inserted", (e: Error) => e.message)).toContain("automation_prompt_versions_automation_id_number_key");
    await sql`DELETE FROM automations WHERE id = 'aut_1'`;
    expect([...await sql`SELECT count(*)::int AS n FROM automation_runs WHERE automation_id = 'aut_1'`]).toEqual([{ n: 0 }]);
    expect([...await sql`SELECT count(*)::int AS n FROM automation_prompt_versions`]).toEqual([{ n: 0 }]);
  } finally {
    await sql.end();
  }
}, 120_000);

test("190 keeps existing sessions private, opens them one way only, and lets the organisation chat in an open one", async () => {
  const { url, sql } = await migratedBefore("190");
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_s', 's', 's'), ('org_t', 't', 't')`;
    await sql`INSERT INTO people (id, organization_id, name, role) VALUES ('per_own', 'org_s', 'Own', 'member'),
      ('per_rd', 'org_s', 'Reader', 'member'), ('per_mem', 'org_s', 'Member', 'member'), ('per_adm', 'org_s', 'Admin', 'admin'),
      ('per_gone', 'org_s', 'Gone', 'member'), ('per_far', 'org_t', 'Far', 'member')`;
    await sql`UPDATE people SET removed_at = now() WHERE id = 'per_gone'`;
    await sql`INSERT INTO sessions (id, organization_id, title) VALUES ('ssn_s', 'org_s', 'Billing')`;
    await sql`INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at)
      VALUES ('ssn_s', 'per_own', 'org_s', 'owner', now()), ('ssn_s', 'per_rd', 'org_s', 'read', now()),
        ('ssn_s', 'per_gone', 'org_s', 'chat', now())`;

    expect((await migrate(url, { log: () => {} })).applied).toEqual(["190_org_sessions.sql", "191_events_session_mentioned_idx.sql", "192_session_owner_sweep.sql", "193_events_session_owner_changes_idx.sql", "194_session_owner_attempts.sql", "195_chat.sql", "196_chat_channel_projects.sql", "197_events_session_tool_attempts_idx.sql", "198_events_task_tool_attempts_idx.sql", "199_events_automation_tool_attempts_idx.sql", "200_drop_events_session_github_reads_v2_idx.sql", "201_drop_events_task_github_reads_v2_idx.sql", "202_drop_events_automation_github_reads_idx.sql", "203_task_pull_request.sql", "210_organization_secrets.sql", "211_fake_checks_base_shown_model.sql", "212_incoming_webhooks.sql", "213_chat_link_offers.sql", "214_chat_request_failure.sql", "215_chat_request_failure_backfill.sql", "216_attachment_storage_keys.sql", "217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
    expect([...await sql`SELECT access, owned_by_admins FROM sessions`]).toEqual([{ access: "private", owned_by_admins: false }]);
    const roles = async () => Object.fromEntries((await sql`SELECT p.id, session_role('ssn_s', p.id) AS role FROM people p ORDER BY p.id`)
      .map((r: { id: string; role: string | null }) => [r.id, r.role]));
    // Private: only its accepted, active members (a removed one's row gives nothing); an admin is not one.
    expect(await roles()).toEqual({ per_adm: null, per_far: null, per_gone: null, per_mem: null, per_own: "owner", per_rd: "read" });

    await sql`UPDATE sessions SET access = 'organisation' WHERE id = 'ssn_s'`;
    // Open: every active member of its organisation chats, a reader lifted to chat; not a removed person, nor another organisation's.
    expect(await roles()).toEqual({ per_adm: "chat", per_far: null, per_gone: null, per_mem: "chat", per_own: "owner", per_rd: "chat" });
    const refusal = async (q: () => Promise<unknown>) => q().then(() => "", (err: Error) => err.message);
    expect(await refusal(async () => await sql`UPDATE sessions SET access = 'private' WHERE id = 'ssn_s'`))
      .toContain("cannot be undone");
    expect(await refusal(async () => await sql`INSERT INTO sessions (id, organization_id, title, access) VALUES ('ssn_x', 'org_s', 'X', 'everyone')`))
      .toContain("sessions_access_check");
    expect([...await sql`SELECT access FROM sessions`]).toEqual([{ access: "organisation" }]);

    // Owned by its admins: an active admin acts as owner.
    await sql`UPDATE sessions SET owned_by_admins = true WHERE id = 'ssn_s'`;
    expect((await roles()).per_adm).toBe("owner");

    // The rule as one join over sessions, its people and people, which the
    // function answers from the person's own row first: the same answer for
    // every person and session of a mixed world.
    await sql`INSERT INTO sessions (id, organization_id, title, access, owned_by_admins) VALUES
      ('ssn_priv', 'org_s', 'P', 'private', false), ('ssn_org', 'org_s', 'O', 'organisation', false),
      ('ssn_adm_priv', 'org_s', 'AP', 'private', true), ('ssn_far', 'org_t', 'F', 'organisation', false)`;
    await sql`INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at) VALUES
      ('ssn_priv', 'per_own', 'org_s', 'owner', now()), ('ssn_priv', 'per_rd', 'org_s', 'read', now()),
      ('ssn_priv', 'per_adm', 'org_s', 'chat', now()), ('ssn_priv', 'per_gone', 'org_s', 'chat', now()),
      ('ssn_org', 'per_mem', 'org_s', 'owner', now()), ('ssn_org', 'per_rd', 'org_s', 'read', now()),
      ('ssn_org', 'per_adm', 'org_s', 'read', now()),
      ('ssn_adm_priv', 'per_gone', 'org_s', 'owner', now()), ('ssn_adm_priv', 'per_rd', 'org_s', 'read', now()),
      ('ssn_adm_priv', 'per_adm', 'org_s', 'chat', now()), ('ssn_adm_priv', 'per_mem', 'org_s', 'chat', now()),
      ('ssn_far', 'per_far', 'org_t', 'owner', now())`;
    await sql`INSERT INTO session_people (session_id, person_id, organization_id, role) VALUES ('ssn_priv', 'per_mem', 'org_s', 'chat')`;
    const differ = async () => [...await sql`SELECT s.id AS session, p.id AS person, session_role(s.id, p.id) AS got, (SELECT CASE
        WHEN me.id IS NULL THEN NULL
        WHEN x.owned_by_admins AND me.role = 'admin' THEN 'owner'
        WHEN sp.role IS NOT NULL THEN CASE WHEN x.access = 'organisation' AND sp.role = 'read' THEN 'chat' ELSE sp.role END
        WHEN x.access = 'organisation' THEN 'chat' END
      FROM sessions x
      LEFT JOIN session_people sp ON sp.session_id = x.id AND sp.person_id = p.id AND sp.accepted_at IS NOT NULL
      LEFT JOIN people me ON me.id = p.id AND me.organization_id = x.organization_id AND me.removed_at IS NULL
      WHERE x.id = s.id) AS want
      FROM sessions s CROSS JOIN people p`] as Array<{ session: string; person: string; got: string | null; want: string | null }>;
    const all = await differ();
    expect(all.length).toBe(30);
    expect(all.filter((r) => r.got !== r.want)).toEqual([]);
    expect(new Set(all.map((r) => r.got))).toEqual(new Set(["owner", "chat", "read", null]));
    // The owner of the open session removed, the admin made a member, then
    // the removed person back: the answer follows people at once.
    await sql`UPDATE people SET removed_at = now() WHERE id = 'per_mem'`;
    await sql`UPDATE people SET role = 'member' WHERE id = 'per_adm'`;
    expect((await differ()).filter((r) => r.got !== r.want)).toEqual([]);
    expect((await sql`SELECT session_role('ssn_org', 'per_mem') AS r`)[0].r).toBe(null);
    expect((await sql`SELECT session_role('ssn_adm_priv', 'per_adm') AS r`)[0].r).toBe("chat");
    await sql`UPDATE people SET removed_at = NULL WHERE id = 'per_mem'`;
    await sql`UPDATE people SET role = 'admin' WHERE id = 'per_adm'`;
    expect((await differ()).filter((r) => r.got !== r.want)).toEqual([]);
    expect((await sql`SELECT session_role('ssn_org', 'per_mem') AS r`)[0].r).toBe("owner");
    expect((await sql`SELECT session_role('ssn_adm_priv', 'per_adm') AS r`)[0].r).toBe("owner");
    // A member promoted with a chat row in an admin-owned session owns it
    // now; one moved to another organisation has nothing in this one's.
    expect((await sql`SELECT session_role('ssn_adm_priv', 'per_mem') AS r`)[0].r).toBe("chat");
    await sql`UPDATE people SET role = 'admin' WHERE id = 'per_mem'`;
    expect((await differ()).filter((r) => r.got !== r.want)).toEqual([]);
    expect((await sql`SELECT session_role('ssn_adm_priv', 'per_mem') AS r`)[0].r).toBe("owner");
    expect((await sql`SELECT session_role('ssn_priv', 'per_own') AS r`)[0].r).toBe("owner");
    await sql`UPDATE people SET organization_id = 'org_t' WHERE id = 'per_own'`;
    expect((await differ()).filter((r) => r.got !== r.want)).toEqual([]);
    expect((await sql`SELECT session_role('ssn_priv', 'per_own') AS r`)[0].r).toBe(null);

    // The notifier's mention sweep has its own partial index, built concurrently.
    expect([...await sql`SELECT i.indisvalid AS valid FROM pg_index i WHERE i.indexrelid = to_regclass('events_session_mentioned_idx')`])
      .toEqual([{ valid: true }]);
  } finally {
    await sql.end();
  }
}, 120_000);

test("195 binds a workspace to one organisation, a provider account to one account, and a thread to one thing", async () => {
  const { url, sql } = await migratedBefore("195");
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_a', 'Acme', 'acme'), ('org_b', 'Other', 'other')`;
    await sql`INSERT INTO accounts (id, email, name) VALUES ('acc_m', 'm@example.com', 'M'), ('acc_e', 'e@example.com', 'E')`;
    await sql`INSERT INTO people (id, organization_id, name, role, account_id) VALUES ('per_m', 'org_a', 'M', 'admin', 'acc_m'),
      ('per_e', 'org_a', 'E', 'member', 'acc_e'), ('per_o', 'org_b', 'O', 'admin', 'acc_m')`;
    await sql`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_a', 'org_a', 'Billing', 'bil', 'BIL')`;
    await sql`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('wi_a', 'org_a', 'prj_a', 1, 'T')`;
    await sql`INSERT INTO sessions (id, organization_id, title) VALUES ('ssn_a', 'org_a', 'S')`;

    expect((await migrate(url, { log: () => {} })).applied).toEqual(["195_chat.sql", "196_chat_channel_projects.sql", "197_events_session_tool_attempts_idx.sql", "198_events_task_tool_attempts_idx.sql", "199_events_automation_tool_attempts_idx.sql", "200_drop_events_session_github_reads_v2_idx.sql", "201_drop_events_task_github_reads_v2_idx.sql", "202_drop_events_automation_github_reads_idx.sql", "203_task_pull_request.sql", "210_organization_secrets.sql", "211_fake_checks_base_shown_model.sql", "212_incoming_webhooks.sql", "213_chat_link_offers.sql", "214_chat_request_failure.sql", "215_chat_request_failure_backfill.sql", "216_attachment_storage_keys.sql", "217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
    const refusal = async (q: () => Promise<unknown>) => q().then(() => "", (err: Error) => err.message);
    const install = (id: string, org: string, workspace: string, settings: unknown = {}) => sql`INSERT INTO chat_installations
      (id, organization_id, provider, external_workspace_id, bot_token, settings) VALUES (${id}, ${org}, 'slack', ${workspace}, 'xoxb', ${settings}::jsonb)`;
    await install("chi_a", "org_a", "T1");
    // The workspace is bound to Acme: another organisation installing it is refused, across tenants.
    expect(await refusal(async () => await install("chi_b", "org_b", "T1"))).toContain("chat_installations_provider_external_workspace_id_key");
    // One workspace per provider per organisation.
    expect(await refusal(async () => await install("chi_c", "org_a", "T2"))).toContain("chat_installations_organization_id_provider_key");
    expect(await refusal(async () => await install("chi_d", "org_b", "T3", { additional_instructions: "x".repeat(4001) })))
      .toContain("chat_installations_settings_check");
    expect(await refusal(async () => await sql`INSERT INTO chat_installations (id, organization_id, provider, external_workspace_id, bot_token)
      VALUES ('chi_e', 'org_b', 'Slack!', 'T4', 'x')`)).toContain("chat_installations_provider_check");

    const link = (user: string, account: string, person: string) => sql`INSERT INTO chat_identities
      (organization_id, provider, external_workspace_id, external_user_id, account_id, person_id) VALUES ('org_a', 'slack', 'T1', ${user}, ${account}, ${person})`;
    await link("U1", "acc_m", "per_m");
    // A provider account links to one account; an account to one provider account per workspace.
    expect(await refusal(async () => await link("U1", "acc_e", "per_e"))).toContain("chat_identities_pkey");
    expect(await refusal(async () => await link("U2", "acc_m", "per_m"))).toContain("chat_identities_provider_external_workspace_id_account_id_key");
    await link("U2", "acc_e", "per_e");

    const bind = (thread: string, session: string | null, task: string | null) => sql`INSERT INTO chat_threads
      (organization_id, provider, external_workspace_id, conversation_id, thread_id, conversation_kind, session_id, task_id)
      VALUES ('org_a', 'slack', 'T1', 'C1', ${thread}, 'public', ${session}, ${task})`;
    await bind("1.0", "ssn_a", null);
    expect(await refusal(async () => await bind("2.0", "ssn_a", "wi_a"))).toContain("chat_threads_check");
    expect(await refusal(async () => await bind("3.0", null, null))).toContain("chat_threads_check");
    expect(await refusal(async () => await bind("1.0", null, "wi_a"))).toContain("chat_threads_pkey");

    expect(await refusal(async () => await sql`INSERT INTO chat_requests (id, organization_id, provider, event_id, external_workspace_id,
      conversation_id, external_user_id, route) VALUES ('chr_x', 'org_a', 'slack', 'Ev1', 'T1', 'C1', 'U1', 'shout')`)).toContain("chat_requests_route_check");
    expect(await refusal(async () => await sql`UPDATE tasks SET origin = '"slack"'::jsonb WHERE id = 'wi_a'`)).toContain("tasks_origin_check");
    expect(await refusal(async () => await sql`UPDATE organizations SET chat_instructions = ${"x".repeat(4001)} WHERE id = 'org_a'`))
      .toContain("organizations_chat_instructions_check");
    await sql`UPDATE tasks SET origin = ${{ provider: "slack", conversationName: "#support" }}::jsonb WHERE id = 'wi_a'`;

    // A connect state's organisation is readable before any tenant scope, and nothing else of it.
    await sql`INSERT INTO chat_connect_states (state_hash, organization_id, kind, provider, external_workspace_id, external_user_id, expires_at)
      VALUES ('h1', 'org_a', 'prompt', 'slack', 'T1', 'U9', now() - interval '1 minute')`;
    expect([...await sql`SELECT * FROM chat_connect_state_org('h1')`]).toEqual([{ organization_id: "org_a", expired: true }]);
    expect(await refusal(async () => await sql`INSERT INTO chat_connect_states (state_hash, organization_id, kind, provider)
      VALUES ('h2', 'org_a', 'prompt', 'slack')`)).toContain("chat_connect_states_check");

    // Tenant isolation: the app role in Acme sees Acme's rows only.
    await sql`INSERT INTO chat_installations (id, organization_id, provider, external_workspace_id, bot_token) VALUES ('chi_o', 'org_b', 'slack', 'T9', 'x')`;
    const seen = await sql.begin(async (tx) => {
      await tx`SET LOCAL ROLE dude_app`;
      await tx`SELECT set_config('app.organization_id', 'org_b', true)`;
      return [...await tx`SELECT id FROM chat_installations ORDER BY id`];
    });
    expect(seen).toEqual([{ id: "chi_o" }]);

    // The bot token, as the forge token (051): the sweeper finds a delivery's
    // organisation by its workspace and reads no secret.
    const asSweeper = (q: string) => sql.begin(async (tx) => {
      await tx`SET LOCAL ROLE dude_sweeper`;
      return [...await tx.unsafe(q)];
    });
    expect(await asSweeper(`SELECT organization_id FROM chat_installations WHERE provider = 'slack' AND external_workspace_id = 'T9'`))
      .toEqual([{ organization_id: "org_b" }]);
    expect(await refusal(async () => await asSweeper(`SELECT bot_token FROM chat_installations`))).toContain("permission denied");
    expect(await refusal(async () => await asSweeper(`SELECT * FROM chat_installations`))).toContain("permission denied");
    expect(await refusal(async () => await asSweeper(`SELECT mention FROM chat_requests`))).toContain("permission denied");
  } finally {
    await sql.end();
  }
}, 120_000);

test("196 keeps a channel's usual projects and a provider's hidden ones per organisation, and takes the channel_projects route", async () => {
  const { url, sql } = await migratedBefore("196");
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_a', 'Acme', 'acme'), ('org_b', 'Other', 'other')`;
    await sql`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_a', 'org_a', 'Billing', 'bil', 'BIL'),
      ('prj_b', 'org_b', 'Other', 'oth', 'OTH')`;
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["196_chat_channel_projects.sql", "197_events_session_tool_attempts_idx.sql", "198_events_task_tool_attempts_idx.sql", "199_events_automation_tool_attempts_idx.sql", "200_drop_events_session_github_reads_v2_idx.sql", "201_drop_events_task_github_reads_v2_idx.sql", "202_drop_events_automation_github_reads_idx.sql", "203_task_pull_request.sql", "210_organization_secrets.sql", "211_fake_checks_base_shown_model.sql", "212_incoming_webhooks.sql", "213_chat_link_offers.sql", "214_chat_request_failure.sql", "215_chat_request_failure_backfill.sql", "216_attachment_storage_keys.sql", "217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
    const refusal = async (q: () => Promise<unknown>) => q().then(() => "", (err: Error) => err.message);
    const usual = (org: string, project: string) => sql`INSERT INTO chat_channel_projects (organization_id, provider, external_workspace_id,
      conversation_id, project_id) VALUES (${org}, 'slack', 'T1', 'C1', ${project})`;
    await usual("org_a", "prj_a");
    expect(await refusal(async () => await usual("org_a", "prj_a"))).toContain("chat_channel_projects_pkey");
    expect(await refusal(async () => await sql`INSERT INTO chat_channel_projects (organization_id, provider, external_workspace_id,
      conversation_id, project_id) VALUES ('org_a', 'slack', 'T1', '', 'prj_a')`)).toContain("chat_channel_projects_conversation_id_check");
    const hide = (org: string, project: string) => sql`INSERT INTO chat_hidden_projects (organization_id, provider, project_id)
      VALUES (${org}, 'slack', ${project})`;
    await hide("org_a", "prj_a");
    expect(await refusal(async () => await hide("org_a", "prj_a"))).toContain("chat_hidden_projects_pkey");
    await usual("org_b", "prj_b");
    await hide("org_b", "prj_b");

    // Tenant isolation: the app role in Acme sees Acme's rows only.
    const seen = await sql.begin(async (tx) => {
      await tx`SET LOCAL ROLE dude_app`;
      await tx`SELECT set_config('app.organization_id', 'org_a', true)`;
      return [[...await tx`SELECT project_id FROM chat_channel_projects`], [...await tx`SELECT project_id FROM chat_hidden_projects`]];
    });
    expect(seen).toEqual([[{ project_id: "prj_a" }], [{ project_id: "prj_a" }]]);

    // A project gone takes its rows with it.
    await sql`DELETE FROM projects WHERE id = 'prj_a'`;
    expect([...await sql`SELECT count(*)::int AS n FROM chat_channel_projects WHERE organization_id = 'org_a'`]).toEqual([{ n: 0 }]);
    expect([...await sql`SELECT count(*)::int AS n FROM chat_hidden_projects WHERE organization_id = 'org_a'`]).toEqual([{ n: 0 }]);

    const request = (id: string, route: string) => sql`INSERT INTO chat_requests (id, organization_id, provider, event_id, external_workspace_id,
      conversation_id, external_user_id, route) VALUES (${id}, 'org_b', 'slack', ${id}, 'T1', 'C1', 'U1', ${route})`;
    await request("chr_1", "channel_projects");
    expect(await refusal(async () => await request("chr_2", "shout"))).toContain("chat_requests_route_check");
  } finally {
    await sql.end();
  }
}, 120_000);

test("210 adds organisation secrets, turns automation picks into project refs, and resolves a same-name pick to the project's", async () => {
  const { url, sql } = await migratedBefore("210");
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_a', 'Acme', 'acme'), ('org_b', 'Other', 'other')`;
    await sql`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_a', 'org_a', 'platform', 'platform', 'PLAT')`;
    await sql`INSERT INTO project_secrets (organization_id, project_id, name, value, hint, recipients)
      VALUES ('org_a', 'prj_a', 'GH_TOKEN', 'project-gh-value', 'alue', '{tester}'), ('org_a', 'prj_a', 'DB_PASS', 'project-db-value', 'alue', '{previews}')`;
    const schedule = { kind: "schedule", cron: "0 3 * * *", tz: "UTC" };
    const tester = { preset: { role: "qa_browser", overrides: {} }, brief: "" };
    await sql`INSERT INTO automations (id, organization_id, project_id, name, kind, trigger, agent, secrets) VALUES
      ('aut_own', 'org_a', 'prj_a', 'Own', 'agent', ${schedule}::jsonb, ${{ own: { prompt: "" } }}::jsonb, '{GH_TOKEN,DB_PASS}'),
      ('aut_qa', 'org_a', 'prj_a', 'QA', 'agent', ${schedule}::jsonb, ${tester}::jsonb, '{DB_PASS,GH_TOKEN}'),
      ('aut_rev', 'org_a', 'prj_a', 'Investigate', 'agent', ${schedule}::jsonb, ${{ preset: { role: "investigator", overrides: {} }, brief: "" }}::jsonb, '{}'),
      ('aut_none', 'org_a', NULL, 'None', 'command', ${schedule}::jsonb, NULL, '{}')`;

    expect((await migrate(url, { log: () => {} })).applied).toEqual(["210_organization_secrets.sql", "211_fake_checks_base_shown_model.sql", "212_incoming_webhooks.sql", "213_chat_link_offers.sql", "214_chat_request_failure.sql", "215_chat_request_failure_backfill.sql", "216_attachment_storage_keys.sql", "217_automation_run_events.sql", "218_events_automation_run_idx.sql", "219_run_person_note.sql", "220_base_sync_requests.sql", "221_branch_previews.sql", "222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
    // Old picks are project refs; a preset's picks go (its role decides what it gets).
    expect([...await sql`SELECT id, secrets FROM automations ORDER BY id`]).toEqual([
      { id: "aut_none", secrets: [] },
      { id: "aut_own", secrets: [{ source: "project", name: "DB_PASS" }, { source: "project", name: "GH_TOKEN" }] },
      { id: "aut_qa", secrets: [] },
      { id: "aut_rev", secrets: [] },
    ]);
    // Each preset that lost picks has an automation.changed naming them, as the API records a change; one that had none, nothing.
    expect([...await sql`SELECT organization_id, project_id, actor_type, actor_id, source, payload, id ~ '^evt_[0-9a-z]{9}[0-9a-f]{16}$' AS id_shape
      FROM events WHERE event_type = 'automation.changed' ORDER BY id`]).toEqual([
      { organization_id: "org_a", project_id: "prj_a", actor_type: "system", actor_id: "dude", source: "control-plane", id_shape: true,
        payload: { automationId: "aut_qa", name: "QA", changed: { secretsDropped: ["DB_PASS", "GH_TOKEN"] } } },
    ]);
    const refusal = async (q: () => Promise<unknown>) => q().then(() => "", (err: Error) => err.message);
    const add = (name: string, recipients: string[]) => sql`INSERT INTO organization_secrets (organization_id, name, value, hint, recipients)
      VALUES ('org_a', ${name}, ${`org-${name.toLowerCase()}-value`}, 'alue', ${sql.array(recipients, "text")}::text[])`;
    // Ticked for nobody is allowed one level up (automations may still pick it); an unknown recipient is not.
    await add("STATUSPAGE_KEY", []);
    await add("GH_TOKEN", ["tester", "implementer"]);
    await add("NPM_TOKEN", ["tester"]);
    expect(await refusal(async () => await add("BAD", ["conductor"]))).toContain("organization_secrets_recipients_check");
    expect(await refusal(async () => await add("LUX_X", []))).toContain("organization_secrets_name_check");
    expect(await refusal(async () => await add("NPM_TOKEN", []))).toContain("organization_secrets_pkey");

    // The project's own first; an organisation name it holds is not in force there.
    expect([...await sql`SELECT name, value, source FROM secrets_in_force('org_a', 'prj_a') ORDER BY name`]).toEqual([
      { name: "DB_PASS", value: "project-db-value", source: "project" },
      { name: "GH_TOKEN", value: "project-gh-value", source: "project" },
      { name: "NPM_TOKEN", value: "org-npm_token-value", source: "organization" },
      { name: "STATUSPAGE_KEY", value: "org-statuspage_key-value", source: "organization" },
    ]);

    const given = async (id: string) => [...await sql`SELECT name, value FROM automation_secrets_given(${id})`];
    // An organisation pick whose name the project holds resolves to the project's value.
    await sql`UPDATE automations SET secrets = '[{"source":"organization","name":"GH_TOKEN"},{"source":"organization","name":"NPM_TOKEN"}]' WHERE id = 'aut_own'`;
    expect(await given("aut_own")).toEqual([{ name: "GH_TOKEN", value: "project-gh-value" }, { name: "NPM_TOKEN", value: "org-npm_token-value" }]);
    // A Tester preset gets what tester is ticked for, at both levels, the project's winning a name.
    expect(await given("aut_qa")).toEqual([{ name: "GH_TOKEN", value: "project-gh-value" }, { name: "NPM_TOKEN", value: "org-npm_token-value" }]);
    // An organisation automation picks the organisation's alone.
    await sql`UPDATE automations SET secrets = '[{"source":"organization","name":"STATUSPAGE_KEY"},{"source":"project","name":"DB_PASS"}]' WHERE id = 'aut_none'`;
    expect(await given("aut_none")).toEqual([{ name: "STATUSPAGE_KEY", value: "org-statuspage_key-value" }]);
    await sql`INSERT INTO automation_runs (id, organization_id, automation_id, status, source) VALUES ('arn_n', 'org_a', 'aut_none', 'running', 'schedule')`;
    expect([...await sql`SELECT name, value FROM automation_run_secret_values('arn_n')`]).toEqual([{ name: "STATUSPAGE_KEY", value: "org-statuspage_key-value" }]);
    expect(await refusal(async () => await sql`UPDATE automations SET secrets = '{}'::jsonb WHERE id = 'aut_none'`)).toContain("automations_secrets_refs");

    // A Run's recorded names redact by the current values once its copy is
    // forgotten: an organisation name by the organisation's, a name the
    // project overrides by both (the organisation's value is redacted too).
    await sql`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('wi_a', 'org_a', 'prj_a', 1, 'T')`;
    await sql`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, project_secret_names)
      VALUES ('run_a', 'org_a', 'prj_a', 'wi_a', 1, '{NPM_TOKEN,GH_TOKEN}')`;
    expect([...await sql`SELECT count(*)::int AS n FROM run_project_secrets WHERE run_id = 'run_a'`]).toEqual([{ n: 0 }]);
    expect([...await sql`SELECT name, value FROM run_secret_values('run_a') ORDER BY name, value`]).toEqual([
      { name: "GH_TOKEN", value: "org-gh_token-value" },
      { name: "GH_TOKEN", value: "project-gh-value" },
      { name: "NPM_TOKEN", value: "org-npm_token-value" },
    ]);

    // Tenant isolation, and no sweeper read.
    await sql`INSERT INTO organization_secrets (organization_id, name, value, hint) VALUES ('org_b', 'OTHER', 'other-value', 'alue')`;
    const seen = await sql.begin(async (tx) => {
      await tx`SET LOCAL ROLE dude_app`;
      await tx`SELECT set_config('app.organization_id', 'org_b', true)`;
      return [...await tx`SELECT name FROM organization_secrets ORDER BY name`];
    });
    expect(seen).toEqual([{ name: "OTHER" }]);
    expect(await refusal(async () => await sql.begin(async (tx) => {
      await tx`SET LOCAL ROLE dude_sweeper`;
      return [...await tx`SELECT value FROM organization_secrets`];
    }))).toContain("permission denied");
  } finally {
    await sql.end();
  }
}, 120_000);

test("222 keeps one chat notice per task or epic and kind, each tenant's alone, and the sweeper reads only what is due", async () => {
  const { url, sql } = await migratedBefore("222");
  try {
    await sql`INSERT INTO organizations (id, name, slug) VALUES ('org_a', 'Acme', 'acme'), ('org_b', 'Other', 'other')`;
    await sql`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_a', 'org_a', 'Billing', 'bil', 'BIL'),
      ('prj_b', 'org_b', 'Other', 'oth', 'OTH')`;
    await sql`INSERT INTO epics (id, organization_id, project_id, title) VALUES ('epc_a', 'org_a', 'prj_a', 'Exports'), ('epc_b', 'org_b', 'prj_b', 'Other')`;
    await sql`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('wi_a', 'org_a', 'prj_a', 1, 'T'), ('wi_b', 'org_b', 'prj_b', 1, 'U')`;
    expect((await migrate(url, { log: () => {} })).applied).toEqual(["222_chat_notices.sql", "223_child_cohorts.sql", "224_preview_feedback.sql", "230_task_branch_snapshots.sql", "231_branch_snapshot_retention.sql", "232_conductor_waits.sql", "233_run_recoveries.sql"]);
    const refusal = async (q: () => Promise<unknown>) => q().then(() => "", (err: Error) => err.message);
    const notice = (id: string, org: string, task: string | null, epic: string | null, kind: string, detail = "") => sql`INSERT INTO chat_notices
      (id, organization_id, task_id, epic_id, kind, provider, external_workspace_id, conversation_id, subject_key, subject_title, detail)
      VALUES (${id}, ${org}, ${task}, ${epic}, ${kind}, 'slack', 'T1', 'C1', 'BIL-1', 'T', ${detail})`;
    await notice("chn_1", "org_a", "wi_a", null, "started");
    await notice("chn_2", "org_b", "wi_b", null, "done");
    await notice("chn_e1", "org_a", null, "epc_a", "epic_started");
    await notice("chn_e2", "org_a", null, "epc_a", "epic_finished", "1 done");
    await notice("chn_e3", "org_b", null, "epc_b", "epic_pr_opened");
    expect(await refusal(async () => await notice("chn_3", "org_a", "wi_a", null, "started"))).toContain("chat_notices_task_id_kind_key");
    expect(await refusal(async () => await notice("chn_3e", "org_a", null, "epc_a", "epic_started"))).toContain("chat_notices_epic_id_kind_key");
    expect(await refusal(async () => await notice("chn_4", "org_a", "wi_a", null, "merged"))).toContain("chat_notices_kind_check");
    expect(await refusal(async () => await notice("chn_5", "org_a", "wi_a", null, "failed", "x".repeat(2001)))).toContain("chat_notices_detail_check");
    // Exactly one subject, and an epic's kinds only on an epic.
    expect(await refusal(async () => await notice("chn_7", "org_a", "wi_a", "epc_a", "started"))).toContain("chat_notices_one_subject");
    expect(await refusal(async () => await notice("chn_8", "org_a", null, null, "started"))).toContain("chat_notices_one_subject");
    expect(await refusal(async () => await notice("chn_9", "org_a", null, "epc_a", "done"))).toContain("chat_notices_one_subject");
    expect(await refusal(async () => await notice("chn_10", "org_a", "wi_a", null, "epic_finished"))).toContain("chat_notices_one_subject");

    const seen = await sql.begin(async (tx) => {
      await tx`SET LOCAL ROLE dude_app`;
      await tx`SELECT set_config('app.organization_id', 'org_b', true)`;
      return [...await tx`SELECT id FROM chat_notices ORDER BY id`];
    });
    expect(seen).toEqual([{ id: "chn_2" }, { id: "chn_e3" }]);
    expect(await refusal(async () => await sql.begin(async (tx) => {
      await tx`SET LOCAL ROLE dude_app`;
      await tx`SELECT set_config('app.organization_id', 'org_b', true)`;
      await tx`INSERT INTO chat_notices (id, organization_id, task_id, kind, provider, external_workspace_id, conversation_id, subject_key, subject_title)
        VALUES ('chn_6', 'org_a', 'wi_a', 'done', 'slack', 'T1', 'C1', 'BIL-1', 'T')`;
    }))).toContain("row-level security");
    // An epic's row is its tenant's alone too.
    expect(await refusal(async () => await sql.begin(async (tx) => {
      await tx`SET LOCAL ROLE dude_app`;
      await tx`SELECT set_config('app.organization_id', 'org_b', true)`;
      await tx`INSERT INTO chat_notices (id, organization_id, epic_id, kind, provider, external_workspace_id, conversation_id, subject_key, subject_title)
        VALUES ('chn_6e', 'org_a', 'epc_a', 'epic_pr_opened', 'slack', 'T1', 'C1', 'BIL', 'Exports')`;
    }))).toContain("row-level security");
    const seenEpic = await sql.begin(async (tx) => {
      await tx`SET LOCAL ROLE dude_app`;
      await tx`SELECT set_config('app.organization_id', 'org_b', true)`;
      return [...await tx`SELECT id FROM chat_notices WHERE epic_id = 'epc_a'`];
    });
    expect(seenEpic).toEqual([]);
    // The sweeper finds what is due across tenants, and reads none of what it says.
    const due = await sql.begin(async (tx) => {
      await tx`SET LOCAL ROLE dude_sweeper`;
      return [...await tx`SELECT id, organization_id FROM chat_notices WHERE posted_at IS NULL AND gave_up_at IS NULL ORDER BY next_attempt_at, id`];
    });
    expect(due.map((r) => r.id).sort()).toEqual(["chn_1", "chn_2", "chn_e1", "chn_e2", "chn_e3"]);
    for (const column of ["subject_title", "epic_id", "task_id", "trigger_task_id", "detail"]) {
      expect(await refusal(async () => await sql.begin(async (tx) => {
        await tx`SET LOCAL ROLE dude_sweeper`;
        return [...await tx`SELECT ${tx(column)} FROM chat_notices`];
      }))).toContain("permission denied");
    }
    // The task an epic notice came of is an epic row's alone.
    expect(await refusal(async () => await sql`UPDATE chat_notices SET trigger_task_id = 'wi_a' WHERE id = 'chn_1'`)).toContain("chat_notices_one_subject");
    await sql`UPDATE chat_notices SET trigger_task_id = 'wi_a' WHERE id = 'chn_e1'`;
    // chat_epic_notice runs as its caller: another tenant's epic records nothing.
    await sql`UPDATE epics SET origin = '{"provider":"slack","workspaceId":"T1","conversationId":"C1","threadId":"1.0"}' WHERE id = 'epc_a'`;
    const recordAs = (org: string, epic = "epc_a", kind = "epic_pr_opened", trigger: string | null = "wi_a") => sql.begin(async (tx) => {
      await tx`SET LOCAL ROLE dude_app`;
      await tx`SELECT set_config('app.organization_id', ${org}, true)`;
      await tx`SELECT chat_epic_notice(${epic}, ${kind}, ${trigger})`;
    });
    await recordAs("org_b");
    expect([...await sql`SELECT count(*)::int AS n FROM chat_notices WHERE epic_id = 'epc_a' AND kind = 'epic_pr_opened'`]).toEqual([{ n: 0 }]);
    await recordAs("org_a");
    await recordAs("org_a");
    expect([...await sql`SELECT subject_key, subject_title, thread_id, trigger_task_id FROM chat_notices WHERE epic_id = 'epc_a' AND kind = 'epic_pr_opened'`])
      .toEqual([{ subject_key: "BIL", subject_title: "Exports", thread_id: "1.0", trigger_task_id: "wi_a" }]);
    // The trigger task is kept only when it is the epic's organisation's: another
    // tenant's or an unknown id is recorded as no task, and the notice still is.
    await sql`INSERT INTO epics (id, organization_id, project_id, title, origin) VALUES
      ('epc_a2', 'org_a', 'prj_a', 'Imports', '{"provider":"slack","workspaceId":"T1","conversationId":"C1","threadId":"2.0"}'),
      ('epc_a3', 'org_a', 'prj_a', 'Reports', '{"provider":"slack","workspaceId":"T1","conversationId":"C1","threadId":"3.0"}')`;
    expect(await refusal(async () => await recordAs("org_a", "epc_a2", "epic_started", "wi_b"))).toBe("");
    expect(await refusal(async () => await recordAs("org_a", "epc_a2", "epic_pr_opened", "wi_nope"))).toBe("");
    // The table's owner sees every tenant's task; the organisation bound still holds.
    expect(await refusal(async () => await sql`SELECT chat_epic_notice('epc_a3', 'epic_started', 'wi_b')`)).toBe("");
    expect([...await sql`SELECT epic_id, kind, trigger_task_id FROM chat_notices WHERE epic_id IN ('epc_a2', 'epc_a3') ORDER BY epic_id, kind`])
      .toEqual([
        { epic_id: "epc_a2", kind: "epic_pr_opened", trigger_task_id: null },
        { epic_id: "epc_a2", kind: "epic_started", trigger_task_id: null },
        { epic_id: "epc_a3", kind: "epic_started", trigger_task_id: null },
      ]);

    await sql`DELETE FROM tasks WHERE id = 'wi_a'`;
    expect([...await sql`SELECT id, trigger_task_id FROM chat_notices WHERE organization_id = 'org_a' AND id LIKE 'chn_e%' ORDER BY id`])
      .toEqual([{ id: "chn_e1", trigger_task_id: null }, { id: "chn_e2", trigger_task_id: null }]);
    expect([...await sql`SELECT count(*)::int AS n FROM chat_notices WHERE organization_id = 'org_a'`]).toEqual([{ n: 6 }]);
    await sql`DELETE FROM epics WHERE id IN ('epc_a', 'epc_a2', 'epc_a3')`;
    expect([...await sql`SELECT count(*)::int AS n FROM chat_notices WHERE organization_id = 'org_a'`]).toEqual([{ n: 0 }]);
    expect([...await sql`SELECT count(*)::int AS n FROM chat_notices WHERE organization_id = 'org_b'`]).toEqual([{ n: 2 }]);
  } finally {
    await sql.end();
  }
}, 120_000);

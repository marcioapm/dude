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
import { listMigrationFiles, repoMigrationsDir } from "../src/db/migrate.ts";

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
});

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

test("an owner that is not a superuser applies every migration", async () => {
  // As on the aiverse host or a managed Postgres: the owner bypasses row-level
  // security and creates roles, and a superuser created vector beforehand.
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

  const migrated = run([binary], { DATABASE_URL: url.toString() });
  expect(migrated.code, migrated.out).toBe(0);
  expect((await recorded(url.toString())).map((r) => r.name)).toEqual(await repoSqlFiles());
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

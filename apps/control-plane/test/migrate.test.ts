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

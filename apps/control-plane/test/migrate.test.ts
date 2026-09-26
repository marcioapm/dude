/**
 * Where the migration runner finds its SQL: a deployment has no repository,
 * so DUDE_MIGRATIONS_DIR points it at the shipped files.
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { listMigrationFiles } from "../src/db/migrate.ts";

let dir: string;
const saved = process.env.DUDE_MIGRATIONS_DIR;

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "dude-migrations-"));
  await writeFile(join(dir, "002_second.sql"), "SELECT 2;");
  await writeFile(join(dir, "001_first.sql"), "SELECT 1;");
  await writeFile(join(dir, "README"), "not a migration");
});

afterAll(async () => {
  if (saved === undefined) delete process.env.DUDE_MIGRATIONS_DIR;
  else process.env.DUDE_MIGRATIONS_DIR = saved;
  await rm(dir, { recursive: true, force: true });
});

test("reads the directory DUDE_MIGRATIONS_DIR names, in order", async () => {
  process.env.DUDE_MIGRATIONS_DIR = dir;
  const files = await listMigrationFiles();
  expect(files.map((f) => f.name)).toEqual(["001_first.sql", "002_second.sql"]);
  expect(files[0]?.path).toBe(join(dir, "001_first.sql"));
});

test("defaults to the repository's migrations", async () => {
  delete process.env.DUDE_MIGRATIONS_DIR;
  const files = await listMigrationFiles();
  expect(files[0]?.name).toBe("001_initial.sql");
});

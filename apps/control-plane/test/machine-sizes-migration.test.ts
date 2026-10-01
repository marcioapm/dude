/**
 * Migration 063, machine sizes: every organization has exactly one size
 * from the start — those that existed before it, and those made after —
 * and the database itself refuses a size off its steps, a second default,
 * and a name used twice.
 *
 * Applies the migrations before 063 to a database of its own, adds an
 * organization, then applies 063, as a deploy would meet one.
 *
 * Requires DATABASE_URL: a role that can create databases (the owner).
 */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { listMigrationFiles } from "../src/db/migrate.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const NAME = `dude_msizes_test_${Bun.randomUUIDv7("hex").slice(-12)}`;

let admin: SQL;
let db: SQL;

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const url = new URL(OWNER_URL);
  url.pathname = `/${NAME}`;
  db = new SQL(url.toString());
  const files = await listMigrationFiles();
  // As the runner has it before the first file (migrate.ts, ensureMigrationsTable).
  await db`CREATE TABLE schema_migrations (version text PRIMARY KEY, name text NOT NULL, checksum text NOT NULL,
           applied_at timestamptz NOT NULL DEFAULT now())`;
  for (const f of files.filter((f) => f.version < "063")) await db.unsafe(await f.contents());
  await db`INSERT INTO organizations (id, name, slug) VALUES ('org_before', 'Before', 'before')`;
  for (const f of files.filter((f) => f.version >= "063")) await db.unsafe(await f.contents());
}, 120_000);

afterAll(async () => {
  await db?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

const sizes = (org: string) =>
  db`SELECT name, cpus::float8 AS cpus, memory_mib AS "memoryMiB", disk_gib AS "diskGiB", pool, is_default AS "isDefault"
     FROM machine_sizes WHERE organization_id = ${org}`;
const STANDARD = { name: "Standard", cpus: 2, memoryMiB: 8192, diskGiB: 20, pool: null, isDefault: true };

describe("seeding", () => {
  test("an organization from before the migration has Standard, as its default", async () => {
    expect(await sizes("org_before")).toEqual([STANDARD]);
  });

  test("an organization made after it gets Standard too", async () => {
    await db`INSERT INTO organizations (id, name, slug) VALUES ('org_after', 'After', 'after')`;
    expect(await sizes("org_after")).toEqual([STANDARD]);
  });
});

describe("the database refuses what the API would", () => {
  /** The error inserting a size raises, or "" when it is taken. */
  const insert = async (id: string, over: Record<string, unknown> = {}): Promise<string> => {
    const s = { name: id, cpus: 4, memory_mib: 8192, disk_gib: 40, pool: null, is_default: false, ...over };
    try {
      await db`INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib, pool, is_default)
               VALUES (${id}, 'org_before', ${s.name}, ${s.cpus}, ${s.memory_mib}, ${s.disk_gib}, ${s.pool}, ${s.is_default})`;
      return "";
    } catch (err) {
      return String(err);
    }
  };

  test("half steps are sizes", async () => {
    expect(await insert("half", { cpus: 6.5, memory_mib: 23040, disk_gib: 125, pool: "big" })).toBe("");
  });

  test("the most of each is a size", async () => {
    expect(await insert("most", { cpus: 256, memory_mib: 2097152, disk_gib: 20000 })).toBe("");
    await db`DELETE FROM machine_sizes WHERE id = 'most'`;
  });

  for (const [what, over] of [
    ["CPUs off the half step", { cpus: 2.3 }],
    ["CPUs just off it", { cpus: 2.04 }],
    ["no CPUs", { cpus: 0 }],
    ["memory off 512 MiB", { memory_mib: 1000 }],
    ["no memory", { memory_mib: 0 }],
    ["disk off 5 GiB", { disk_gib: 12 }],
    ["no disk", { disk_gib: 0 }],
    ["too many CPUs", { cpus: 256.5 }],
    ["too much memory", { memory_mib: 2097152 + 512 }],
    ["too much disk", { disk_gib: 20005 }],
    ["an empty name", { name: "" }],
    ["a name over 40", { name: "x".repeat(41) }],
    ["a pool lux would not name", { pool: "Big Pool" }],
  ] as const) {
    test(`${what}`, async () => {
      expect(await insert(`bad_${what.replaceAll(" ", "_")}`, over)).toMatch(/check constraint/);
    });
  }

  test("a second default", async () => {
    expect(await insert("second_default", { is_default: true })).toMatch(/machine_sizes_one_default/);
  });

  test("the default moves in one statement, whichever row it meets first", async () => {
    expect(await insert("next_default")).toBe("");
    for (const to of ["next_default", "half", "next_default"]) {
      await db`UPDATE machine_sizes SET is_default = (id = ${to}) WHERE organization_id = 'org_before' AND (is_default OR id = ${to})`;
      expect((await db`SELECT id FROM machine_sizes WHERE organization_id = 'org_before' AND is_default`).map((r: { id: string }) => r.id)).toEqual([to]);
    }
  });

  test("a name used twice, whatever its case", async () => {
    expect(await insert("dup", { name: "standard" })).toMatch(/machine_sizes_name_idx/);
  });
});

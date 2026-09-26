/**
 * Migration runner.
 *
 * Raw .sql files are the source of truth for schema (the Drizzle schema in
 * ./schema.ts mirrors them for typed queries but never drives DDL). Each file
 * runs once, inside a transaction, and is recorded in schema_migrations.
 *
 *   bun run src/db/migrate.ts                  # apply pending migrations
 *   bun run src/db/migrate.ts --status         # list applied/pending
 *
 * DUDE_MIGRATIONS_DIR names the directory of .sql files; by default, the
 * repository's migrations/.
 */

import { readdir, readFile } from "node:fs/promises";
import { join } from "node:path";
import { SQL } from "bun";
import { createHash } from "node:crypto";

const REPO_MIGRATIONS_DIR = join(import.meta.dir, "../../../../migrations");

export function migrationsDir(): string {
  return process.env.DUDE_MIGRATIONS_DIR || REPO_MIGRATIONS_DIR;
}

export interface MigrationFile {
  version: string;
  name: string;
  path: string;
}

export async function listMigrationFiles(dir = migrationsDir()): Promise<MigrationFile[]> {
  const entries = await readdir(dir);
  return entries
    .filter((f) => f.endsWith(".sql"))
    .sort()
    .map((f) => {
      const version = f.split("_")[0];
      if (!version) throw new Error(`Migration ${f} must start with a version prefix`);
      return { version, name: f, path: join(dir, f) };
    });
}

async function ensureMigrationsTable(sql: SQL): Promise<void> {
  await sql`
    CREATE TABLE IF NOT EXISTS schema_migrations (
      version     text PRIMARY KEY,
      name        text NOT NULL,
      checksum    text NOT NULL,
      applied_at  timestamptz NOT NULL DEFAULT now()
    )`;
}

async function appliedVersions(sql: SQL): Promise<Map<string, string>> {
  const rows = await sql`SELECT version, checksum FROM schema_migrations`;
  return new Map(rows.map((r: { version: string; checksum: string }) => [r.version, r.checksum]));
}

/**
 * Migrations move every organization's rows (renames, backfills). Under
 * row-level security a role that does not bypass it sees none of them, so
 * those statements would match nothing and succeed — and the migration be
 * recorded as done. Refuse up front instead.
 */
async function requireBypassRLS(sql: SQL): Promise<void> {
  const [role] = await sql`SELECT rolsuper OR rolbypassrls AS ok FROM pg_roles WHERE rolname = current_user`;
  if (!role?.ok) {
    throw new Error(
      "Migrations must run as a role that bypasses row-level security (the owner, e.g. dude): " +
        "as any other role their data changes would silently touch no rows.",
    );
  }
}

function checksum(contents: string): string {
  return createHash("sha256").update(contents).digest("hex");
}

export async function migrate(
  databaseUrl: string,
  opts: { dir?: string; log?: (msg: string) => void } = {},
): Promise<{ applied: string[] }> {
  const log = opts.log ?? console.log;
  const sql = new SQL(databaseUrl);
  const applied: string[] = [];

  try {
    await ensureMigrationsTable(sql);
    const already = await appliedVersions(sql);
    const files = await listMigrationFiles(opts.dir);
    if (files.some((f) => !already.has(f.version))) await requireBypassRLS(sql);

    for (const file of files) {
      const contents = await readFile(file.path, "utf8");
      const sum = checksum(contents);
      const priorSum = already.get(file.version);

      if (priorSum !== undefined) {
        // An edited migration means the DB and the repo disagree about what
        // the schema is. Refuse rather than silently diverge.
        if (priorSum !== sum) {
          throw new Error(
            `Migration ${file.name} was modified after it was applied ` +
              `(recorded ${priorSum.slice(0, 12)}, now ${sum.slice(0, 12)}). ` +
              `Add a new migration instead of editing an applied one.`,
          );
        }
        continue;
      }

      log(`applying ${file.name}`);
      await sql.begin(async (tx) => {
        await tx.unsafe(contents);
        await tx`INSERT INTO schema_migrations (version, name, checksum)
                 VALUES (${file.version}, ${file.name}, ${sum})`;
      });
      applied.push(file.name);
    }
  } finally {
    await sql.end();
  }

  return { applied };
}

async function status(databaseUrl: string): Promise<void> {
  const sql = new SQL(databaseUrl);
  try {
    await ensureMigrationsTable(sql);
    const already = await appliedVersions(sql);
    for (const file of await listMigrationFiles()) {
      console.log(`${already.has(file.version) ? "applied" : "pending"}  ${file.name}`);
    }
  } finally {
    await sql.end();
  }
}

if (import.meta.main) {
  const databaseUrl = process.env.DATABASE_URL;
  if (!databaseUrl) {
    console.error("DATABASE_URL is required");
    process.exit(1);
  }

  if (process.argv.includes("--status")) {
    await status(databaseUrl);
  } else {
    const { applied } = await migrate(databaseUrl);
    console.log(applied.length ? `applied ${applied.length} migration(s)` : "up to date");
  }
}

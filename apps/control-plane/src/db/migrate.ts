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
 * Run from source, the files are the repository's migrations/. The release
 * binary bin/dude-migrate (scripts/build-migrate.sh) carries them inside
 * itself and reads nothing from disk.
 */

import { readdir, readFile } from "node:fs/promises";
import { join } from "node:path";
import { SQL } from "bun";
import { createHash } from "node:crypto";
import { version } from "../build.ts";

// Defined by scripts/build-migrate.sh, which embeds migrations/*.sql.
declare const DUDE_EMBEDDED_MIGRATIONS: boolean | undefined;
const embedded = typeof DUDE_EMBEDDED_MIGRATIONS === "boolean" && DUDE_EMBEDDED_MIGRATIONS;

export const repoMigrationsDir = join(import.meta.dir, "../../../../migrations");

export interface MigrationFile {
  version: string;
  name: string;
  /** The file's text. Decoded the same way from disk and from the binary, so its checksum is too. */
  contents(): Promise<string>;
}

function decode(bytes: Uint8Array): string {
  return Buffer.from(bytes.buffer, bytes.byteOffset, bytes.byteLength).toString("utf8");
}

function migrationFile(name: string, read: () => Promise<Uint8Array>): MigrationFile {
  const version = name.split("_")[0];
  if (!version) throw new Error(`Migration ${name} must start with a version prefix`);
  return { version, name, contents: async () => decode(await read()) };
}

export async function listMigrationFiles(): Promise<MigrationFile[]> {
  if (embedded) {
    const files = Bun.embeddedFiles
      .map((blob) => ({ blob, name: (blob as Blob & { name: string }).name }))
      .filter((f) => f.name.endsWith(".sql"));
    if (files.length === 0) throw new Error("this dude-migrate was built without its migrations");
    return files
      .sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0))
      .map((f) => migrationFile(f.name, async () => new Uint8Array(await f.blob.arrayBuffer())));
  }
  const entries = await readdir(repoMigrationsDir);
  return entries
    .filter((f) => f.endsWith(".sql"))
    .sort()
    .map((f) => migrationFile(f, () => readFile(join(repoMigrationsDir, f))));
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

/**
 * Checksums a migration was released with before an edit, still accepted from a
 * database that applied it then. Only for an edit that leaves such a database
 * the same: 002's ALTER ROLE became conditional so an owner that is not a
 * superuser can apply it; wherever the old text ran, it ran as a superuser.
 */
export const PRIOR_CHECKSUMS: Readonly<Record<string, readonly string[]>> = {
  "002": ["9629d868e268365046359588f469a4834f20a1418c6f3954f8f56797d649cb86"],
};

function checksum(contents: string): string {
  return createHash("sha256").update(contents).digest("hex");
}

export async function migrate(
  databaseUrl: string,
  opts: { log?: (msg: string) => void } = {},
): Promise<{ applied: string[] }> {
  const log = opts.log ?? console.log;
  const sql = new SQL(databaseUrl);
  const applied: string[] = [];

  try {
    await ensureMigrationsTable(sql);
    const already = await appliedVersions(sql);
    const files = await listMigrationFiles();
    if (files.some((f) => !already.has(f.version))) await requireBypassRLS(sql);

    for (const file of files) {
      const contents = await file.contents();
      const sum = checksum(contents);
      const priorSum = already.get(file.version);

      if (priorSum !== undefined) {
        // An edited migration means the DB and the repo disagree about what
        // the schema is. Refuse rather than silently diverge.
        if (priorSum !== sum && !PRIOR_CHECKSUMS[file.version]?.includes(priorSum)) {
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
  if (process.argv.includes("--version")) {
    console.log(version);
    process.exit(0);
  }
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

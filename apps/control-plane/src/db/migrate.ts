/**
 * Migration runner.
 *
 * Raw .sql files are the source of truth for schema (the Drizzle schema in
 * ./schema.ts mirrors them for typed queries but never drives DDL). Each file
 * runs once, inside a transaction, and is recorded in schema_migrations.
 *
 * A file whose first line is `-- dude:no-transaction` runs outside one, for
 * a statement Postgres refuses in a transaction (CREATE INDEX CONCURRENTLY):
 * one statement, recorded once it succeeded. It must be safe to run again
 * (IF NOT EXISTS), as a crash between the statement and its record runs it
 * again on the next migrate. One that builds an index concurrently is
 * recorded only once the index is valid (dropIfInvalid).
 *
 * A file whose first line is `-- dude:batch-sql` is a batch migration, for a
 * data change too large for one transaction: it runs in many transactions
 * and resumes where it stopped (see BATCH_SQL).
 *
 * One migrate runs at a time per database: each holds a session advisory
 * lock (MIGRATE_LOCK) for its whole run and reads what is applied only once
 * it has it. A second polls for the lock, holding no snapshot while it waits.
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
import { SQL, type ReservedSQL } from "bun";
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

/** The marker on a migration's first line that runs it outside a transaction. */
export const NO_TRANSACTION = "-- dude:no-transaction";

/**
 * The marker on a batch migration's first line. Its protocol:
 *
 * - Sections. After the marker, comment lines, then `-- dude:setup`,
 *   `-- dude:batch` and optionally `-- dude:cleanup`, each on a line of its
 *   own and each at most once; setup and batch must not be empty. An
 *   optional `-- dude:batch-limit N` line sets the limit (default 1000).
 * - Placeholder. `__BATCH_LIMIT__` in the batch is replaced by the limit.
 * - Transactions. Setup runs in one transaction. Then the batch runs again
 *   and again, each run its own transaction, so a batch's row locks and
 *   writes are released at its commit. Last, cleanup runs in one
 *   transaction with the file's schema_migrations record: a cleanup never
 *   commits unless the file is recorded.
 * - batch_count. Each batch must return exactly one row with an integer
 *   batch_count column in 0..limit; anything else fails the migration,
 *   which is then not recorded. The runner stops after a batch reports 0,
 *   so a batch reports more than 0 while work remains, even when it changed
 *   no row itself (277 reads one window of cursors per batch and reports 1
 *   while windows remain).
 * - Resumability. The batch keeps its own progress in the database (277's
 *   progress table), so a migrate interrupted between batches, or refused
 *   at its record, picks up from the last committed batch when it is run
 *   again. Setup must therefore be idempotent and keep saved progress, and
 *   a batch must be safe to run again over rows an earlier one filled.
 */
export const BATCH_SQL = "-- dude:batch-sql";

interface BatchMigration {
  limit: number;
  setup: string;
  batch: string;
  cleanup: string;
}

function batchMigration(contents: string): BatchMigration | null {
  if (contents.split("\n", 1)[0]?.trim() !== BATCH_SQL) return null;
  const limitMatch = contents.match(/^-- dude:batch-limit(?:=|\s+)(\d+)\s*$/m);
  const limit = Number(limitMatch?.[1] ?? "1000");
  if (!Number.isInteger(limit) || limit <= 0) throw new Error("dude:batch-limit must be a positive integer");
  const sections = new Map<string, string>();
  // Lines before the first marker are the file's header: comments only.
  let section = "";
  const lines: string[] = [];
  const save = () => {
    const text = lines.join("\n").trim();
    if (section === "") {
      if (text.replace(/^--.*$/gm, "").trim()) throw new Error("dude:batch-sql has SQL before its first section marker");
    } else {
      sections.set(section, text);
    }
  };
  for (const line of contents.split("\n").slice(1)) {
    const marker = /^-- dude:(setup|batch|cleanup)\s*$/.exec(line.trim());
    if (marker) {
      save();
      section = marker[1]!;
      if (sections.has(section)) throw new Error(`dude:batch-sql has more than one ${section} section`);
      lines.length = 0;
    } else if (!/^-- dude:batch-limit(?:=|\s+)(?:\d+)\s*$/.test(line.trim())) {
      lines.push(line);
    }
  }
  save();
  if (!sections.get("setup") || !sections.get("batch")) throw new Error("dude:batch-sql requires setup and batch sections");
  return { limit, setup: sections.get("setup")!, batch: sections.get("batch")!, cleanup: sections.get("cleanup") ?? "" };
}

/**
 * The batch_count a batch's result reports: exactly one row, with a
 * batch_count column holding an integer in 0..limit. Anything else throws,
 * so a batch that reports nothing is never taken for one that found nothing.
 */
export function batchCount(result: ReadonlyArray<Record<string, unknown>>, limit: number): number {
  if (result.length !== 1) throw new Error(`a batch must return exactly one row, not ${result.length}`);
  const row = result[0]!;
  if (!Object.hasOwn(row, "batch_count")) throw new Error("a batch must return a batch_count column");
  const raw = row.batch_count;
  const count = typeof raw === "number" ? raw
    : typeof raw === "bigint" ? Number(raw)
    : typeof raw === "string" && /^-?\d+$/.test(raw) ? Number(raw)
    : Number.NaN;
  if (!Number.isInteger(count)) throw new Error(`a batch's batch_count must be a non-null integer, not ${raw === null ? "NULL" : JSON.stringify(raw)}`);
  if (count < 0 || count > limit) throw new Error(`a batch's batch_count ${count} is outside 0..${limit}`);
  return count;
}

async function runBatchMigration(sql: SQL, file: MigrationFile, contents: string, sum: string, log: (msg: string) => void): Promise<void> {
  const batch = batchMigration(contents)!;
  await sql.begin(async (tx) => { await tx.unsafe(batch.setup); });
  for (;;) {
    const rows = await sql.begin(async (tx) => {
      const result = await tx.unsafe(batch.batch.replaceAll("__BATCH_LIMIT__", String(batch.limit)));
      try {
        return batchCount(result as Array<Record<string, unknown>>, batch.limit);
      } catch (err) {
        throw new Error(`${file.name}: ${(err as Error).message}`);
      }
    });
    log(`applied ${file.name} batch (${rows} rows)`);
    if (rows === 0) break;
  }
  await sql.begin(async (tx) => {
    if (batch.cleanup) await tx.unsafe(batch.cleanup);
    await tx`INSERT INTO schema_migrations (version, name, checksum) VALUES (${file.version}, ${file.name}, ${sum})`;
  });
}

export function outsideTransaction(contents: string): boolean {
  return contents.split("\n", 1)[0]?.trim() === NO_TRANSACTION || contents.split("\n", 1)[0]?.trim() === BATCH_SQL;
}

/** The index a no-transaction file builds with CREATE INDEX CONCURRENTLY, or null. */
export function concurrentIndex(contents: string): string | null {
  const m = /CREATE\s+(?:UNIQUE\s+)?INDEX\s+CONCURRENTLY\s+(?:IF\s+NOT\s+EXISTS\s+)?("[^"]+"|[\w.]+)/i.exec(contents);
  return m?.[1] ?? null;
}

/**
 * A concurrent build that was cancelled or failed leaves its index in the
 * catalog, INVALID, and IF NOT EXISTS would skip it on the next run. Such an
 * index is dropped (concurrently: no lock that blocks writes) before its
 * file runs, and again if the file fails, which is then not recorded: the
 * next migrate builds it afresh, and a later file that drops the index it
 * replaces never runs over an invalid one. Says whether it dropped one.
 */
async function dropIfInvalid(sql: SQL, index: string): Promise<boolean> {
  const [row] = await sql`SELECT NOT i.indisvalid AS invalid FROM pg_index i WHERE i.indexrelid = to_regclass(${index})`;
  if (!row?.invalid) return false;
  await sql.unsafe(`DROP INDEX CONCURRENTLY IF EXISTS ${index}`);
  return true;
}

/**
 * Refuses to touch index while a build of it is still running: a migrate
 * retried after a timeout whose killed client's backend builds on would
 * otherwise wait that build out, drop the index it made, and build it again.
 */
async function refuseWhileBuilding(sql: SQL, index: string): Promise<void> {
  if (await building(sql, index)) throw new Error(`a build of ${index} is still running; migrate again when it finishes`);
}

async function building(sql: SQL, index: string): Promise<boolean> {
  const [row] = await sql`SELECT EXISTS (SELECT 1 FROM pg_stat_progress_create_index
    WHERE index_relid = to_regclass(${index})) AS building`;
  return row?.building === true;
}

/** The advisory lock (hashtext of this key) a migrate holds for its whole run. */
export const MIGRATE_LOCK = "dude-migrate";

/** How long a migrate waiting for MIGRATE_LOCK sleeps between tries. */
const MIGRATE_LOCK_POLL_MS = 1000;

/**
 * Applies one migration file and records it in schema_migrations, as
 * migrate does: a dude:batch-sql file batch by batch, a dude:no-transaction
 * file as one simple-Query message, any other in one transaction with its
 * record. Checks nothing about what is already recorded; migrate does. For
 * a test that builds a database at an older release, so it applies each
 * file exactly as the runner would.
 */
export async function applyMigration(sql: SQL, file: MigrationFile, contents: string, log: (msg: string) => void = () => {}): Promise<void> {
  const sum = checksum(contents);
  if (batchMigration(contents)) {
    await runBatchMigration(sql, file, contents, sum, log);
    return;
  }
  if (outsideTransaction(contents)) {
    const index = concurrentIndex(contents);
    // An earlier interrupted build's leftover, which IF NOT EXISTS would
    // skip; never one another backend is still building.
    if (index) {
      await refuseWhileBuilding(sql, index);
      await dropIfInvalid(sql, index);
    }
    // Postgres reports a cancelled or failed CREATE INDEX CONCURRENTLY
    // as an error, so an index this statement leaves invalid is dropped
    // here and the file not recorded. A success never leaves it invalid:
    // the leftover IF NOT EXISTS could skip was dropped just above, and
    // none was being built.
    // A statement that failed on the client's side (a dropped connection)
    // can leave its backend still building: that index is left alone,
    // and the next migrate refuses until the build ends. A failed check
    // drops nothing either.
    try {
      await sql.unsafe(contents);
    } catch (err) {
      if (index && !(await building(sql, index).catch(() => true))) await dropIfInvalid(sql, index);
      throw err;
    }
    await sql`INSERT INTO schema_migrations (version, name, checksum)
              VALUES (${file.version}, ${file.name}, ${sum})`;
    return;
  }
  await sql.begin(async (tx) => {
    await tx.unsafe(contents);
    await tx`INSERT INTO schema_migrations (version, name, checksum)
             VALUES (${file.version}, ${file.name}, ${sum})`;
  });
}

export async function migrate(
  databaseUrl: string,
  opts: {
    log?: (msg: string) => void;
    /** The files to apply in place of the release's (listMigrationFiles); for tests. */
    files?: MigrationFile[];
  } = {},
): Promise<{ applied: string[] }> {
  const log = opts.log ?? console.log;
  // In UTC: a migration that renders a time into stored JSON (090, 091) renders it at "+00:00".
  const sql = new SQL(databaseUrl, { connection: { TimeZone: "UTC" } });
  const applied: string[] = [];
  let lock: ReservedSQL | undefined;

  try {
    // One migrate at a time per database: a second waits here for the
    // first to finish, then reads what it recorded. Without this a second
    // runner that read the applied versions before the first finished would
    // re-run a batch file's setup after the cleanup that followed it, and
    // leave its progress table behind. A session lock on a connection of
    // its own, released below or with the connection on any error.
    // Polled, never waited for inside a statement: a blocked
    // pg_advisory_lock holds a snapshot, which the first's CREATE INDEX
    // CONCURRENTLY waits out while the first waits for its index, a cycle
    // through this process that Postgres's deadlock detector never sees.
    lock = await sql.reserve();
    for (let waiting = false; ;) {
      const [got] = await lock.unsafe(`SELECT pg_try_advisory_lock(hashtext('${MIGRATE_LOCK}')) AS ok`);
      if (got.ok) break;
      if (!waiting) log("waiting for another migrate to finish");
      waiting = true;
      await Bun.sleep(MIGRATE_LOCK_POLL_MS);
    }
    await ensureMigrationsTable(sql);
    const already = await appliedVersions(sql);
    const files = opts.files ?? await listMigrationFiles();
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
      await applyMigration(sql, file, contents, log);
      applied.push(file.name);
    }
  } finally {
    if (lock) {
      // A connection that broke drops its session lock with it.
      await lock.unsafe(`SELECT pg_advisory_unlock(hashtext('${MIGRATE_LOCK}'))`).catch(() => {});
      lock.release();
    }
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

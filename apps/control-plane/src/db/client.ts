/**
 * Database access and tenant scoping.
 *
 * The control plane connects as `dude_app`, a role with neither SUPERUSER nor
 * BYPASSRLS, so the row-level security policies in 001_initial.sql are a real
 * boundary rather than a convention. Three access modes, in descending order
 * of how often they should appear:
 *
 *  1. `withOrg` — tenant tables, inside a transaction that has set
 *     `app.organization_id`. Forgetting it yields zero rows, never another
 *     tenant's data. Nearly all application code wants this.
 *  2. `withoutTenant` — the genuinely global tables (organizations, users,
 *     org_memberships, workers). Named to be conspicuous in review.
 *  3. `withSystemScope` — cross-tenant background sweeps. Enters a role that
 *     may bypass RLS, so its use is deliberately restricted and audited.
 */

import { SQL, type TransactionSQL } from "bun";

/** A transaction already scoped to one organization. */
export interface OrgScope {
  readonly organizationId: string;
  readonly sql: TransactionSQL;
}

/** A transaction that can see every tenant's rows. */
export interface SystemScope {
  /** Names the sweeper, so a slow or stuck query is attributable. */
  readonly sweeper: string;
  readonly sql: TransactionSQL;
}

let pool: SQL | null = null;

export function getPool(databaseUrl = process.env.DATABASE_URL): SQL {
  if (!pool) {
    if (!databaseUrl) throw new Error("DATABASE_URL is not set");
    pool = new SQL(databaseUrl);
  }
  return pool;
}

/** Replace the pool — used by tests to point at a per-run database. */
export function setPool(next: SQL | null): void {
  pool = next;
}

/**
 * Close the pool, but only if `expected` is still the current one.
 *
 * The pool is a module global and `bun test` runs every file in one process,
 * so without the guard a file closing its pool in `afterAll` can sever the
 * connection a concurrently running file is still using — which surfaces as
 * an unrelated test failing at random.
 */
export async function closePool(expected?: SQL | null): Promise<void> {
  if (expected !== undefined && pool !== expected) return;
  await pool?.end();
  pool = null;
}

/**
 * Run `fn` inside a transaction scoped to `organizationId`.
 *
 * `set_config(..., true)` is transaction-local, so the setting is discarded on
 * commit or rollback and can never leak to the next borrower of the pooled
 * connection. This is the only supported way to touch tenant tables.
 */
export async function withOrg<T>(
  organizationId: string,
  fn: (scope: OrgScope) => Promise<T>,
): Promise<T> {
  if (!organizationId) {
    throw new Error("withOrg requires an organizationId");
  }
  return getPool().begin(async (tx) => {
    await tx`SELECT set_config('app.organization_id', ${organizationId}, true)`;
    return fn({ organizationId, sql: tx });
  });
}

/**
 * Run `fn` against tables that are not tenant-scoped: organizations, users,
 * org_memberships, workers, schema_migrations.
 *
 * Tenant tables accessed here return nothing, because no organization is set.
 */
export async function withoutTenant<T>(
  fn: (ctx: { sql: TransactionSQL }) => Promise<T>,
): Promise<T> {
  return getPool().begin(async (tx) => fn({ sql: tx }));
}

/**
 * Run a named background sweep across every tenant.
 *
 * Some work cannot name a tenant because finding the tenants that need
 * attention *is* the work: claiming the oldest runnable workflow, dispatching
 * the outbox, reclaiming expired run leases, marking lost workers. Enumerating
 * organizations and polling each would turn one indexed query into N and make
 * fairness impossible.
 *
 * `SET LOCAL ROLE` is transaction-scoped, so the elevated role is dropped on
 * commit or rollback and cannot leak to the next borrower of the connection.
 *
 * Two rules for anything written against this:
 *   - it must be one of the named sweepers below, started by the control
 *     plane — not a convenience for a request handler that finds `withOrg`
 *     inconvenient;
 *   - once it has a row, it should carry that row's own `organization_id`
 *     into a normal `withOrg` transaction rather than keep reading across
 *     tenants.
 */
export type SweeperName =
  | "workflow-poller"
  | "outbox-dispatcher"
  | "run-lease-reaper"
  | "worker-liveness-reaper"
  | "phase-notifier";

export async function withSystemScope<T>(
  sweeper: SweeperName,
  fn: (scope: SystemScope) => Promise<T>,
): Promise<T> {
  return getPool().begin(async (tx) => {
    await tx`SET LOCAL ROLE dude_sweeper`;
    // Attributes the query to its sweeper in pg_stat_activity, so a slow or
    // stuck sweep is identifiable without guessing.
    await tx`SELECT set_config('application_name', ${`dude:${sweeper}`}, true)`;
    return fn({ sweeper, sql: tx });
  });
}

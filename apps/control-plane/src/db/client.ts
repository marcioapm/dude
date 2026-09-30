/**
 * Database access and tenant scoping.
 *
 * The backend connects as `dude_app`, a role with neither SUPERUSER nor
 * BYPASSRLS, so the row-level security policies in 001_initial.sql are a real
 * boundary rather than a convention. Two access modes:
 *
 *  1. `withOrg` — tenant tables, inside a transaction that has set
 *     `app.organization_id`. Forgetting it yields zero rows, never another
 *     tenant's data. Nearly all code wants this.
 *  2. `withoutTenant` — the genuinely global tables (organizations, users,
 *     org_memberships). Named to be conspicuous in review.
 *
 * Cross-tenant background work belongs to the orchestrator, not here.
 */

import { SQL, type TransactionSQL } from "bun";
import { config } from "../config.ts";

/** A transaction already scoped to one organization. */
export interface OrgScope {
  readonly organizationId: string;
  readonly sql: TransactionSQL;
}

let pool: SQL | null = null;

export function getPool(databaseUrl?: string): SQL {
  if (!pool) {
    databaseUrl ??= config().databaseUrl;
    if (!databaseUrl) throw new Error("database.url (DATABASE_URL) is not set");
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
 * org_memberships, schema_migrations.
 *
 * Tenant tables accessed here return nothing, because no organization is set.
 */
export async function withoutTenant<T>(
  fn: (ctx: { sql: TransactionSQL }) => Promise<T>,
): Promise<T> {
  return getPool().begin(async (tx) => fn({ sql: tx }));
}

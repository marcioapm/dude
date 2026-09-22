/**
 * Database access and tenant scoping.
 *
 * The control plane connects as `dude_app`, a role with neither SUPERUSER nor
 * BYPASSRLS, so the row-level security policies in 001_initial.sql are a real
 * boundary rather than a convention. Two consequences shape this module:
 *
 *  1. Tenant tables are only reachable inside a transaction that has set
 *     `app.organization_id`. `withOrg` is that transaction; forgetting it
 *     yields zero rows, never another tenant's data.
 *  2. `withoutTenant` exists for the genuinely global tables (organizations,
 *     users, workers) and is named to make its use conspicuous in review.
 */

import { SQL, type TransactionSQL } from "bun";

/** A transaction already scoped to one organization. */
export interface OrgScope {
  readonly organizationId: string;
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

export async function closePool(): Promise<void> {
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

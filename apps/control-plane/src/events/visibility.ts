/**
 * Who may see a brainstorm session's events: its accepted members, and no
 * one else in the organisation — admins included. The ledger's reads
 * filter in SQL (session_visible); the live bus hands every event of the
 * organisation to every stream, so each stream asks here before it sends
 * one that names a session.
 *
 * Answers are kept a few seconds per person and session, and forgotten for
 * a session the moment its membership changes (its session.* events), so
 * someone removed stops receiving at once.
 */

import { withOrg } from "../db/client.ts";

/** Brainstorm sessions' ids (an agent's sessions inside a Run are `ses_`). */
export const BRAINSTORM_PREFIX = "ssn_";

const KEEP_MS = 5_000;

const answers = new Map<string, { ok: boolean; at: number }>();

/** Membership changes: what makes a cached answer about a session stale. */
const MEMBERSHIP_EVENTS = new Set([
  "session.shared", "session.joined", "session.declined", "session.role_changed",
  "session.member_removed", "session.owner_changed",
]);

export function forgetSession(sessionId: string): void {
  for (const key of answers.keys()) if (key.endsWith(`:${sessionId}`)) answers.delete(key);
}

/** Called for every event the bus publishes, before any stream reads it. */
export function noteEvent(eventType: string, sessionId: string | null): void {
  if (sessionId && MEMBERSHIP_EVENTS.has(eventType)) forgetSession(sessionId);
}

/** Whether `personId` may see an event on `sessionId` (null: no session). */
export async function canSee(organizationId: string, personId: string, sessionId: string | null): Promise<boolean> {
  if (!sessionId || !sessionId.startsWith(BRAINSTORM_PREFIX)) return true;
  const key = `${personId}:${sessionId}`;
  const known = answers.get(key);
  if (known && Date.now() - known.at < KEEP_MS) return known.ok;
  const ok = await withOrg(organizationId, async ({ sql }) => {
    const rows = (await sql`SELECT session_visible(${sessionId}, ${personId}) AS ok`) as Array<{ ok: boolean }>;
    return rows[0]?.ok === true;
  });
  answers.set(key, { ok, at: Date.now() });
  return ok;
}

/**
 * Whether a read that names a brainstorm session (`?sessionId=ssn_…`) is
 * an accepted member's. Anyone else is answered as if it did not exist,
 * as the session's own routes answer them. Not cached: asked once per read.
 */
export async function mayReadSession(organizationId: string, personId: string, sessionId: string | undefined): Promise<boolean> {
  if (!sessionId?.startsWith(BRAINSTORM_PREFIX)) return true;
  return withOrg(organizationId, async ({ sql }) => {
    const rows = (await sql`SELECT session_role(${sessionId}, ${personId}) IS NOT NULL AS ok`) as Array<{ ok: boolean }>;
    return rows[0]?.ok === true;
  });
}

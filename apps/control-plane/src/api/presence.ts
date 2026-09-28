/**
 * Presence: who is here. Any authenticated request says its person is
 * around; `people.last_seen_at` is touched at most once a minute per
 * person (this process remembers when it last did), and each touch is
 * announced as a live `person.seen` event so open pages update their
 * Online row. No Redis: at this size a row per person is enough, and the
 * announcement rides the NOTIFY channel the ledger already uses.
 */

import type { PersonRef } from "@dude/domain";
import { withOrg } from "../db/client.ts";
import type { Principal } from "./auth.ts";

export const TOUCH_EVERY_MS = 60_000;

const touched = new Map<string, number>();

/** Whether `personId` is due a touch at `now`, marking it touched if so. */
export function due(seen: Map<string, number>, personId: string, now: number, every = TOUCH_EVERY_MS): boolean {
  const last = seen.get(personId);
  if (last !== undefined && now - last < every) return false;
  seen.set(personId, now);
  return true;
}

/**
 * What the person had open, as their browser names it (`x-dude-where`,
 * "TEXT-14" or "Settings"): shown beside their face. Plain text, short.
 */
function whereFrom(header: string | null): string | null {
  const where = header?.replace(/[\u0000-\u001f]/g, "").trim().slice(0, 80);
  return where ? where : null;
}

/**
 * Note that `principal`'s person is here, before their request is
 * answered, so what it reads counts them. Once a minute it costs one small
 * write; the rest of the time nothing. Best-effort: never fails a request.
 */
export async function touch(principal: Principal, whereHeader: string | null): Promise<void> {
  if (!due(touched, principal.personId, Date.now())) return;
  const where = whereFrom(whereHeader);
  await withOrg(principal.organizationId, async ({ sql }) => {
    const rows = (await sql`
      UPDATE people SET last_seen_at = now(), last_seen_where = COALESCE(${where}, last_seen_where)
      WHERE id = ${principal.personId} AND removed_at IS NULL
      RETURNING person_ref(people) AS person`) as Array<{ person: PersonRef }>;
    if (!rows[0]) return;
    // Heard by every backend's live stream (events/listen.ts), on commit.
    await sql`SELECT pg_notify('dude_events', ${JSON.stringify({
      organizationId: principal.organizationId,
      seen: { ...rows[0].person, where },
    })})`;
  }).catch(() => touched.delete(principal.personId));
}

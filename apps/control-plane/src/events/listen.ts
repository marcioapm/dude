/**
 * Feed the live event bus from the database, not from this process.
 *
 * Most events are written by the orchestrator, a separate process, so an
 * in-process publish after each append can no longer see them. Every insert
 * into `events` raises a NOTIFY (migration 014); this listens for it,
 * re-reads rows by cursor — NOTIFY payloads are capped at 8000 bytes and an
 * event's payload is not — and publishes them to local subscribers.
 *
 * One connection for the whole process. `Bun.sql` cannot LISTEN, so this is
 * the one place that uses the postgres.js driver.
 *
 * Two properties matter:
 *
 * - **Nothing in a burst is dropped.** Reads are paged until they catch up,
 *   and a read that fails keeps its position to try again.
 * - **Order is cursor order.** Cursors are handed out at insert but become
 *   visible at commit, so two transactions can commit — and notify — out of
 *   order. Subscribers keep a high-water mark, so publishing 105 before 104
 *   would lose 104. Notifications are gathered for a moment before reading,
 *   which puts transactions that commit close together back in order.
 */

import postgres from "postgres";
import { eventBus } from "./bus.ts";
import * as ledger from "./ledger.ts";

interface Notice {
  cursor: number;
  organizationId: string;
}

/** How long notifications are gathered before the events are read. */
const SETTLE_MS = 150;
const PAGE = 1000;

export async function listenForEvents(databaseUrl: string): Promise<() => Promise<void>> {
  const sql = postgres(databaseUrl, { max: 1, onnotice: () => {} });
  // Per organization: the lowest cursor notified and not yet published.
  const waiting = new Map<string, number>();
  let timer: ReturnType<typeof setTimeout> | null = null;
  let draining = false;

  const drain = async () => {
    timer = null;
    if (draining) return;
    draining = true;
    try {
      while (waiting.size > 0) {
        const [organizationId, from] = waiting.entries().next().value!;
        waiting.delete(organizationId);
        let after = from - 1;
        try {
          for (;;) {
            const events = await ledger.query(organizationId, { after, limit: PAGE });
            for (const event of events) eventBus.publish(event);
            if (events.length < PAGE) break;
            after = events[events.length - 1]!.cursor;
          }
        } catch (err) {
          console.error("live event read failed:", err);
          // Keep the position; a later notification or the retry reads it.
          const pending = waiting.get(organizationId);
          waiting.set(organizationId, Math.min(pending ?? Infinity, after + 1));
          schedule(1000);
          return;
        }
      }
    } finally {
      draining = false;
    }
  };

  const schedule = (ms: number) => {
    if (!timer) timer = setTimeout(() => void drain(), ms);
  };

  const { unlisten } = await sql.listen("dude_events", (payload) => {
    // Nobody watching: nothing to read. A browser that connects later
    // backfills from the ledger by cursor.
    if (eventBus.subscriberCount === 0) return;
    let notice: Notice;
    try {
      notice = JSON.parse(payload) as Notice;
    } catch {
      return;
    }
    const pending = waiting.get(notice.organizationId);
    if (pending === undefined || notice.cursor < pending) waiting.set(notice.organizationId, notice.cursor);
    schedule(SETTLE_MS);
  });

  return async () => {
    if (timer) clearTimeout(timer);
    await unlisten();
    await sql.end({ timeout: 2 });
  };
}

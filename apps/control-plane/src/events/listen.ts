/**
 * Feed the live event bus from the database, not from this process.
 *
 * Most events are written by the orchestrator, a separate process, so an
 * in-process publish after each append can no longer see them. Every insert
 * into `events` raises a NOTIFY (migration 014); this listens for it,
 * re-reads the row by cursor — NOTIFY payloads are capped at 8000 bytes and
 * an event's payload is not — and publishes it to local subscribers.
 *
 * One connection for the whole process. `Bun.sql` cannot LISTEN, so this is
 * the one place that uses the postgres.js driver.
 *
 * A missed notification is not a correctness problem: the stream's contract
 * is resume-by-cursor, and a client that reconnects gets everything after
 * its last cursor from the ledger.
 */

import postgres from "postgres";
import type { PersistedEvent } from "@dude/domain";
import { eventBus } from "./bus.ts";
import * as ledger from "./ledger.ts";

interface Notice {
  cursor: number;
  organizationId: string;
}

export async function listenForEvents(databaseUrl: string): Promise<() => Promise<void>> {
  const sql = postgres(databaseUrl, { max: 1, onnotice: () => {} });
  // Read in batches per organization, in cursor order, so a burst of
  // notifications costs one query per tenant rather than one per event.
  const waiting = new Map<string, number>();
  let draining = false;

  const drain = async () => {
    if (draining) return;
    draining = true;
    try {
      while (waiting.size > 0) {
        const batch = [...waiting.entries()];
        waiting.clear();
        for (const [organizationId, after] of batch) {
          const events: PersistedEvent[] = await ledger.query(organizationId, { after, limit: 1000 });
          for (const event of events) eventBus.publish(event);
        }
      }
    } catch (err) {
      console.error("live event read failed:", err);
    } finally {
      draining = false;
    }
  };

  const { unlisten } = await sql.listen("dude_events", (payload) => {
    let notice: Notice;
    try {
      notice = JSON.parse(payload) as Notice;
    } catch {
      return;
    }
    // Everything after the cursor before this one, so the event itself is
    // read; a lower pending position for the tenant wins.
    const after = notice.cursor - 1;
    const pending = waiting.get(notice.organizationId);
    if (pending === undefined || after < pending) waiting.set(notice.organizationId, after);
    void drain();
  });

  return async () => {
    await unlisten();
    await sql.end({ timeout: 2 });
  };
}

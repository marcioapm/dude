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
import { EventTypes, type PersistedEvent, type PersonRef } from "@dude/domain";
import { eventBus } from "./bus.ts";
import * as ledger from "./ledger.ts";
import { noteEvent } from "./visibility.ts";

interface Notice {
  cursor: number;
  organizationId: string;
  /** Presence (api/presence.ts): live only, not in the ledger. */
  seen?: PersonRef & { where: string | null };
  /** Someone has a session open (routes/sessions.ts): live only, its members only. */
  sessionOpen?: { sessionId: string; personId: string; open: boolean };
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
            for (const event of events) {
              noteEvent(event.eventType, event.sessionId);
              eventBus.publish(event);
            }
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
    if (notice.seen) {
      eventBus.publish(seenEvent(notice.organizationId, notice.seen));
      return;
    }
    if (notice.sessionOpen) {
      // Only to the session's members: its sessionId routes it through
      // their streams' membership check (visibility.ts).
      eventBus.publish(sessionOpenEvent(notice.organizationId, notice.sessionOpen));
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

/**
 * Someone was seen, as an event on the live stream. Cursor 0: it is not
 * in the ledger, so a stream sends it without an id and a reconnect does
 * not ask for it again (routes/events.ts).
 */
export function seenEvent(organizationId: string, person: PersonRef & { where: string | null }): PersistedEvent {
  return {
    cursor: 0,
    eventId: `seen_${person.id}_${Date.now()}`,
    eventType: EventTypes.PersonSeen,
    occurredAt: new Date().toISOString(),
    organizationId,
    projectId: null,
    taskId: null,
    runId: null,
    sessionId: null,
    workflowRunId: null,
    actor: { type: "person", id: person.id, name: person.name, photoUrl: person.photoUrl, online: true },
    source: "control-plane",
    correlationId: null,
    causationId: null,
    payload: { person },
  };
}

/** Someone has a session open, or no longer: cursor 0, as seenEvent. */
export function sessionOpenEvent(organizationId: string, o: { sessionId: string; personId: string; open: boolean }): PersistedEvent {
  return {
    cursor: 0,
    eventId: `open_${o.sessionId}_${o.personId}_${Date.now()}`,
    eventType: EventTypes.BrainstormOpen,
    occurredAt: new Date().toISOString(),
    organizationId,
    projectId: null,
    taskId: null,
    runId: null,
    sessionId: o.sessionId,
    workflowRunId: null,
    actor: { type: "person", id: o.personId },
    source: "control-plane",
    correlationId: null,
    causationId: null,
    payload: { personId: o.personId, open: o.open },
  };
}

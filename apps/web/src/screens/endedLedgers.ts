/**
 * The ledgers of a task's ended conductors, as Chat shows them above the
 * latest: read once per task, kept while the task's page is open, so
 * coming back to Chat, or the latest conductor being replaced, does not
 * read them again. One per task page (TaskScreen), never global.
 */

import { EventTypes, type PersistedEvent } from "@dude/domain";
import type { ApiClient } from "../api/client.ts";

/** Events per read: the API's most. */
export const ENDED_PAGE = 1000;

interface Entry {
  events: PersistedEvent[];
  /** The cursor read up to. */
  after: number;
  /** Nothing more will be written to it (finished). */
  final: boolean;
}

export class EndedLedgers {
  readonly #entries = new Map<string, Entry>();
  constructor(readonly client: ApiClient) {}

  /** What was read of the Run's ledger, while it is final; null when it must be read (again). */
  cached(runId: string): PersistedEvent[] | null {
    const entry = this.#entries.get(runId);
    return entry?.final ? entry.events : null;
  }

  /**
   * The Run's ledger: what was read before and is final, or that and the
   * pages after it. Null once `cancelled()` (its view unmounted): no page
   * is read, or kept, after that. Rejects when a read fails.
   */
  async read(runId: string, cancelled: () => boolean): Promise<PersistedEvent[] | null> {
    const entry = this.#entries.get(runId) ?? { events: [], after: 0, final: false };
    if (entry.final) return entry.events;
    const fresh: PersistedEvent[] = [];
    let after = entry.after;
    for (;;) {
      const page = await this.client.events({ runId, after, limit: ENDED_PAGE });
      if (cancelled()) return null;
      fresh.push(...page.events);
      if (page.events.length > 0) after = page.nextCursor;
      if (page.events.length < ENDED_PAGE) break;
    }
    const events = fresh.length === 0 ? entry.events : [...entry.events, ...fresh];
    this.#entries.set(runId, { events, after, final: finished(events) });
    return events;
  }
}

/** Events a person's message to a conductor is recorded with, naming its directive. */
const SENT = new Set<string>([EventTypes.ChatMessage, EventTypes.RunSteered, EventTypes.QuestionAnswered]);

/**
 * Whether an ended conductor's ledger can still grow. It has ended, and
 * every message sent to it is settled: delivered, or failed — the
 * hand-over of what it never read is written after its end, by the sweep,
 * for one that failed or was aborted.
 */
function finished(events: readonly PersistedEvent[]): boolean {
  if (!events.some((e) => e.eventType === EventTypes.RunCompleted || e.eventType === EventTypes.RunFailed ||
    e.eventType === EventTypes.RunAborted)) return false;
  const open = new Set<string>();
  for (const e of events) {
    const id = typeof e.payload.directiveId === "string" ? e.payload.directiveId : null;
    if (!id) continue;
    if (SENT.has(e.eventType)) open.add(id);
    else if (e.eventType === EventTypes.DirectiveDelivered || e.eventType === EventTypes.DirectiveFailed) open.delete(id);
  }
  return open.size === 0;
}

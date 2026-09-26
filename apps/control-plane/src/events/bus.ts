/**
 * In-process live event fanout — plan §106.
 *
 * The durable ledger is the source of truth; this is only transport for
 * already-committed events. A subscriber that misses events while
 * disconnected recovers by re-reading from its last cursor, so dropping a
 * live message is never a correctness problem.
 *
 * Fed by `listen.ts` from Postgres NOTIFY, not by the code that appends:
 * most events are written by the orchestrator, another process.
 */

import type { PersistedEvent } from "@dude/domain";

export interface EventFilter {
  organizationId: string;
  sessionId?: string | undefined;
  runId?: string | undefined;
  taskId?: string | undefined;
}

export type EventListener = (event: PersistedEvent) => void;

interface Subscription {
  filter: EventFilter;
  listener: EventListener;
}

function matches(filter: EventFilter, event: PersistedEvent): boolean {
  if (event.organizationId !== filter.organizationId) return false;
  if (filter.sessionId && event.sessionId !== filter.sessionId) return false;
  if (filter.runId && event.runId !== filter.runId) return false;
  if (filter.taskId && event.taskId !== filter.taskId) return false;
  return true;
}

export class EventBus {
  readonly #subscriptions = new Set<Subscription>();

  /** Returns an unsubscribe function. */
  subscribe(filter: EventFilter, listener: EventListener): () => void {
    const subscription: Subscription = { filter, listener };
    this.#subscriptions.add(subscription);
    return () => {
      this.#subscriptions.delete(subscription);
    };
  }

  publish(event: PersistedEvent): void {
    for (const subscription of this.#subscriptions) {
      if (!matches(subscription.filter, event)) continue;
      try {
        subscription.listener(event);
      } catch {
        // A broken subscriber must not stop delivery to the others, and must
        // never fail the append that triggered this publish.
      }
    }
  }

  get subscriberCount(): number {
    return this.#subscriptions.size;
  }
}

export const eventBus = new EventBus();

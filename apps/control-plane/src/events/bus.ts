/**
 * In-process live event fanout — plan §106.
 *
 * The durable ledger is the source of truth; this is only transport for
 * already-committed events. A subscriber that misses events while
 * disconnected recovers by re-reading from its last cursor, so dropping a
 * live message is never a correctness problem.
 *
 * Deliberately in-process for v1: a single control plane. When the control
 * plane scales horizontally this becomes a Postgres LISTEN/NOTIFY or a
 * broker-backed implementation behind the same interface.
 */

import type { PersistedEvent } from "@dude/domain";

export interface EventFilter {
  organizationId: string;
  sessionId?: string | undefined;
  runId?: string | undefined;
  workItemId?: string | undefined;
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
  if (filter.workItemId && event.workItemId !== filter.workItemId) return false;
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

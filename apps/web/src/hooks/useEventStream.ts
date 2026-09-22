/**
 * Live event subscription with exact resume.
 *
 * Every frame carries `id: <cursor>`, and the server honours the
 * `Last-Event-ID` header the browser sends back on its own reconnect. That is
 * what makes a dropped connection harmless: EventSource reconnects by itself
 * and the server replays exactly what was missed (plan §106), so nothing here
 * needs a backoff timer, a retained cursor, or a dedupe of its own.
 */

import { useEffect, useRef, useState } from "react";
import type { PersistedEvent } from "@dude/domain";
import type { ApiClient } from "../api/client.ts";

/**
 * A dropped connection is never an error state: EventSource reconnects and
 * the server resumes, so the honest report is "reconnecting".
 */
export type StreamStatus = "connecting" | "live" | "reconnecting";

export interface UseEventStreamOptions {
  client: ApiClient;
  runId?: string | undefined;
  sessionId?: string | undefined;
  workItemId?: string | undefined;
  /** Every event in the organization. For panels that watch everything. */
  all?: boolean | undefined;
  /** Cap on retained events, so a long session cannot grow without bound. */
  limit?: number;
}

export interface EventStreamState {
  events: PersistedEvent[];
  status: StreamStatus;
}

const DEFAULT_LIMIT = 2_000;

export function useEventStream(options: UseEventStreamOptions): EventStreamState {
  const { client, runId, sessionId, workItemId, all, limit = DEFAULT_LIMIT } = options;

  const [events, setEvents] = useState<PersistedEvent[]>([]);
  const [status, setStatus] = useState<StreamStatus>("connecting");

  useEffect(() => {
    if (!runId && !sessionId && !workItemId && !all) return;

    // A new scope is a new history: keeping the previous run's events would
    // show its transcript under this run's header.
    setEvents([]);
    setStatus("connecting");

    // An organization-wide stream is for noticing change, not for reading
    // history, so it starts from now rather than replaying the ledger.
    const source = new EventSource(client.streamUrl({ runId, sessionId, workItemId, live: all }));

    source.onopen = () => setStatus("live");

    /*
     * The server sends unnamed frames precisely so this handler fires. A
     * frame named after its event type would only reach a listener
     * registered for that exact name, and the ledger's vocabulary grows.
     */
    source.onmessage = (message) => {
      let event: PersistedEvent;
      try {
        event = JSON.parse(message.data) as PersistedEvent;
      } catch {
        // A malformed frame must not tear down a working stream.
        return;
      }

      setEvents((previous) => {
        const next = [...previous, event];
        return next.length > limit ? next.slice(next.length - limit) : next;
      });
    };

    // EventSource retries on its own and resumes from Last-Event-ID, so a
    // control plane restart heals without leaving a dead panel.
    source.onerror = () => setStatus("reconnecting");

    return () => source.close();
  }, [client, runId, sessionId, workItemId, all, limit]);

  return { events, status };
}

/**
 * Call `reload` when events arrive on a scope, coalesced.
 *
 * For views that re-read their data rather than folding events themselves:
 * the sidebar, the delivery view. A phase emits dozens of events in a burst,
 * and re-reading once per burst rather than once per event is what keeps a
 * busy organization from turning each open panel into a request storm.
 */
export function useReloadOnEvents(
  options: Omit<UseEventStreamOptions, "limit">,
  reload: () => void,
  debounceMs = 300,
): void {
  // Only whether something arrived matters, so the stream keeps almost
  // nothing.
  const { events } = useEventStream({ ...options, limit: 1 });
  const latest = useRef(reload);
  latest.current = reload;

  useEffect(() => {
    if (events.length === 0) return;
    const timer = setTimeout(() => latest.current(), debounceMs);
    return () => clearTimeout(timer);
  }, [events, debounceMs]);
}

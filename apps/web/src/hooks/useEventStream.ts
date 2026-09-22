/**
 * Live event subscription with exact resume.
 *
 * Every frame carries `id: <cursor>`, and the server honours the
 * `Last-Event-ID` header the browser sends back on its own reconnect. That is
 * what makes a dropped connection harmless: EventSource reconnects by itself
 * and the server replays exactly what was missed (plan §106), so nothing here
 * needs a backoff timer, a retained cursor, or a dedupe of its own.
 */

import { useEffect, useState } from "react";
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
  /** Cap on retained events, so a long session cannot grow without bound. */
  limit?: number;
}

export interface EventStreamState {
  events: PersistedEvent[];
  status: StreamStatus;
}

const DEFAULT_LIMIT = 2_000;

export function useEventStream(options: UseEventStreamOptions): EventStreamState {
  const { client, runId, sessionId, limit = DEFAULT_LIMIT } = options;

  const [events, setEvents] = useState<PersistedEvent[]>([]);
  const [status, setStatus] = useState<StreamStatus>("connecting");

  useEffect(() => {
    if (!runId && !sessionId) return;

    // A new scope is a new history: keeping the previous run's events would
    // show its transcript under this run's header.
    setEvents([]);
    setStatus("connecting");

    const source = new EventSource(client.streamUrl({ runId, sessionId }));

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
  }, [client, runId, sessionId, limit]);

  return { events, status };
}

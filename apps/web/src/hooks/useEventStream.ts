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
  taskId?: string | undefined;
  /** Every event in the organization. For panels that watch everything. */
  all?: boolean | undefined;
  /** Cap on retained events, so a long session cannot grow without bound. */
  limit?: number;
  /** Only what happens from now: no replay of the scope's history. */
  live?: boolean | undefined;
}

export interface EventStreamState {
  events: PersistedEvent[];
  status: StreamStatus;
}

const DEFAULT_LIMIT = 2_000;

export function useEventStream(options: UseEventStreamOptions): EventStreamState {
  const { client, runId, sessionId, taskId, all, limit = DEFAULT_LIMIT, live = all } = options;

  const [events, setEvents] = useState<PersistedEvent[]>([]);
  const [status, setStatus] = useState<StreamStatus>("connecting");

  useEffect(() => {
    if (!runId && !sessionId && !taskId && !all) return;

    // A new scope is a new history: keeping the previous run's events would
    // show its transcript under this run's header.
    setEvents([]);
    setStatus("connecting");

    // An organization-wide stream, or one only watched for change, starts
    // from now rather than replaying the ledger.
    const source = new EventSource(client.streamUrl({ runId, sessionId, taskId, live }));

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
  }, [client, runId, sessionId, taskId, all, limit, live]);

  return { events, status };
}

/**
 * Call `reload` when events arrive on a scope, at most once per `everyMs`.
 *
 * For views that re-read their data rather than folding events themselves:
 * the sidebar, the delivery view, the metrics. A phase emits dozens of
 * events in a burst, and re-reading once per interval rather than once per
 * event is what keeps a busy organization from turning each open panel into
 * a request storm. A throttle, not a debounce: an agent at work sends events
 * steadily, and a debounce would never fire until it stopped — exactly
 * when the view is changing.
 */
export function useReloadOnEvents(
  options: Omit<UseEventStreamOptions, "limit">,
  reload: () => void,
  everyMs = 300,
  /** Sees each event first; returning true means it needs no reload (presence). */
  onEvent?: (event: PersistedEvent) => boolean,
): void {
  // Only whether something arrived matters, so the stream keeps almost
  // nothing.
  const { events } = useEventStream({ ...options, limit: 1, live: true });
  const latest = useRef(reload);
  latest.current = reload;
  const handle = useRef(onEvent);
  handle.current = onEvent;
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  useEffect(() => {
    const event = events[0];
    if (!event || (handle.current?.(event) ?? false) || timer.current !== undefined) return;
    timer.current = setTimeout(() => {
      timer.current = undefined;
      latest.current();
    }, everyMs);
  }, [events, everyMs]);
  useEffect(() => () => clearTimeout(timer.current), []);
}

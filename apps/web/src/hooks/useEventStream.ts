/**
 * Live event subscription with exact resume.
 *
 * Every event carries a monotonic cursor, so a dropped connection is not a
 * lost event: reconnecting asks for everything after the last cursor seen and
 * then reattaches (plan §106). The UI never has to guess whether it missed
 * something.
 */

import { useEffect, useRef, useState } from "react";
import type { PersistedEvent } from "@dude/domain";
import type { ApiClient } from "../api/client.ts";

/**
 * A dropped connection is never an error state: the hook retries with backoff
 * and resumes from its cursor, so the honest report is "reconnecting".
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
const RECONNECT_BASE_MS = 500;
const RECONNECT_MAX_MS = 15_000;

export function useEventStream(options: UseEventStreamOptions): EventStreamState {
  const { client, runId, sessionId, limit = DEFAULT_LIMIT } = options;

  const [events, setEvents] = useState<PersistedEvent[]>([]);
  const [status, setStatus] = useState<StreamStatus>("connecting");

  // Held in a ref rather than state: a reconnect must read the latest cursor
  // without re-subscribing, and re-subscribing on every event would reset the
  // stream continuously.
  const cursorRef = useRef(0);
  const attemptRef = useRef(0);

  useEffect(() => {
    if (!runId && !sessionId) return;

    // A new scope is a new history: keep neither the events nor the cursor,
    // or switching runs would show the previous one's transcript and then
    // resume from a cursor that means nothing here.
    setEvents([]);
    setStatus("connecting");
    cursorRef.current = 0;
    attemptRef.current = 0;

    let source: EventSource | null = null;
    let retryTimer: ReturnType<typeof setTimeout> | undefined;
    let cancelled = false;

    const connect = () => {
      if (cancelled) return;

      const url = client.streamUrl({
        after: cursorRef.current || undefined,
        runId,
        sessionId,
      });
      source = new EventSource(url);

      source.onopen = () => {
        if (cancelled) return;
        attemptRef.current = 0;
        setStatus("live");
      };

      /*
       * The server sends unnamed frames precisely so this handler fires.
       * A frame named after its event type would only reach a listener
       * registered for that exact name, and the ledger's vocabulary grows.
       */
      source.onmessage = (message) => {
        if (cancelled) return;
        let event: PersistedEvent;
        try {
          event = JSON.parse(message.data) as PersistedEvent;
        } catch {
          // A malformed frame must not tear down a working stream.
          return;
        }

        // The server already drops anything at or below the resume cursor,
        // but a reconnect can race; dedupe here so the UI never shows a
        // duplicate turn.
        if (event.cursor <= cursorRef.current) return;
        cursorRef.current = event.cursor;

        setEvents((previous) => {
          const next = [...previous, event];
          return next.length > limit ? next.slice(next.length - limit) : next;
        });
      };

      source.onerror = () => {
        if (cancelled) return;
        source?.close();
        source = null;

        // Back off, but keep trying: a control plane restart should heal on
        // its own rather than leaving a dead panel the operator must reload.
        const delay = Math.min(RECONNECT_BASE_MS * 2 ** attemptRef.current, RECONNECT_MAX_MS);
        attemptRef.current += 1;
        setStatus("reconnecting");
        retryTimer = setTimeout(connect, delay);
      };
    };

    connect();

    return () => {
      cancelled = true;
      if (retryTimer) clearTimeout(retryTimer);
      source?.close();
    };
  }, [client, runId, sessionId, limit]);

  return { events, status };
}

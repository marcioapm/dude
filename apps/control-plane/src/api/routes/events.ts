/**
 * Event routes: history by cursor, and a live SSE stream.
 *
 * Reconnection contract (plan §106):
 *   GET /v1/events?after=<cursor>   -> everything missed, in order
 *   GET /v1/events/stream?after=... -> backfill from cursor, then live
 *
 * No activity is lost because a connection dropped.
 */

import type { PersistedEvent } from "@dude/domain";
import { eventBus } from "../../events/bus.ts";
import * as ledger from "../../events/ledger.ts";
import { intParam, json } from "../http.ts";
import type { RequestContext } from "../router.ts";
import type { Router } from "../router.ts";

function filtersFrom(url: URL) {
  return {
    sessionId: url.searchParams.get("sessionId") ?? undefined,
    runId: url.searchParams.get("runId") ?? undefined,
    workItemId: url.searchParams.get("workItemId") ?? undefined,
    projectId: url.searchParams.get("projectId") ?? undefined,
  };
}

async function listEvents({ url, principal }: RequestContext): Promise<Response> {
  const events = await ledger.query(principal.organizationId, {
    ...filtersFrom(url),
    after: intParam(url, "after", { min: 0 }),
    limit: intParam(url, "limit", { min: 1, max: 1000 }),
  });

  return json({
    events,
    // The cursor a client should pass as `after` on its next call.
    nextCursor: events.length ? events[events.length - 1]!.cursor : (intParam(url, "after", { min: 0 }) ?? 0),
  });
}

/**
 * Server-sent events: backfill then live.
 *
 * The gap between "read history" and "attach listener" is closed by
 * subscribing *first* and buffering, then discarding buffered events that the
 * backfill already covered. Without that ordering an event committed between
 * the two steps would be lost.
 */
function streamEvents({ url, principal, request }: RequestContext): Response {
  const filter = { organizationId: principal.organizationId, ...filtersFrom(url) };
  /*
   * `after` is explicit; `Last-Event-ID` is what the browser sends by itself.
   *
   * Every frame carries `id: <cursor>`, and the sole purpose of that field is
   * for EventSource to echo the last one back on its own reconnect. Honouring
   * it means the native reconnect resumes exactly, so a client needs no
   * backoff timer, no retained cursor and no dedupe of its own.
   */
  const resume = Number(request.headers.get("last-event-id"));
  const after =
    intParam(url, "after", { min: 0 }) ??
    (Number.isSafeInteger(resume) && resume > 0 ? resume : undefined);

  let unsubscribe: (() => void) | undefined;
  let heartbeat: ReturnType<typeof setInterval> | undefined;

  const stream = new ReadableStream<Uint8Array>({
    async start(controller) {
      const encoder = new TextEncoder();
      let closed = false;
      let highWater = after ?? 0;

      // Flush a comment immediately so the client's response headers arrive
      // before the first event. Without this a stream whose backfill is empty
      // stays silent, and the client cannot distinguish "connected, idle" from
      // "still connecting".
      controller.enqueue(encoder.encode(": open\n\n"));

      const send = (event: PersistedEvent) => {
        if (closed || event.cursor <= highWater) return;
        highWater = event.cursor;
        /*
         * Deliberately unnamed frames.
         *
         * Naming each frame after its event type means `EventSource.onmessage`
         * never fires — that handler only receives frames without a name — so
         * a client would have to `addEventListener` for every type in a
         * vocabulary that grows. The type is already in the payload.
         */
        controller.enqueue(
          encoder.encode(`id: ${event.cursor}\ndata: ${JSON.stringify(event)}\n\n`),
        );
      };

      // Buffer anything published while the backfill query is in flight.
      const pending: PersistedEvent[] = [];
      let backfilled = false;
      unsubscribe = eventBus.subscribe(filter, (event) => {
        if (backfilled) send(event);
        else pending.push(event);
      });

      try {
        const history = await ledger.query(principal.organizationId, {
          ...filtersFrom(url),
          after,
          limit: 1000,
        });
        for (const event of history) send(event);
      } catch (err) {
        console.error("event stream backfill failed:", err);
      }

      backfilled = true;
      // `send` drops anything at or below the high-water mark, so events that
      // appeared in both the buffer and the backfill are not delivered twice.
      for (const event of pending) send(event);
      pending.length = 0;

      // Keeps proxies from reaping an idle connection.
      heartbeat = setInterval(() => {
        if (!closed) controller.enqueue(encoder.encode(": heartbeat\n\n"));
      }, 15_000);

      request.signal.addEventListener("abort", () => {
        closed = true;
        unsubscribe?.();
        if (heartbeat) clearInterval(heartbeat);
        try {
          controller.close();
        } catch {
          // Already closed by the runtime.
        }
      });
    },
    cancel() {
      unsubscribe?.();
      if (heartbeat) clearInterval(heartbeat);
    },
  });

  return new Response(stream, {
    headers: {
      "content-type": "text/event-stream; charset=utf-8",
      "cache-control": "no-cache, no-transform",
      connection: "keep-alive",
      // Disables proxy buffering, which would otherwise defeat streaming.
      "x-accel-buffering": "no",
    },
  });
}

export function registerEventRoutes(router: Router): void {
  router.get("/v1/events", listEvents);
  // EventSource cannot set an Authorization header, so the live stream is the
  // one endpoint that accepts the key as a query parameter. It is read-only.
  router.get("/v1/events/stream", streamEvents, { allowKeyInQuery: true });
}

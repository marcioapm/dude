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
    taskId: url.searchParams.get("taskId") ?? undefined,
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
/** Events per backfill query. The whole history is still sent, in pages. */
const BACKFILL_PAGE = 1000;
/** The longest a session-authenticated stream stays open before it must authenticate again. */
export const SESSION_STREAM_MS = 10 * 60_000;

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
  let lifetime: ReturnType<typeof setTimeout> | undefined;

  const stream = new ReadableStream<Uint8Array>({
    async start(controller) {
      const encoder = new TextEncoder();
      let closed = false;
      let highWater = after ?? 0;
      /*
       * `live=1` skips the backfill: a client that only wants to know *that*
       * something changed — the shell refreshing its sidebar — has no use
       * for the organization's entire history, and replaying it on every
       * page load would be the most expensive thing the page does.
       */
      const liveOnly = url.searchParams.get("live") === "1";

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
        // Not in the ledger (presence, cursor 0): sent at once and without
        // an id, so a reconnect's Last-Event-ID stays the last ledger
        // event's. Only for streams watching what happens now.
        if (event.cursor === 0) {
          if (liveOnly && !closed) controller.enqueue(encoder.encode(`data: ${JSON.stringify(event)}\n\n`));
        } else if (backfilled) send(event);
        else pending.push(event);
      });

      try {
        /*
         * Paged, not capped. A single capped query delivered only the
         * *oldest* thousand events of a long history and then jumped to
         * live, silently dropping everything in between — a gap in the one
         * stream whose contract is that it has none.
         */
        let cursor = after;
        while (!liveOnly && !closed) {
          const page = await ledger.query(principal.organizationId, {
            ...filtersFrom(url),
            after: cursor,
            limit: BACKFILL_PAGE,
          });
          for (const event of page) send(event);
          if (page.length < BACKFILL_PAGE) break;
          cursor = page[page.length - 1]!.cursor;
        }
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

      /*
       * A session-authenticated stream ends by the time its token does, and
       * at the latest after SESSION_STREAM_MS, so EventSource's reconnect
       * (resuming by Last-Event-ID) authenticates again: someone removed or
       * signed out stops receiving within that bound. API keys are checked
       * per connection as before.
       */
      if (principal.credentialKind === "person") {
        const untilExpiry = principal.expiresAt !== undefined ? principal.expiresAt * 1000 - Date.now() : Infinity;
        lifetime = setTimeout(end, Math.max(0, Math.min(untilExpiry, SESSION_STREAM_MS)));
      }

      request.signal.addEventListener("abort", end);
      function end() {
        if (closed) return;
        closed = true;
        unsubscribe?.();
        if (heartbeat) clearInterval(heartbeat);
        if (lifetime) clearTimeout(lifetime);
        try {
          controller.close();
        } catch {
          // Already closed by the runtime.
        }
      }
    },
    cancel() {
      unsubscribe?.();
      if (heartbeat) clearInterval(heartbeat);
      if (lifetime) clearTimeout(lifetime);
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

/**
 * Integration tests for the event ledger and its SSE transport.
 *
 * These talk to a real PostgreSQL and a real HTTP server, because the
 * properties under test — cursor ordering, RLS isolation, and not losing an
 * event in the gap between backfill and live subscription — only exist in
 * the presence of a real database and a real connection.
 *
 * Requires DATABASE_URL (owner role) and TEST_APP_DATABASE_URL (app role).
 */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import type { PersistedEvent } from "@dude/domain";
import { closePool, setPool, withOrg } from "../src/db/client.ts";
import { createApiKey } from "../src/api/auth.ts";
import * as ledger from "../src/events/ledger.ts";
import { startServer } from "../src/index.ts";
import { listenForEvents } from "../src/events/listen.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const APP_URL = process.env.TEST_APP_DATABASE_URL ?? "postgres://dude_app:dude_app@localhost:5433/dude";

const ORG_A = `org_test_a_${Bun.randomUUIDv7("hex").slice(0, 8)}`;
const ORG_B = `org_test_b_${Bun.randomUUIDv7("hex").slice(0, 8)}`;

let owner: SQL;
/** This file's pool, so closing it cannot sever another file's. */
let app: SQL;
let server: ReturnType<typeof startServer>;
/** Live events reach the stream through NOTIFY, as in production. */
let stopListening: () => Promise<void>;
let baseUrl: string;
let keyA: string;

/** Minimal event input; the ledger fills in id and timestamp. */
function eventInput(organizationId: string, eventType: string, extra: Record<string, unknown> = {}) {
  return {
    eventType,
    organizationId,
    projectId: null,
    taskId: null,
    runId: null,
    sessionId: null,
    workflowRunId: null,
    actor: { type: "system" as const, id: "test" },
    source: "control-plane" as const,
    correlationId: null,
    causationId: null,
    payload: {},
    ...extra,
  };
}

beforeAll(async () => {
  owner = new SQL(OWNER_URL);
  for (const [id, slug] of [[ORG_A, ORG_A], [ORG_B, ORG_B]]) {
    await owner`INSERT INTO organizations (id, name, slug) VALUES (${id}, ${id}, ${slug})
                ON CONFLICT (id) DO NOTHING`;
  }

  app = new SQL(APP_URL);
  setPool(app);
  keyA = (await createApiKey({ organizationId: ORG_A, name: "test" })).key;

  server = startServer(0);
  stopListening = await listenForEvents(APP_URL);
  baseUrl = `http://localhost:${server.port}`;
});

afterAll(async () => {
  await stopListening?.();
  await server?.stop(true);
  await closePool(app);
  // Cascades through events, api_keys and the rest of the tenant tables.
  await owner`DELETE FROM organizations WHERE id IN (${ORG_A}, ${ORG_B})`;
  await owner.end();
});

describe("event ledger", () => {
  test("assigns strictly increasing cursors in append order", async () => {
    const first = await ledger.append(eventInput(ORG_A, "test.one"));
    const second = await ledger.append(eventInput(ORG_A, "test.two"));
    const third = await ledger.append(eventInput(ORG_A, "test.three"));

    expect(second.cursor).toBeGreaterThan(first.cursor);
    expect(third.cursor).toBeGreaterThan(second.cursor);
  });

  test("appendMany commits atomically and preserves order", async () => {
    const events = await ledger.appendMany(ORG_A, [
      eventInput(ORG_A, "batch.a"),
      eventInput(ORG_A, "batch.b"),
      eventInput(ORG_A, "batch.c"),
    ]);

    expect(events.map((e) => e.eventType)).toEqual(["batch.a", "batch.b", "batch.c"]);
    expect(events[1]!.cursor).toBeGreaterThan(events[0]!.cursor);
    expect(events[2]!.cursor).toBeGreaterThan(events[1]!.cursor);
  });

  test("rejects an event whose organization differs from its scope", async () => {
    // Guards against a caller passing another tenant's id into a scoped write.
    await expect(
      withOrg(ORG_A, (scope) => ledger.appendInScope(scope, eventInput(ORG_B, "cross.tenant"))),
    ).rejects.toThrow(/does not match scope/);
  });

  test("after-cursor returns only later events", async () => {
    const anchor = await ledger.append(eventInput(ORG_A, "anchor"));
    await ledger.append(eventInput(ORG_A, "after.one"));
    await ledger.append(eventInput(ORG_A, "after.two"));

    const events = await ledger.query(ORG_A, { after: anchor.cursor });
    expect(events.every((e) => e.cursor > anchor.cursor)).toBe(true);
    expect(events.map((e) => e.eventType)).toEqual(["after.one", "after.two"]);
  });

  test("filters by run", async () => {
    const runId = `run_${Bun.randomUUIDv7("hex").slice(0, 8)}`;
    await ledger.append(eventInput(ORG_A, "scoped.run", { runId }));
    await ledger.append(eventInput(ORG_A, "unscoped"));

    const events = await ledger.query(ORG_A, { runId });
    expect(events).toHaveLength(1);
    expect(events[0]!.eventType).toBe("scoped.run");
  });

  test("round-trips a structured payload", async () => {
    const payload = { nested: { count: 3 }, list: ["a", "b"], flag: true };
    const appended = await ledger.append(eventInput(ORG_A, "payload.test", { payload }));

    const [read] = await ledger.query(ORG_A, { after: appended.cursor - 1, limit: 1 });
    // Guards the jsonb binding: a double-encoded payload reads back as a string.
    expect(read!.payload).toEqual(payload);
  });

  test("does not leak events across organizations", async () => {
    await ledger.append(eventInput(ORG_B, "org-b.private"));

    const visibleToA = await ledger.query(ORG_A);
    expect(visibleToA.some((e) => e.eventType === "org-b.private")).toBe(false);

    const visibleToB = await ledger.query(ORG_B);
    expect(visibleToB.some((e) => e.eventType === "org-b.private")).toBe(true);
  });
});

describe("event API", () => {
  test("rejects unauthenticated requests", async () => {
    const res = await fetch(`${baseUrl}/v1/events`);
    expect(res.status).toBe(401);
  });

  test("rejects an unknown key without revealing why", async () => {
    const res = await fetch(`${baseUrl}/v1/events`, {
      headers: { authorization: "Bearer dude_sk_not-a-real-key" },
    });
    expect(res.status).toBe(401);
  });

  test("returns events with a resumable nextCursor", async () => {
    await ledger.append(eventInput(ORG_A, "api.listed"));

    const res = await fetch(`${baseUrl}/v1/events`, { headers: { authorization: `Bearer ${keyA}` } });
    expect(res.status).toBe(200);

    const body = (await res.json()) as { events: PersistedEvent[]; nextCursor: number };
    expect(body.events.length).toBeGreaterThan(0);
    expect(body.nextCursor).toBe(body.events[body.events.length - 1]!.cursor);
  });
});

describe("SSE stream", () => {
  /**
   * Read SSE frames until `count` events arrive or the timeout elapses.
   *
   * `onOpen` fires once the connection is established rather than after the
   * first frame: when the backfill is empty there is no frame to wait for, and
   * hanging on one would deadlock the test.
   */
  async function collect(
    url: string,
    count: number,
    onOpen?: () => Promise<void>,
    timeoutMs = 5000,
    headers: Record<string, string> = {},
  ): Promise<Array<{ cursor: number; type: string }>> {
    const ctl = new AbortController();
    const res = await fetch(url, {
      headers: { authorization: `Bearer ${keyA}`, ...headers },
      signal: ctl.signal,
    });
    const reader = res.body!.getReader();
    const decoder = new TextDecoder();
    const seen: Array<{ cursor: number; type: string }> = [];

    const deadline = setTimeout(() => ctl.abort(), timeoutMs);

    // Give the server's backfill query time to run before emitting live
    // events, so both paths are exercised rather than collapsing into one.
    const triggered = onOpen ? Bun.sleep(150).then(onOpen) : Promise.resolve();

    let buffer = "";
    try {
      while (seen.length < count) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });

        const frames = buffer.split("\n\n");
        buffer = frames.pop() ?? "";
        for (const frame of frames) {
          const cursor = frame.match(/^id: (\d+)$/m)?.[1];
          // Frames are deliberately unnamed so EventSource.onmessage fires;
          // the event type travels in the payload.
          const data = frame.match(/^data: (.+)$/m)?.[1];
          if (cursor && data) {
            seen.push({ cursor: Number(cursor), type: JSON.parse(data).eventType as string });
          }
        }
      }
    } catch {
      // Aborted by the deadline or by satisfying `count`.
    } finally {
      clearTimeout(deadline);
      await triggered.catch(() => {});
      ctl.abort();
    }
    return seen;
  }

  test("backfills from a cursor, then delivers live events", async () => {
    const anchor = await ledger.append(eventInput(ORG_A, "sse.anchor"));
    await ledger.append(eventInput(ORG_A, "sse.backfilled"));

    const seen = await collect(`${baseUrl}/v1/events/stream?after=${anchor.cursor}`, 2, async () => {
      await ledger.append(eventInput(ORG_A, "sse.live"));
    });

    const types = seen.map((s) => s.type);
    expect(types).toContain("sse.backfilled");
    expect(types).toContain("sse.live");
  });

  test("never delivers the same cursor twice", async () => {
    const anchor = await ledger.append(eventInput(ORG_A, "dedupe.anchor"));
    await ledger.append(eventInput(ORG_A, "dedupe.one"));

    const seen = await collect(`${baseUrl}/v1/events/stream?after=${anchor.cursor}`, 2, async () => {
      await ledger.append(eventInput(ORG_A, "dedupe.two"));
    });

    const cursors = seen.map((s) => s.cursor);
    expect(new Set(cursors).size).toBe(cursors.length);
  });

  /*
   * The browser resends the last `id:` it saw as `Last-Event-ID` when it
   * reconnects on its own. Honouring it is what lets the client drop its
   * backoff timer, its retained cursor and its dedupe.
   */
  test("resumes from Last-Event-ID when no explicit cursor is given", async () => {
    const anchor = await ledger.append(eventInput(ORG_A, "resume.anchor"));
    await ledger.append(eventInput(ORG_A, "resume.after"));

    const seen = await collect(`${baseUrl}/v1/events/stream`, 1, undefined, 5000, {
      "last-event-id": String(anchor.cursor),
    });

    expect(seen.every((s) => s.cursor > anchor.cursor)).toBe(true);
    expect(seen.map((s) => s.type)).toContain("resume.after");
  });

  test("an explicit cursor wins over Last-Event-ID", async () => {
    const first = await ledger.append(eventInput(ORG_A, "precedence.first"));
    const second = await ledger.append(eventInput(ORG_A, "precedence.second"));

    // A stale header must not replay what the caller said it already has.
    const seen = await collect(
      `${baseUrl}/v1/events/stream?after=${second.cursor}`,
      1,
      async () => {
        await ledger.append(eventInput(ORG_A, "precedence.live"));
      },
      5000,
      { "last-event-id": String(first.cursor) },
    );

    expect(seen.map((s) => s.type)).not.toContain("precedence.second");
    expect(seen.map((s) => s.type)).toContain("precedence.live");
  });

  test("delivers events in cursor order", async () => {
    const anchor = await ledger.append(eventInput(ORG_A, "order.anchor"));

    const seen = await collect(`${baseUrl}/v1/events/stream?after=${anchor.cursor}`, 3, async () => {
      await ledger.appendMany(ORG_A, [
        eventInput(ORG_A, "order.one"),
        eventInput(ORG_A, "order.two"),
        eventInput(ORG_A, "order.three"),
      ]);
    });

    const cursors = seen.map((s) => s.cursor);
    expect(cursors).toEqual([...cursors].sort((a, b) => a - b));
  });
});

/**
 * Presence counts a person using dude, and is cheap. A request counts only
 * with `x-dude-active: 1`, as the web app sends from a tab in use; the CLI,
 * scripts and the event stream never change who is online. A person's row
 * is touched at most once a minute.
 *
 * The router against the DATABASE_URL database (owner role; the app role
 * is dude_app there), in an organization of its own, with presence's NOTIFY
 * heard as a running backend hears it.
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { EventTypes, type PersistedEvent } from "@dude/domain";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import type { Router } from "../src/api/router.ts";
import { authenticate, createApiKey, personPrincipal } from "../src/api/auth.ts";
import { SESSION_WHERE, due } from "../src/api/presence.ts";
import { eventBus } from "../src/events/bus.ts";
import { listenForEvents } from "../src/events/listen.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const APP_URL = process.env.TEST_APP_DATABASE_URL ?? (() => {
  const url = new URL(OWNER_URL);
  url.username = "dude_app";
  url.password = "dude_app";
  return url.toString();
})();
const ORG = `org_presence_${Bun.randomUUIDv7("hex").slice(-10)}`;

let owner: SQL;
let app: SQL;
let router: Router;
let stopListening: () => Promise<void>;
let unsubscribe: () => void;
const heard: PersistedEvent[] = [];

beforeAll(async () => {
  owner = new SQL(OWNER_URL);
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${ORG}, ${ORG}, ${ORG})`;
  app = new SQL(APP_URL);
  setPool(app);
  // A request naming no credential is the person its x-test-person header names: a session, as Access signs one in.
  router = buildRouter("", async (credential, request) => {
    if (credential !== null) return authenticate(credential);
    const personId = request.headers.get("x-test-person");
    return personId ? personPrincipal(ORG, personId) : null;
  });
  unsubscribe = eventBus.subscribe({ organizationId: ORG }, (event) => {
    if (event.eventType === EventTypes.PersonSeen) heard.push(event);
  });
  stopListening = await listenForEvents(APP_URL);
});

afterAll(async () => {
  unsubscribe?.();
  await stopListening?.();
  await closePool(app);
  await owner`DELETE FROM organizations WHERE id = ${ORG}`;
  await owner.end();
});

/** A person no request has touched yet in this process, so the minute limit hides nothing. */
async function someone(name: string) {
  return createApiKey({ organizationId: ORG, name });
}

function call(path: string, headers: Record<string, string>): Promise<Response> {
  return router.handle(new Request(`http://dude.test${path}`, { headers }));
}

async function seen(personId: string) {
  const [row] = await owner`SELECT last_seen_at, last_seen_where FROM people WHERE id = ${personId}` as
    Array<{ last_seen_at: Date | null; last_seen_where: string | null }>;
  return row!;
}

/** The `person.seen` events heard for `personId` after `wait` ms. */
async function announced(personId: string, wait: number) {
  await Bun.sleep(wait);
  return heard.filter((e) => (e.payload["person"] as { id: string }).id === personId);
}

async function waitAnnounced(personId: string) {
  for (let i = 0; i < 50; i++) {
    const found = await announced(personId, 50);
    if (found.length > 0) return found;
  }
  return [];
}

test("a person is touched at most once a minute", () => {
  const seenAt = new Map<string, number>();
  expect(due(seenAt, "a", 0)).toBe(true);
  expect(due(seenAt, "a", 59_999)).toBe(false);
  expect(due(seenAt, "b", 30_000)).toBe(true);
  expect(due(seenAt, "a", 60_000)).toBe(true);
  expect(due(seenAt, "a", 90_000)).toBe(false);
});

test("a request from a tab in use says where its person is, and announces it", async () => {
  const ana = await someone("Ana");
  const res = await call("/v1/me", { authorization: `Bearer ${ana.key}`, "x-dude-active": "1", "x-dude-where": "TEXT-14" });
  expect(res.status).toBe(200);
  const row = await seen(ana.personId);
  expect(row.last_seen_at).not.toBeNull();
  expect(Date.now() - row.last_seen_at!.getTime()).toBeLessThan(60_000);
  expect(row.last_seen_where).toBe("TEXT-14");
  const events = await waitAnnounced(ana.personId);
  expect(events.length).toBe(1);
  expect(events[0]!.payload["person"]).toMatchObject({ id: ana.personId, online: true, where: "TEXT-14" });
  expect((await call("/v1/people", { authorization: `Bearer ${ana.key}` }).then((r) => r.json()) as {
    people: Array<{ id: string; online: boolean }> }).people.find((p) => p.id === ana.personId)?.online).toBe(true);
});

test("an API key's request without the header (the CLI, a script, MCP) changes nothing", async () => {
  const cli = await someone("Cli");
  for (const active of [undefined, "0", "true", "", "yes"]) {
    const headers: Record<string, string> = { authorization: `Bearer ${cli.key}`, "x-dude-where": "TEXT-9" };
    if (active !== undefined) headers["x-dude-active"] = active;
    expect([active, (await call("/v1/navigation", headers)).status]).toEqual([active, 200]);
  }
  expect(await seen(cli.personId)).toEqual({ last_seen_at: null, last_seen_where: null });
  expect(await announced(cli.personId, 400)).toEqual([]);
  // Not the minute limit: the same person, from a tab in use, is seen at once.
  await call("/v1/navigation", { authorization: `Bearer ${cli.key}`, "x-dude-active": "1" });
  expect((await seen(cli.personId)).last_seen_at).not.toBeNull();
});

test("a signed-in person's request counts by the header too", async () => {
  const sam = await someone("Sam");
  await call("/v1/me", { "x-test-person": sam.personId });
  expect((await seen(sam.personId)).last_seen_at).toBeNull();
  await call("/v1/me", { "x-test-person": sam.personId, "x-dude-active": "1", "x-dude-where": "Board" });
  expect(await seen(sam.personId)).toMatchObject({ last_seen_where: "Board" });
  expect((await seen(sam.personId)).last_seen_at).not.toBeNull();
});

test("opening the event stream, key in the query as a browser does, changes nothing", async () => {
  const tab = await someone("Tab");
  const res = await call(`/v1/events/stream?live=1&key=${encodeURIComponent(tab.key)}`, { "x-dude-where": "TEXT-3" });
  expect(res.status).toBe(200);
  const reader = res.body!.getReader();
  await Bun.sleep(200);
  await reader.cancel();
  expect(await seen(tab.personId)).toEqual({ last_seen_at: null, last_seen_where: null });
  expect(await announced(tab.personId, 300)).toEqual([]);
});

test("a brainstorm session's request, in use, says only that it is a session", async () => {
  const bea = await someone("Bea");
  // The orchestrator it forwards to is not there; the person is seen before the route answers.
  await call("/v1/brainstorms/ssn_secret", { authorization: `Bearer ${bea.key}`, "x-dude-active": "1", "x-dude-where": "Usage-based billing" });
  expect((await seen(bea.personId)).last_seen_where).toBe(SESSION_WHERE);
  const events = await waitAnnounced(bea.personId);
  expect((events[0]!.payload["person"] as { where: string }).where).toBe(SESSION_WHERE);
});

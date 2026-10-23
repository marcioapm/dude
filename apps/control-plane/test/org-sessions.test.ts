/**
 * Organisation sessions on the backend's side: the new session routes
 * forward as the person, a mention's fields reach the orchestrator, a
 * person removed from the organisation passes their sessions on (to its
 * admins when someone else can reach them, archived with them when nobody
 * can), and someone who is not one of an organisation session's people may
 * say they have it open, which marks their mentions in it seen.
 *
 * Against a database of its own, migrated as a release is, and a stand-in
 * orchestrator. Requires DATABASE_URL.
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { join } from "node:path";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import type { Router } from "../src/api/router.ts";
import { authenticate, createApiKey } from "../src/api/auth.ts";
import { Config, useConfig } from "../src/config.ts";
import { ownerLeftNotice } from "../src/api/routes/sessions.ts";
import { canSee, noteEvent } from "../src/events/visibility.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");
const NAME = `dude_org_sessions_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_os";

function databaseUrl(user: string): string {
  const url = new URL(OWNER_URL);
  if (user === "app") {
    url.username = "dude_app";
    url.password = "dude_app";
  }
  url.pathname = `/${NAME}`;
  return url.toString();
}

type Key = { key: string; id: string; personId: string };
let admin: SQL;
let owner: SQL;
let app: SQL;
let router: Router;
let orchestrator: ReturnType<typeof Bun.serve>;
let boss: Key, leaver: Key, eli: Key, gus: Key;
const forwarded: Array<{ method: string; path: string; body: unknown; person: string | null }> = [];

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const migrate = Bun.spawnSync(["bun", "run", "apps/control-plane/src/db/migrate.ts"], {
    cwd: ROOT, env: { ...process.env, DATABASE_URL: databaseUrl("owner") },
  });
  if (migrate.exitCode !== 0) throw new Error(`migrate: ${migrate.stderr.toString()}`);
  owner = new SQL(databaseUrl("owner"));
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${ORG}, 'Acme', 'acme')`;
  app = new SQL(databaseUrl("app"));
  setPool(app);
  boss = await createApiKey({ organizationId: ORG, name: "Boss" });
  leaver = await createApiKey({ organizationId: ORG, name: "Lea" });
  eli = await createApiKey({ organizationId: ORG, name: "Eli" });
  gus = await createApiKey({ organizationId: ORG, name: "Gus" });
  await owner`UPDATE people SET role = 'admin' WHERE id = ${boss.personId}`;

  orchestrator = Bun.serve({ port: 0, async fetch(request) {
    const url = new URL(request.url);
    forwarded.push({ method: request.method, path: url.pathname, body: await request.json().catch(() => null),
      person: request.headers.get("x-dude-person") });
    return Response.json({ ok: true });
  } });
  useConfig(Config.load({ env: { ...process.env, DUDE_ORCHESTRATOR_URL: `http://localhost:${orchestrator.port}`, DUDE_ORCHESTRATOR_TOKEN: "t" } }));
  router = buildRouter("", async (credential) => credential === null ? null : authenticate(credential));
});

afterAll(async () => {
  orchestrator?.stop(true);
  useConfig(null);
  await closePool(app);
  await owner?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

async function call(who: Key, method: string, path: string, body?: unknown): Promise<Response> {
  return router.handle(new Request(`http://dude.test${path}`, {
    method, headers: { authorization: `Bearer ${who.key}`, "content-type": "application/json" },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  }));
}

async function session(id: string, access: "private" | "organisation", ownerId: string, others: Array<[string, string]> = []) {
  await owner.begin(async (tx) => {
    await tx`INSERT INTO sessions (id, organization_id, title, access) VALUES (${id}, ${ORG}, ${id}, ${access})`;
    await tx`INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at)
      VALUES (${id}, ${ownerId}, ${ORG}, 'owner', now())`;
    for (const [person, role] of others) {
      await tx`INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at)
        VALUES (${id}, ${person}, ${ORG}, ${role}, now())`;
    }
  });
}

test("the open list, access and take-ownership forward as the person; a message's mentions and shareWith travel, a bad one never does", async () => {
  forwarded.length = 0;
  const ok = [
    ["GET", "/v1/brainstorms/open", undefined, "/internal/sessions/open"],
    ["PUT", "/v1/brainstorms/ssn_x/access", { access: "organisation" }, "/internal/sessions/ssn_x/access"],
    ["POST", "/v1/brainstorms/ssn_x/take-ownership", undefined, "/internal/sessions/ssn_x/take-ownership"],
    ["POST", "/v1/brainstorms/ssn_x/chat", { text: "@Gus look", mentions: [gus.personId], shareWith: [gus.personId] },
      "/internal/sessions/ssn_x/chat"],
  ] as const;
  for (const [method, path, body] of ok) expect((await call(eli, method, path, body)).status).toBe(200);
  expect(forwarded.map((f) => [f.method, f.path])).toEqual(ok.map(([m, , , to]) => [m, to]));
  expect(forwarded.every((f) => f.person === eli.personId)).toBe(true);
  expect(forwarded[1]!.body).toEqual({ access: "organisation" });
  expect(forwarded[3]!.body).toEqual({ text: "@Gus look", mentions: [gus.personId], shareWith: [gus.personId] });

  forwarded.length = 0;
  for (const body of [
    { text: "x", mentions: [""] },
    { text: "x", mentions: Array.from({ length: 21 }, (_, i) => `per_${i}`) },
    { text: "x", mentions: "per_a" },
  ]) expect((await call(eli, "POST", "/v1/brainstorms/ssn_x/chat", body)).status).toBe(400);
  expect((await call(eli, "PUT", "/v1/brainstorms/ssn_x/access", {})).status).toBe(400);
  expect((await call(eli, "PUT", "/v1/brainstorms/ssn_x/access", { access: "everyone" })).status).toBe(400);
  expect(forwarded).toEqual([]);
  // "private" is a known access: the orchestrator answers it (its 409, one way).
  expect((await call(eli, "PUT", "/v1/brainstorms/ssn_x/access", { access: "private" })).status).toBe(200);
  expect(forwarded.map((f) => f.body)).toEqual([{ access: "private" }]);
});

test("removing a person hands their reachable sessions to the admins and archives the ones nobody else is in", async () => {
  await session("ssn_lea_org", "organisation", leaver.personId);
  await session("ssn_lea_shared", "private", leaver.personId, [[eli.personId, "chat"]]);
  await session("ssn_lea_alone", "private", leaver.personId);
  await session("ssn_eli", "private", eli.personId, [[leaver.personId, "chat"]]);
  // Shared only with someone invited and not yet in: an unaccepted invitee
  // counts as shared, so it goes to the admins rather than being archived.
  await session("ssn_lea_invited", "private", leaver.personId);
  await owner`INSERT INTO session_people (session_id, person_id, organization_id, role) VALUES ('ssn_lea_invited', ${gus.personId}, ${ORG}, 'chat')`;
  // A handover Lea offered (to Gus, not in it yet) is void once she leaves.
  await owner`INSERT INTO session_people (session_id, person_id, organization_id, role, becomes_owner, handover_keep, invited_by)
    VALUES ('ssn_lea_org', ${gus.personId}, ${ORG}, 'chat', true, 'chat', ${leaver.personId})`;
  // The open session's agent is live and parked: it is told, and woken to read it.
  await owner`INSERT INTO runs (id, organization_id, session_id, attempt, role, kind, status, dude_pause)
    VALUES ('run_lea_org', ${ORG}, 'ssn_lea_org', 1, 'brainstorm', 'agent', 'paused', 'idle')`;

  expect((await call(boss, "DELETE", `/v1/people/${leaver.personId}`)).status).toBe(204);
  const rows = await owner`SELECT id, owned_by_admins AS "byAdmins" FROM sessions WHERE id LIKE 'ssn_lea%' OR id = 'ssn_eli' ORDER BY id`;
  expect([...rows]).toEqual([
    { id: "ssn_eli", byAdmins: false },
    { id: "ssn_lea_alone", byAdmins: false },
    { id: "ssn_lea_invited", byAdmins: true },
    { id: "ssn_lea_org", byAdmins: true },
    { id: "ssn_lea_shared", byAdmins: true },
  ]);
  const left = await owner`SELECT session_id AS id, payload->>'owner' AS owner, payload->>'ownerName' AS name, actor_id
    FROM events WHERE event_type = 'session.owner_left' ORDER BY session_id`;
  // Recorded as the admin's request is audited (their key here).
  expect([...left]).toEqual([
    { id: "ssn_lea_invited", owner: leaver.personId, name: "Lea", actor_id: boss.id },
    { id: "ssn_lea_org", owner: leaver.personId, name: "Lea", actor_id: boss.id },
    { id: "ssn_lea_shared", owner: leaver.personId, name: "Lea", actor_id: boss.id },
  ]);
  const [alone] = await owner`SELECT archived_at IS NOT NULL AS archived FROM session_people
    WHERE session_id = 'ssn_lea_alone' AND person_id = ${leaver.personId}`;
  expect(alone).toEqual({ archived: true });
  expect([...await owner`SELECT becomes_owner FROM session_people WHERE session_id = 'ssn_lea_org' AND person_id = ${gus.personId}`])
    .toEqual([{ becomes_owner: false }]);
  const told = [...await owner`SELECT d.text, r.control FROM directives d JOIN runs r ON r.id = d.run_id WHERE d.run_id = 'run_lea_org'`];
  expect(told).toEqual([{ text: ownerLeftNotice("Lea"), control: "resume" }]);
  expect([...await owner`SELECT payload->>'text' AS text FROM events WHERE event_type = 'session.told' AND session_id = 'ssn_lea_org'`])
    .toEqual([{ text: ownerLeftNotice("Lea") }]);
  // While its admins own it, an admin acts as owner of each; a member not in the private one gets nothing.
  const role = async (session: string, person: string) =>
    (await owner`SELECT session_role(${session}, ${person}) AS r`)[0].r as string | null;
  expect(await role("ssn_lea_org", boss.personId)).toBe("owner");
  expect(await role("ssn_lea_shared", boss.personId)).toBe("owner");
  expect(await role("ssn_lea_shared", gus.personId)).toBe(null);
  expect(await role("ssn_lea_alone", boss.personId)).toBe(null);
  // A session someone else owns is untouched by the leaver's departure.
  expect(await role("ssn_eli", boss.personId)).toBe(null);
});

test("someone not in an organisation session may say they have it open; opening marks their mentions in it seen", async () => {
  await session("ssn_open", "organisation", eli.personId);
  await session("ssn_closed", "private", eli.personId);
  await owner`INSERT INTO session_mentions (organization_id, session_id, person_id, by_person, text)
    VALUES (${ORG}, 'ssn_open', ${gus.personId}, ${eli.personId}, '@Gus')`;
  expect((await call(gus, "POST", "/v1/brainstorms/ssn_closed/open", { open: true })).status).toBe(404);
  expect((await call(gus, "POST", "/v1/brainstorms/ssn_open/open", { open: true })).status).toBe(200);
  const [viewer] = await owner`SELECT open_at IS NOT NULL AS open FROM session_viewers WHERE session_id = 'ssn_open' AND person_id = ${gus.personId}`;
  expect(viewer).toEqual({ open: true });
  expect([...await owner`SELECT seen_at IS NOT NULL AS seen FROM session_mentions WHERE person_id = ${gus.personId}`]).toEqual([{ seen: true }]);
  // Not one of its people: opening it adds no membership.
  expect([...await owner`SELECT 1 FROM session_people WHERE session_id = 'ssn_open' AND person_id = ${gus.personId}`]).toEqual([]);
  expect((await call(gus, "POST", "/v1/brainstorms/ssn_open/open", { open: false })).status).toBe(200);
  const [closed] = await owner`SELECT open_at IS NULL AS closed FROM session_viewers WHERE session_id = 'ssn_open' AND person_id = ${gus.personId}`;
  expect(closed).toEqual({ closed: true });
  // Closing it marks nothing seen.
  await owner`INSERT INTO session_mentions (organization_id, session_id, person_id, by_person, text)
    VALUES (${ORG}, 'ssn_open', ${gus.personId}, ${eli.personId}, '@Gus again')`;
  expect((await call(gus, "POST", "/v1/brainstorms/ssn_open/open", { open: false })).status).toBe(200);
  expect([...await owner`SELECT seen_at IS NULL AS unseen FROM session_mentions WHERE person_id = ${gus.personId} AND text = '@Gus again'`])
    .toEqual([{ unseen: true }]);

  // A member's heartbeat goes on their own row: their mention is seen, and no viewer row is made.
  await session("ssn_member", "private", eli.personId, [[gus.personId, "chat"]]);
  await owner`INSERT INTO session_mentions (organization_id, session_id, person_id, by_person, text)
    VALUES (${ORG}, 'ssn_member', ${gus.personId}, ${eli.personId}, '@Gus member')`;
  expect((await call(gus, "POST", "/v1/brainstorms/ssn_member/open", { open: true })).status).toBe(200);
  expect([...await owner`SELECT seen_at IS NOT NULL AS seen FROM session_mentions WHERE text = '@Gus member'`]).toEqual([{ seen: true }]);
  expect([...await owner`SELECT open_at IS NOT NULL AS open FROM session_people WHERE session_id = 'ssn_member' AND person_id = ${gus.personId}`])
    .toEqual([{ open: true }]);
  expect([...await owner`SELECT 1 FROM session_viewers WHERE session_id = 'ssn_member'`]).toEqual([]);
});

test("a live stream forgets what it knew of a session the moment it is opened, passed to the admins, or taken over", async () => {
  for (const [type, id] of [["session.access_changed", "ssn_vis_a"], ["session.owner_left", "ssn_vis_b"], ["session.owner_taken", "ssn_vis_c"]] as const) {
    await session(id, "private", eli.personId);
    expect(await canSee(ORG, gus.personId, id)).toBe(false);
    if (type === "session.access_changed") await owner`UPDATE sessions SET access = 'organisation' WHERE id = ${id}`;
    else await owner`UPDATE people SET role = 'admin' WHERE id = ${gus.personId}`;
    if (type !== "session.access_changed") await owner`UPDATE sessions SET owned_by_admins = true WHERE id = ${id}`;
    // Within the cache's few seconds, only the event makes the new answer visible.
    expect(await canSee(ORG, gus.personId, id)).toBe(false);
    noteEvent(type, id);
    expect({ type, ok: await canSee(ORG, gus.personId, id) }).toEqual({ type, ok: true });
    await owner`UPDATE people SET role = 'member' WHERE id = ${gus.personId}`;
  }
});

test("the ledger shows each person the events session_visible gives them, over private, organisation and admin-owned sessions", async () => {
  // A world of its own: an organisation with a member, a reader, an admin,
  // a removed person and an outsider, and every kind of session.
  const org = "org_lv";
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${org}, 'Acme', 'acme-lv'), ('org_lv_far', 'Far', 'far-lv')`;
  await owner`INSERT INTO people (id, organization_id, name, role) VALUES
    ('per_lv_own', ${org}, 'Own', 'member'), ('per_lv_rd', ${org}, 'Reader', 'member'), ('per_lv_mem', ${org}, 'Mem', 'member'),
    ('per_lv_adm', ${org}, 'Adm', 'admin'), ('per_lv_gone', ${org}, 'Gone', 'admin'), ('per_lv_far', 'org_lv_far', 'Far', 'member')`;
  await owner`UPDATE people SET removed_at = now() WHERE id = 'per_lv_gone'`;
  await owner`INSERT INTO sessions (id, organization_id, title, access, owned_by_admins) VALUES
    ('ssn_lv_priv', ${org}, 'P', 'private', false), ('ssn_lv_org', ${org}, 'O', 'organisation', false),
    ('ssn_lv_adm', ${org}, 'A', 'private', true), ('ssn_lv_far', 'org_lv_far', 'F', 'organisation', false)`;
  await owner`INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at) VALUES
    ('ssn_lv_priv', 'per_lv_own', ${org}, 'owner', now()), ('ssn_lv_priv', 'per_lv_rd', ${org}, 'read', now()),
    ('ssn_lv_priv', 'per_lv_gone', ${org}, 'chat', now()),
    ('ssn_lv_org', 'per_lv_own', ${org}, 'owner', now()),
    ('ssn_lv_adm', 'per_lv_gone', ${org}, 'owner', now()), ('ssn_lv_adm', 'per_lv_rd', ${org}, 'read', now()),
    ('ssn_lv_far', 'per_lv_far', 'org_lv_far', 'owner', now())`;
  await owner`INSERT INTO session_people (session_id, person_id, organization_id, role) VALUES ('ssn_lv_priv', 'per_lv_mem', ${org}, 'chat')`;
  let n = 0;
  for (const session of ["ssn_lv_priv", "ssn_lv_org", "ssn_lv_adm", "ssn_lv_nosuch", null]) {
    for (let i = 0; i < 3; i++) {
      await owner`INSERT INTO events (id, organization_id, event_type, session_id, actor_type, actor_id, source, payload)
        VALUES (${`evt_lv_${n++}`}, ${org}, 'chat.message', ${session}, 'human', 'x', 'test', '{}')`;
    }
  }
  const { query } = await import("../src/events/ledger.ts");
  for (const person of ["per_lv_own", "per_lv_rd", "per_lv_mem", "per_lv_adm", "per_lv_gone", "per_lv_far"]) {
    const want = [...await owner`SELECT id, session_id FROM events WHERE organization_id = ${org} AND session_visible(session_id, ${person})
      ORDER BY cursor`] as Array<{ id: string; session_id: string | null }>;
    for (const sessionId of [undefined, "ssn_lv_priv", "ssn_lv_org", "ssn_lv_adm"]) {
      const got = (await query(org, { viewer: person, limit: 1000, sessionId })).map((e) => e.eventId);
      const expected = want.filter((r) => !sessionId || r.session_id === sessionId).map((r) => r.id);
      expect({ person, sessionId, got }).toEqual({ person, sessionId, got: expected });
    }
  }
  // The world is mixed enough to tell: each person sees a different set.
  const counts = new Map<string, number>();
  for (const person of ["per_lv_own", "per_lv_rd", "per_lv_mem", "per_lv_adm", "per_lv_gone"]) {
    counts.set(person, (await query(org, { viewer: person, limit: 1000 })).length);
  }
  expect(Object.fromEntries(counts)).toEqual({ per_lv_own: 12, per_lv_rd: 15, per_lv_mem: 9, per_lv_adm: 12, per_lv_gone: 6 });
});

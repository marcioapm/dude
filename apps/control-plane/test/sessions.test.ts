/**
 * Brainstorm sessions through the public API: /v1/brainstorms forwards to
 * the orchestrator as the person asking, refusing a bad body itself; a
 * session's events — its agent's Run's included — reach only its accepted
 * members, through the history, the live stream, a Run's own routes and
 * the question list; whether a member has it open goes only to members;
 * and presence never carries a session's title to the organisation.
 *
 * Against a database of its own, migrated as a release is, and a stand-in
 * orchestrator. Requires DATABASE_URL: a role that can create databases.
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { join } from "node:path";
import { EventTypes, type PersistedEvent } from "@dude/domain";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter, startServer } from "../src/index.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";
import { Config, useConfig } from "../src/config.ts";
import { listenForEvents } from "../src/events/listen.ts";
import { SESSION_WHERE, whereFrom } from "../src/api/presence.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");
const NAME = `dude_sessions_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_ses";
const SESSION = "ssn_billing";
const RUN = "run_brainstorm";
const TITLE = "Usage-based billing";

function databaseUrl(user: string, name: string): string {
  const url = new URL(OWNER_URL);
  if (user === "app") {
    url.username = "dude_app";
    url.password = "dude_app";
  }
  url.pathname = `/${name}`;
  return url.toString();
}

type Key = { key: string; id: string; personId: string };
let admin: SQL;
let owner: SQL;
let app: SQL;
let router: Router;
let server: ReturnType<typeof startServer>;
let stopListening: () => Promise<void>;
let marcio: Key, ana: Key, outsider: Key, boss: Key;
let orchestrator: ReturnType<typeof Bun.serve>;
const forwarded: Array<{ method: string; path: string; body: unknown; person: string | null }> = [];

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const migrate = Bun.spawnSync(["bun", "run", "apps/control-plane/src/db/migrate.ts"], {
    cwd: ROOT, env: { ...process.env, DATABASE_URL: databaseUrl("owner", NAME) },
  });
  if (migrate.exitCode !== 0) throw new Error(`migrate: ${migrate.stderr.toString()}`);
  owner = new SQL(databaseUrl("owner", NAME));
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${ORG}, ${ORG}, ${ORG})`;
  app = new SQL(databaseUrl("app", NAME));
  setPool(app);
  marcio = await createApiKey({ organizationId: ORG, name: "Márcio" });
  ana = await createApiKey({ organizationId: ORG, name: "Ana" });
  outsider = await createApiKey({ organizationId: ORG, name: "Otto" });
  boss = await createApiKey({ organizationId: ORG, name: "Boss" });
  await owner`UPDATE people SET role = 'admin' WHERE id = ${boss.personId}`;
  // A session Márcio owns, Ana invited and not yet accepted, and its agent.
  await owner.begin(async (tx) => {
    await tx`INSERT INTO sessions (id, organization_id, title) VALUES (${SESSION}, ${ORG}, ${TITLE})`;
    await tx`INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at)
      VALUES (${SESSION}, ${marcio.personId}, ${ORG}, 'owner', now())`;
    await tx`INSERT INTO session_people (session_id, person_id, organization_id, role)
      VALUES (${SESSION}, ${ana.personId}, ${ORG}, 'chat')`;
  });
  await owner`INSERT INTO runs (id, organization_id, session_id, attempt, role, kind) VALUES (${RUN}, ${ORG}, ${SESSION}, 1, 'brainstorm', 'agent')`;
  await owner`INSERT INTO events (id, organization_id, event_type, run_id, actor_type, actor_id, source, payload)
    VALUES ('evt_secret', ${ORG}, 'agent.message', ${RUN}, 'agent', ${RUN}, 'runner', '{"text":"the meter dedupes per key"}')`;
  await owner`INSERT INTO questions (id, organization_id, run_id, prompt) VALUES ('qst_secret', ${ORG}, ${RUN}, 'Grow the window?')`;
  await owner`INSERT INTO directives (id, organization_id, run_id, text) VALUES ('dir_secret', ${ORG}, ${RUN}, 'Márcio: hello')`;

  orchestrator = Bun.serve({ port: 0, async fetch(request) {
    const url = new URL(request.url);
    forwarded.push({ method: request.method, path: url.pathname, body: await request.json().catch(() => null),
      person: request.headers.get("x-dude-person") });
    return Response.json({ ok: true });
  } });
  useConfig(Config.load({ env: { ...process.env, DUDE_ORCHESTRATOR_URL: `http://localhost:${orchestrator.port}`, DUDE_ORCHESTRATOR_TOKEN: "t" } }));
  router = buildRouter("");
  server = startServer(0, router);
  stopListening = await listenForEvents(databaseUrl("app", NAME));
});

afterAll(async () => {
  await stopListening?.();
  await server?.stop(true);
  orchestrator?.stop(true);
  useConfig(null);
  await closePool(app);
  await owner?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

async function call(who: Key, method: string, path: string, body?: unknown, headers: Record<string, string> = {}): Promise<Response> {
  return router.handle(new Request(`http://dude.test${path}`, {
    method, headers: { authorization: `Bearer ${who.key}`, "content-type": "application/json", ...headers },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  }));
}

test("every session route goes to the orchestrator as the person, and a bad body never does", async () => {
  forwarded.length = 0;
  const ok = [
    ["GET", "/v1/brainstorms", undefined, "/internal/sessions"],
    ["POST", "/v1/brainstorms", { title: " Ideas ", projects: [{ projectId: "prj_1", repositoryIds: ["repo_1"] }] }, "/internal/sessions"],
    ["GET", `/v1/brainstorms/${SESSION}`, undefined, `/internal/sessions/${SESSION}`],
    ["POST", `/v1/brainstorms/${SESSION}/chat`, { text: "hi" }, `/internal/sessions/${SESSION}/chat`],
    ["POST", `/v1/brainstorms/${SESSION}/link`, { projects: [] }, `/internal/sessions/${SESSION}/link`],
    ["POST", `/v1/brainstorms/${SESSION}/people`, { people: ["per_x"], role: "read" }, `/internal/sessions/${SESSION}/people`],
    ["POST", `/v1/brainstorms/${SESSION}/people/per_x/role`, { role: "chat" }, `/internal/sessions/${SESSION}/people/per_x/role`],
    ["POST", `/v1/brainstorms/${SESSION}/people/per_x/remove`, undefined, `/internal/sessions/${SESSION}/people/per_x/remove`],
    ["POST", `/v1/brainstorms/${SESSION}/owner`, { person: "per_x", keep: "leave" }, `/internal/sessions/${SESSION}/owner`],
    ["POST", `/v1/brainstorms/${SESSION}/accept`, undefined, `/internal/sessions/${SESSION}/accept`],
    ["POST", `/v1/brainstorms/${SESSION}/decline`, undefined, `/internal/sessions/${SESSION}/decline`],
    ["POST", `/v1/brainstorms/${SESSION}/file`, { proposalId: "prp_1", items: [0, 2] }, `/internal/sessions/${SESSION}/file`],
  ] as const;
  for (const [method, path, body] of ok) expect((await call(marcio, method, path, body)).status).toBe(200);
  expect(forwarded.map((f) => [f.method, f.path])).toEqual(ok.map(([m, , , to]) => [m, to]));
  expect(forwarded.every((f) => f.person === marcio.personId)).toBe(true);
  expect(forwarded[1]!.body).toEqual({ title: "Ideas", projects: [{ projectId: "prj_1", repositoryIds: ["repo_1"] }] });

  forwarded.length = 0;
  for (const [path, body] of [
    ["/v1/brainstorms", { title: "  " }],
    [`/v1/brainstorms/${SESSION}/chat`, { text: "" }],
    [`/v1/brainstorms/${SESSION}/people`, { people: ["per_x"], role: "owner" }],
    [`/v1/brainstorms/${SESSION}/people/per_x/role`, { role: "owner" }],
    [`/v1/brainstorms/${SESSION}/file`, { proposalId: "prp_1", items: [] }],
  ] as const) expect((await call(marcio, "POST", path, body)).status).toBe(400);
  expect(forwarded).toEqual([]);
});

test("a session's Run, its events, questions and directives are its accepted members' alone", async () => {
  for (const who of [ana, outsider, boss]) {
    expect((await call(who, "GET", `/v1/runs/${RUN}`)).status).toBe(404);
    expect((await call(who, "GET", `/v1/runs/${RUN}/diff`)).status).toBe(404);
    const directives = await (await call(who, "GET", `/v1/runs/${RUN}/directives`)).json() as { directives: unknown[] };
    expect(directives.directives).toEqual([]);
    const questions = await (await call(who, "GET", "/v1/questions")).json() as { questions: Array<{ id: string }> };
    expect(questions.questions.map((q) => q.id)).not.toContain("qst_secret");
    for (const path of ["/v1/events", `/v1/events?runId=${RUN}`, `/v1/events?sessionId=${SESSION}`]) {
      const events = await (await call(who, "GET", path)).json() as { events: PersistedEvent[] };
      expect(events.events.map((e) => e.eventId)).not.toContain("evt_secret");
    }
  }
  expect((await call(marcio, "GET", `/v1/runs/${RUN}`)).status).toBe(200);
  const mine = await (await call(marcio, "GET", `/v1/events?runId=${RUN}`)).json() as { events: PersistedEvent[] };
  expect(mine.events.map((e) => e.eventId)).toContain("evt_secret");
  const directives = await (await call(marcio, "GET", `/v1/runs/${RUN}/directives`)).json() as { directives: Array<{ id: string }> };
  expect(directives.directives.map((d) => d.id)).toEqual(["dir_secret"]);
});

/** The frames a live stream sends while `act` runs, as `who`. */
async function streamed(who: Key, act: () => Promise<void>, path = "/v1/events/stream?live=1"): Promise<PersistedEvent[]> {
  const ctl = new AbortController();
  const res = await fetch(`http://localhost:${server.port}${path}`, { headers: { authorization: `Bearer ${who.key}` }, signal: ctl.signal });
  const reader = res.body!.getReader();
  const decoder = new TextDecoder();
  const out: PersistedEvent[] = [];
  const reading = (async () => {
    let buffer = "";
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        const frames = buffer.split("\n\n");
        buffer = frames.pop() ?? "";
        for (const frame of frames) {
          const data = frame.match(/^data: (.+)$/m)?.[1];
          if (data) out.push(JSON.parse(data) as PersistedEvent);
        }
      }
    } catch { /* aborted */ }
  })();
  await Bun.sleep(150);
  await act();
  await Bun.sleep(900);
  ctl.abort();
  await reading;
  return out;
}

test("the live stream gives a session's events, and who has it open, to its members only", async () => {
  // Ana accepts: from now on she is a member.
  await owner`UPDATE session_people SET accepted_at = now() WHERE session_id = ${SESSION} AND person_id = ${ana.personId}`;
  const act = async () => {
    await owner`INSERT INTO events (id, organization_id, event_type, run_id, actor_type, actor_id, source, payload)
      VALUES (${`evt_live_${Date.now()}`}, ${ORG}, 'agent.message', ${RUN}, 'agent', ${RUN}, 'runner', '{"text":"live words"}')`;
    expect((await call(marcio, "POST", `/v1/brainstorms/${SESSION}/open`, { open: true })).status).toBe(200);
  };
  const [toAna, toOtto, toBoss] = await Promise.all([streamed(ana, act), streamed(outsider, async () => {}), streamed(boss, async () => {})]);
  expect(toAna.some((e) => e.eventType === "agent.message" && e.sessionId === SESSION)).toBe(true);
  expect(toAna.some((e) => e.eventType === EventTypes.BrainstormOpen && e.payload.personId === marcio.personId)).toBe(true);
  for (const frames of [toOtto, toBoss]) {
    expect(frames.some((e) => e.sessionId === SESSION)).toBe(false);
    expect(JSON.stringify(frames)).not.toContain("live words");
  }
  // Someone not in it cannot say they have it open.
  expect((await call(outsider, "POST", `/v1/brainstorms/${SESSION}/open`, { open: true })).status).toBe(404);
});

test("presence never carries a session's title: its where is a fixed word", async () => {
  expect(whereFrom(`${TITLE}`, `/v1/brainstorms/${SESSION}`)).toBe(SESSION_WHERE);
  expect(whereFrom(`A session · ${TITLE}`, "/v1/navigation")).toBe(SESSION_WHERE);
  expect(whereFrom("TEXT-14 · Implement", "/v1/navigation")).toBe("TEXT-14 · Implement");
  // Through a request (someone not seen yet this minute): the row
  // everyone's Online list reads says only that.
  const joao = await createApiKey({ organizationId: ORG, name: "João" });
  await owner`INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at)
    VALUES (${SESSION}, ${joao.personId}, ${ORG}, 'read', now())`;
  const res = await call(joao, "GET", `/v1/brainstorms/${SESSION}`, undefined, { "x-dude-where": TITLE });
  expect(res.status).toBe(200);
  const [row] = await owner`SELECT last_seen_where FROM people WHERE id = ${joao.personId}` as Array<{ last_seen_where: string }>;
  expect(row!.last_seen_where).toBe(SESSION_WHERE);
});

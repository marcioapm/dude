/**
 * Brainstorm sessions through the public API: /v1/brainstorms forwards to
 * the orchestrator as the person asking, refusing a bad body itself; a
 * session's events — its agent's Run's included — reach only its accepted
 * members, through the history, the live stream, a Run's own routes, its
 * artifacts and the question list; whether a member has it open goes only
 * to members; and presence never carries a session's title to the
 * organisation.
 *
 * Every refusal is checked for the three people who are not members: an
 * invitee who has not accepted (Ana), someone else in the organisation
 * (Otto) and an admin not in the session (Boss).
 *
 * Against a database of its own, migrated as a release is, and a stand-in
 * orchestrator. Requires DATABASE_URL: a role that can create databases.
 * Each test sets up the membership it relies on; none depends on another
 * having run.
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
const ARTIFACT = "art_secret";
const AGENT_SESSION = "ses_inner";
const TITLE = "Usage-based billing";
const BYTES = "the private plan, as a file";

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
/** Márcio owns the session, João reads it; Ana is invited and has not accepted; Otto and Boss (an admin) are not in it. */
let marcio: Key, joao: Key, ana: Key, outsider: Key, boss: Key;
let outsiders: Array<[string, Key]>;
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
  joao = await createApiKey({ organizationId: ORG, name: "João" });
  ana = await createApiKey({ organizationId: ORG, name: "Ana" });
  outsider = await createApiKey({ organizationId: ORG, name: "Otto" });
  boss = await createApiKey({ organizationId: ORG, name: "Boss" });
  await owner`UPDATE people SET role = 'admin' WHERE id = ${boss.personId}`;
  outsiders = [["a pending invitee", ana], ["someone else", outsider], ["an admin not in it", boss]];
  await owner.begin(async (tx) => {
    await tx`INSERT INTO sessions (id, organization_id, title) VALUES (${SESSION}, ${ORG}, ${TITLE})`;
    await tx`INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at)
      VALUES (${SESSION}, ${marcio.personId}, ${ORG}, 'owner', now()), (${SESSION}, ${joao.personId}, ${ORG}, 'read', now())`;
    await tx`INSERT INTO session_people (session_id, person_id, organization_id, role)
      VALUES (${SESSION}, ${ana.personId}, ${ORG}, 'chat')`;
  });
  await owner`INSERT INTO runs (id, organization_id, session_id, attempt, role, kind) VALUES (${RUN}, ${ORG}, ${SESSION}, 1, 'brainstorm', 'agent')`;
  await owner`INSERT INTO events (id, organization_id, event_type, run_id, actor_type, actor_id, source, payload)
    VALUES ('evt_secret', ${ORG}, 'agent.message', ${RUN}, 'agent', ${RUN}, 'runner', '{"text":"the meter dedupes per key"}')`;
  await owner`INSERT INTO questions (id, organization_id, run_id, prompt) VALUES ('qst_secret', ${ORG}, ${RUN}, 'Grow the window?')`;
  await owner`INSERT INTO directives (id, organization_id, run_id, text) VALUES ('dir_secret', ${ORG}, ${RUN}, 'Márcio: hello')`;
  await owner`INSERT INTO artifacts (id, organization_id, run_id, kind, name, content_type, size_bytes, storage_key, sha256, description)
    VALUES (${ARTIFACT}, ${ORG}, ${RUN}, 'file', 'plan.md', 'text/markdown', ${BYTES.length}, 'k/plan.md', 'x', 'The rollout plan')`;
  await owner`INSERT INTO agent_sessions (id, organization_id, run_id, role, harness, model)
    VALUES (${AGENT_SESSION}, ${ORG}, ${RUN}, 'brainstorm', 'opencode', 'm')`;

  orchestrator = Bun.serve({ port: 0, async fetch(request) {
    const url = new URL(request.url);
    forwarded.push({ method: request.method, path: url.pathname, body: await request.json().catch(() => null),
      person: request.headers.get("x-dude-person") });
    if (url.pathname.endsWith("/content")) return new Response(BYTES, { headers: { "content-type": "text/markdown" } });
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
    // Untitled: its agent names it.
    ["POST", "/v1/brainstorms", {}, "/internal/sessions"],
    ["GET", `/v1/brainstorms/${SESSION}`, undefined, `/internal/sessions/${SESSION}`],
    ["POST", `/v1/brainstorms/${SESSION}/title`, { title: " Billing v2 " }, `/internal/sessions/${SESSION}/title`],
    ["POST", `/v1/brainstorms/${SESSION}/chat`, { text: "hi" }, `/internal/sessions/${SESSION}/chat`],
    ["POST", `/v1/brainstorms/${SESSION}/link`, { projects: [] }, `/internal/sessions/${SESSION}/link`],
    ["POST", `/v1/brainstorms/${SESSION}/people`, { people: ["per_x"], role: "read" }, `/internal/sessions/${SESSION}/people`],
    ["POST", `/v1/brainstorms/${SESSION}/people/per_x/role`, { role: "chat" }, `/internal/sessions/${SESSION}/people/per_x/role`],
    ["POST", `/v1/brainstorms/${SESSION}/people/per_x/remove`, undefined, `/internal/sessions/${SESSION}/people/per_x/remove`],
    ["POST", `/v1/brainstorms/${SESSION}/owner`, { person: "per_x", keep: "leave" }, `/internal/sessions/${SESSION}/owner`],
    ["POST", `/v1/brainstorms/${SESSION}/accept`, undefined, `/internal/sessions/${SESSION}/accept`],
    ["POST", `/v1/brainstorms/${SESSION}/decline`, undefined, `/internal/sessions/${SESSION}/decline`],
    ["POST", `/v1/brainstorms/${SESSION}/file`, { proposalId: "prp_1", items: [0, 2] }, `/internal/sessions/${SESSION}/file`],
    ["POST", `/v1/brainstorms/${SESSION}/chat`, { text: "one sec", aside: true }, `/internal/sessions/${SESSION}/chat`],
    ["POST", `/v1/brainstorms/${SESSION}/questions/q_1/answer`, { answers: [{ choices: [0] }, { choices: [], text: "Later" }], note: "n" },
      `/internal/sessions/${SESSION}/questions/q_1/answer`],
  ] as const;
  for (const [method, path, body] of ok) expect((await call(marcio, method, path, body)).status).toBe(200);
  expect(forwarded.map((f) => [f.method, f.path])).toEqual(ok.map(([m, , , to]) => [m, to]));
  expect(forwarded.every((f) => f.person === marcio.personId)).toBe(true);
  expect(forwarded[1]!.body).toEqual({ title: "Ideas", projects: [{ projectId: "prj_1", repositoryIds: ["repo_1"] }] });
  expect(forwarded[2]!.body).toEqual({ projects: [] });
  expect(forwarded[4]!.body).toEqual({ title: "Billing v2" });
  expect(forwarded.at(-2)!.body).toEqual({ text: "one sec", aside: true });
  expect(forwarded.at(-1)!.body).toEqual({ answers: [{ choices: [0] }, { choices: [], text: "Later" }], note: "n" });

  forwarded.length = 0;
  for (const [path, body] of [
    ["/v1/brainstorms", { title: "x".repeat(201) }],
    [`/v1/brainstorms/${SESSION}/title`, { title: "  " }],
    [`/v1/brainstorms/${SESSION}/title`, {}],
    [`/v1/brainstorms/${SESSION}/chat`, { text: "" }],
    [`/v1/brainstorms/${SESSION}/people`, { people: ["per_x"], role: "owner" }],
    [`/v1/brainstorms/${SESSION}/people/per_x/role`, { role: "owner" }],
    [`/v1/brainstorms/${SESSION}/file`, { proposalId: "prp_1", items: [] }],
    [`/v1/brainstorms/${SESSION}/questions/q_1/answer`, { answers: Array.from({ length: 5 }, () => ({ choices: [0] })) }],
    [`/v1/brainstorms/${SESSION}/questions/q_1/answer`, { answers: [{ choices: [0] }], attachmentIds: ["att_1"] }],
  ] as const) expect((await call(marcio, "POST", path, body)).status).toBe(400);
  expect(forwarded).toEqual([]);
});

test("a session's Run, its events, questions, directives and agent sessions are its accepted members' alone", async () => {
  for (const [, who] of outsiders) {
    for (const path of [`/v1/runs/${RUN}`, `/v1/runs/${RUN}/diff`, `/v1/sessions/${AGENT_SESSION}`]) {
      const res = await call(who, "GET", path);
      expect([path, res.status]).toEqual([path, 404]);
      expect(await res.text()).not.toContain(SESSION);
    }
    const directives = await (await call(who, "GET", `/v1/runs/${RUN}/directives`)).json() as { directives: unknown[] };
    expect(directives.directives).toEqual([]);
    for (const path of ["/v1/questions", `/v1/questions?runId=${RUN}`]) {
      const questions = await (await call(who, "GET", path)).json() as { questions: Array<{ id: string }> };
      expect(questions.questions.map((q) => q.id)).not.toContain("qst_secret");
    }
    for (const path of ["/v1/events", `/v1/events?runId=${RUN}`]) {
      const events = await (await call(who, "GET", path)).json() as { events: PersistedEvent[] };
      expect(events.events.map((e) => e.eventId)).not.toContain("evt_secret");
    }
  }
  for (const member of [marcio, joao]) {
    expect((await call(member, "GET", `/v1/runs/${RUN}`)).status).toBe(200);
    expect((await call(member, "GET", `/v1/sessions/${AGENT_SESSION}`)).status).toBe(200);
    const mine = await (await call(member, "GET", `/v1/events?runId=${RUN}`)).json() as { events: PersistedEvent[] };
    expect(mine.events.map((e) => e.eventId)).toContain("evt_secret");
    const directives = await (await call(member, "GET", `/v1/runs/${RUN}/directives`)).json() as { directives: Array<{ id: string }> };
    expect(directives.directives.map((d) => d.id)).toEqual(["dir_secret"]);
  }
});

test("a session Run's artifact downloads for its members alone, and nobody else's request reaches the orchestrator", async () => {
  for (const member of [marcio, joao]) {
    forwarded.length = 0;
    const res = await call(member, "GET", `/v1/artifacts/${ARTIFACT}/content`);
    expect(res.status).toBe(200);
    expect(await res.text()).toBe(BYTES);
    // As the person: the orchestrator checks the session against them too.
    expect(forwarded.map((f) => [f.path, f.person])).toEqual([[`/internal/artifacts/${ARTIFACT}/content`, member.personId]]);
  }
  for (const [, who] of outsiders) {
    forwarded.length = 0;
    const res = await call(who, "GET", `/v1/artifacts/${ARTIFACT}/content`);
    expect(res.status).toBe(404);
    const body = await res.text();
    expect(body).not.toContain(BYTES);
    expect(body).not.toContain("plan.md");
    expect(forwarded).toEqual([]);
  }
});

test("a session's files are listed and zipped for its members alone; anyone else is told the session does not exist", async () => {
  for (const member of [marcio, joao]) {
    const res = await call(member, "GET", `/v1/artifacts?sessionId=${SESSION}`);
    expect(res.status).toBe(200);
    const { artifacts } = await res.json() as { artifacts: Array<{ id: string; name: string; sessionId: string; runId: string; role: string; description: string }> };
    expect(artifacts.map((a) => [a.id, a.name, a.sessionId, a.runId, a.role, a.description]))
      .toEqual([[ARTIFACT, "plan.md", SESSION, RUN, "brainstorm", "The rollout plan"]]);
    forwarded.length = 0;
    const zip = await call(member, "GET", `/v1/brainstorms/${SESSION}/artifacts.zip`);
    expect(zip.status).toBe(200);
    expect(zip.headers.get("content-disposition")).toBe('attachment; filename="session-files.zip"');
    const bytes = new Uint8Array(await zip.arrayBuffer());
    expect(new TextDecoder().decode(bytes)).toContain(BYTES);
    expect(forwarded.map((f) => [f.path, f.person])).toEqual([[`/internal/artifacts/${ARTIFACT}/content`, member.personId]]);
  }
  for (const [name, who] of outsiders) {
    forwarded.length = 0;
    for (const path of [`/v1/artifacts?sessionId=${SESSION}`, `/v1/brainstorms/${SESSION}/artifacts.zip`]) {
      const res = await call(who, "GET", path);
      const body = await res.text();
      expect([name, path, res.status]).toEqual([name, path, 404]);
      expect(body).not.toContain("plan.md");
      expect(body).not.toContain(BYTES);
    }
    expect(forwarded).toEqual([]);
  }
  // Both, or neither, is a mistake rather than a way around the check.
  expect((await call(outsider, "GET", `/v1/artifacts?sessionId=${SESSION}&taskId=wi_x`)).status).toBe(400);
  expect((await call(marcio, "GET", "/v1/artifacts")).status).toBe(400);
});

test("a name published hundreds of times leaves every other name listed, and its own count whole", async () => {
  const session = "ssn_republished";
  const run = "run_republished";
  await owner.begin(async (tx) => {
    await tx`INSERT INTO sessions (id, organization_id, title) VALUES (${session}, ${ORG}, 'Republished')`;
    await tx`INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at)
      VALUES (${session}, ${marcio.personId}, ${ORG}, 'owner', now())`;
    await tx`INSERT INTO runs (id, organization_id, session_id, attempt, role, kind) VALUES (${run}, ${ORG}, ${session}, 1, 'brainstorm', 'agent')`;
    await tx`INSERT INTO artifacts (id, organization_id, run_id, kind, name, content_type, size_bytes, storage_key, sha256, description, created_at)
      VALUES ('art_early', ${ORG}, ${run}, 'published', 'early.md', 'text/markdown', 1, 'k/early', 'x', 'Published first', now() - interval '1 day')`;
    await tx`INSERT INTO artifacts (id, organization_id, run_id, kind, name, content_type, size_bytes, storage_key, sha256, description, created_at)
      SELECT 'art_notes_' || lpad(i::text, 4, '0'), ${ORG}, ${run}, 'published', 'notes.md', 'text/markdown', 1, 'k/notes/' || i, 'x',
        'draft ' || i, now() - interval '1 hour' + i * interval '1 second'
      FROM generate_series(1, 600) i`;
  });
  const res = await call(marcio, "GET", `/v1/artifacts?sessionId=${session}`);
  expect(res.status).toBe(200);
  const { artifacts } = await res.json() as { artifacts: Array<{ name: string; description: string; version: number; versions: number }> };
  expect(artifacts.filter((a) => a.name === "early.md").map((a) => [a.version, a.versions, a.description]))
    .toEqual([[1, 1, "Published first"]]);
  const notes = artifacts.filter((a) => a.name === "notes.md");
  expect(notes.length).toBe(50);
  expect([notes[0]!.version, notes[0]!.versions, notes[0]!.description]).toEqual([600, 600, "draft 600"]);
  expect(notes.at(-1)!.version).toBe(551);
});

test("a session Run's controls, answers and servers go only to the orchestrator, which refuses them as not a task's Run", async () => {
  // These change what runs: the control plane forwards them as the person,
  // and the orchestrator's run routes find no task Run by that id (they
  // select session_id IS NULL), so a session's agent is never steered,
  // paused, resumed, aborted, answered or served through them.
  const routes: Array<[string, string, unknown, string]> = [
    ["POST", `/v1/runs/${RUN}/steer`, { text: "go" }, `/internal/runs/${RUN}/steer`],
    ["POST", `/v1/runs/${RUN}/pause`, {}, `/internal/runs/${RUN}/pause`],
    ["POST", `/v1/runs/${RUN}/resume`, {}, `/internal/runs/${RUN}/resume`],
    ["POST", `/v1/runs/${RUN}/abort`, {}, `/internal/runs/${RUN}/abort`],
    ["POST", "/v1/questions/qst_secret/answer", { text: "yes" }, "/internal/questions/qst_secret/answer"],
    ["GET", `/v1/runs/${RUN}/servers`, undefined, `/internal/runs/${RUN}/servers`],
    ["POST", `/v1/runs/${RUN}/servers/start-all`, undefined, `/internal/runs/${RUN}/servers/start-all`],
    ["POST", `/v1/runs/${RUN}/servers/stop-all`, undefined, `/internal/runs/${RUN}/servers/stop-all`],
    ["POST", `/v1/runs/${RUN}/servers/web/start`, undefined, `/internal/runs/${RUN}/servers/web/start`],
    ["POST", `/v1/runs/${RUN}/servers/web/stop`, undefined, `/internal/runs/${RUN}/servers/web/stop`],
    ["POST", `/v1/runs/${RUN}/servers/web/restart`, undefined, `/internal/runs/${RUN}/servers/web/restart`],
    ["DELETE", `/v1/runs/${RUN}/servers/web`, undefined, `/internal/runs/${RUN}/servers/web`],
    ["GET", `/v1/runs/${RUN}/servers/web/log`, undefined, `/internal/runs/${RUN}/servers/web/log`],
  ];
  for (const [, who] of outsiders) {
    forwarded.length = 0;
    for (const [method, path, body] of routes) await call(who, method, path, body);
    expect(forwarded.map((f) => [f.method, f.path])).toEqual(routes.map(([m, , , to]) => [m, to]));
    expect(forwarded.every((f) => f.person === who.personId)).toBe(true);
  }
  // A nested agent session on a session Run is never made from here: it needs a task's project.
  forwarded.length = 0;
  const made = await call(marcio, "POST", `/v1/runs/${RUN}/sessions`, { role: "implementer" });
  expect(made.status).toBe(404);
  const [row] = await owner`SELECT count(*)::int AS n FROM agent_sessions WHERE run_id = ${RUN}` as Array<{ n: number }>;
  expect(row!.n).toBe(1);
});

/**
 * A live stream as `who`: `act` runs once the stream is open, and the
 * frames are collected until `done` says enough arrived (or 5s pass).
 */
async function streamed(who: Key, act: () => Promise<void>, done: (frames: PersistedEvent[]) => boolean,
  path = "/v1/events/stream?live=1", out: PersistedEvent[] = []): Promise<PersistedEvent[]> {
  const ctl = new AbortController();
  const res = await fetch(`http://localhost:${server.port}${path}`, { headers: { authorization: `Bearer ${who.key}` }, signal: ctl.signal });
  const reader = res.body!.getReader();
  const decoder = new TextDecoder();
  let opened!: () => void;
  const open = new Promise<void>((resolve) => { opened = resolve; });
  const reading = (async () => {
    let buffer = "";
    try {
      for (;;) {
        const { done: end, value } = await reader.read();
        if (end) break;
        buffer += decoder.decode(value, { stream: true });
        if (buffer.includes(": open")) opened();
        const frames = buffer.split("\n\n");
        buffer = frames.pop() ?? "";
        for (const frame of frames) {
          const data = frame.match(/^data: (.+)$/m)?.[1];
          if (data) out.push(JSON.parse(data) as PersistedEvent);
        }
      }
    } catch { /* aborted */ }
  })();
  await open;
  await act();
  const deadline = Date.now() + 5_000;
  while (!done(out) && Date.now() < deadline) await Bun.sleep(20);
  ctl.abort();
  await reading;
  return out;
}

/** A ledger event on the session's Run, with its words; returns its id. */
async function said(words: string): Promise<string> {
  const id = `evt_live_${Bun.randomUUIDv7("hex").slice(-12)}`;
  await owner`INSERT INTO events (id, organization_id, event_type, run_id, actor_type, actor_id, source, payload)
    VALUES (${id}, ${ORG}, 'agent.message', ${RUN}, 'agent', ${RUN}, 'runner', ${JSON.stringify({ text: words })}::jsonb)`;
  return id;
}

/** A marker after the session's events: on no session, so every stream receives it, after them. */
async function marker(): Promise<string> {
  const id = `evt_mark_${Bun.randomUUIDv7("hex").slice(-12)}`;
  await owner`INSERT INTO events (id, organization_id, event_type, actor_type, actor_id, source, payload)
    VALUES (${id}, ${ORG}, 'settings.updated', 'system', 'dude', 'control-plane', '{}'::jsonb)`;
  return id;
}

test("the live stream gives a session's events, and who has it open, to its members only — not a pending invitee", async () => {
  let mark = "";
  const act = async () => {
    await said("live words");
    expect((await call(marcio, "POST", `/v1/brainstorms/${SESSION}/open`, { open: true })).status).toBe(200);
    mark = await marker();
  };
  const seen = (frames: PersistedEvent[]) => mark !== "" && frames.some((e) => e.eventId === mark);
  const [toJoao, ...toOthers] = await Promise.all([streamed(joao, act, seen), ...outsiders.map(([, who]) => streamed(who, async () => {}, seen))]);
  expect(toJoao.some((e) => e.eventType === "agent.message" && e.sessionId === SESSION)).toBe(true);
  expect(toJoao.some((e) => e.eventType === EventTypes.BrainstormOpen && e.payload.personId === marcio.personId)).toBe(true);
  for (const frames of toOthers) {
    // Each got the marker after the session's events: what it missed, it was not sent.
    expect(frames.some((e) => e.eventId === mark)).toBe(true);
    expect(frames.some((e) => e.sessionId === SESSION)).toBe(false);
    expect(JSON.stringify(frames)).not.toContain("live words");
  }
  // Nobody not in it can say they have it open.
  for (const [, who] of outsiders) expect((await call(who, "POST", `/v1/brainstorms/${SESSION}/open`, { open: true })).status).toBe(404);
});

test("someone removed stops receiving on a stream they already have open", async () => {
  const remi = await createApiKey({ organizationId: ORG, name: "Remi" });
  await owner`INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at)
    VALUES (${SESSION}, ${remi.personId}, ${ORG}, 'chat', now())`;
  const received: PersistedEvent[] = [];
  let before = "", after = "", mark = "";
  const frames = await streamed(remi, async () => {
    before = await said("said while in it");
    // Received as a member (so the answer is cached) before the removal.
    const deadline = Date.now() + 5_000;
    while (!received.some((e) => e.eventId === before) && Date.now() < deadline) await Bun.sleep(20);
    await owner.begin(async (tx) => {
      await tx`DELETE FROM session_people WHERE session_id = ${SESSION} AND person_id = ${remi.personId}`;
      await tx`INSERT INTO events (id, organization_id, event_type, session_id, actor_type, actor_id, source, payload)
        VALUES (${`evt_rm_${Date.now()}`}, ${ORG}, 'session.member_removed', ${SESSION}, 'human', ${marcio.personId}, 'orchestrator',
          ${JSON.stringify({ person: remi.personId })}::jsonb)`;
    });
    after = await said("said after they left");
    mark = await marker();
  }, (f) => mark !== "" && f.some((e) => e.eventId === mark), undefined, received);
  expect(frames.some((e) => e.eventId === before)).toBe(true);
  expect(frames.some((e) => e.eventId === mark)).toBe(true);
  expect(frames.some((e) => e.eventId === after)).toBe(false);
  expect(JSON.stringify(frames)).not.toContain("said after they left");
});

test("a session's rename, title and all, reaches its members alone: through the history and the live stream", async () => {
  const renamed = async () => {
    const id = `evt_ren_${Bun.randomUUIDv7("hex").slice(-12)}`;
    await owner`INSERT INTO events (id, organization_id, event_type, session_id, actor_type, actor_id, source, payload)
      VALUES (${id}, ${ORG}, ${EventTypes.BrainstormRenamed}, ${SESSION}, 'agent', ${RUN}, 'orchestrator',
        ${JSON.stringify({ title: "Zephyr acquisition", by: "agent" })}::jsonb)`;
    return id;
  };
  let mark = "", event = "";
  const seen = (frames: PersistedEvent[]) => mark !== "" && frames.some((e) => e.eventId === mark);
  const [toJoao, ...toOthers] = await Promise.all([
    streamed(joao, async () => { event = await renamed(); mark = await marker(); }, seen),
    ...outsiders.map(([, who]) => streamed(who, async () => {}, seen)),
  ]);
  expect(toJoao.some((e) => e.eventId === event)).toBe(true);
  for (const frames of toOthers) {
    expect(frames.some((e) => e.eventId === mark)).toBe(true);
    expect(JSON.stringify(frames)).not.toContain("Zephyr");
  }
  for (const [, who] of outsiders) {
    const history = await call(who, "GET", `/v1/events?sessionId=${SESSION}`);
    expect(history.status).toBe(404);
    expect(await history.text()).not.toContain("Zephyr");
  }
  const mine = await (await call(marcio, "GET", `/v1/events?sessionId=${SESSION}`)).json() as { events: PersistedEvent[] };
  expect(mine.events.map((e) => e.eventId)).toContain(event);
});

test("a session's Events: every Run's events and its own, in order, for its members; anyone else gets 404 on the history and the stream", async () => {
  const second = "run_brainstorm_2";
  await owner`UPDATE runs SET status = 'completed' WHERE id = ${RUN}`;
  await owner`INSERT INTO runs (id, organization_id, session_id, attempt, role, kind) VALUES (${second}, ${ORG}, ${SESSION}, 1, 'brainstorm', 'agent')`;
  const ids = [`evt_l1_${Date.now()}`, `evt_l2_${Date.now()}`, `evt_l3_${Date.now()}`];
  await owner`INSERT INTO events (id, organization_id, event_type, run_id, actor_type, actor_id, source, payload)
    VALUES (${ids[0]!}, ${ORG}, 'agent.message', ${RUN}, 'agent', ${RUN}, 'runner', '{"text":"first Run"}'::jsonb)`;
  await owner`INSERT INTO events (id, organization_id, event_type, session_id, actor_type, actor_id, source, payload)
    VALUES (${ids[1]!}, ${ORG}, 'session.linked', ${SESSION}, 'human', ${marcio.personId}, 'orchestrator', '{"projects":[]}'::jsonb)`;
  await owner`INSERT INTO events (id, organization_id, event_type, run_id, actor_type, actor_id, source, payload)
    VALUES (${ids[2]!}, ${ORG}, 'agent.message', ${second}, 'agent', ${second}, 'runner', '{"text":"second Run"}'::jsonb)`;
  try {
    for (const member of [marcio, joao]) {
      const res = await call(member, "GET", `/v1/events?sessionId=${SESSION}&limit=1000`);
      expect(res.status).toBe(200);
      const { events } = await res.json() as { events: PersistedEvent[] };
      expect(events.filter((e) => ids.includes(e.eventId)).map((e) => e.eventId)).toEqual(ids);
      expect(events.some((e) => e.eventId === "evt_secret")).toBe(true);
      expect(events.every((e) => e.sessionId === SESSION)).toBe(true);
    }
    for (const [name, who] of outsiders) {
      for (const path of [`/v1/events?sessionId=${SESSION}`, `/v1/events/stream?sessionId=${SESSION}`]) {
        const res = await call(who, "GET", path);
        const body = await res.text();
        expect([name, path, res.status]).toEqual([name, path, 404]);
        expect(body).not.toContain("Run");
      }
    }
  } finally {
    await owner`DELETE FROM runs WHERE id = ${second}`;
    await owner`UPDATE runs SET status = 'pending' WHERE id = ${RUN}`;
  }
});

test("presence never carries a session's title: its where is a fixed word", async () => {
  expect(whereFrom(`${TITLE}`, `/v1/brainstorms/${SESSION}`)).toBe(SESSION_WHERE);
  expect(whereFrom(`A session · ${TITLE}`, "/v1/navigation")).toBe(SESSION_WHERE);
  expect(whereFrom("TEXT-14 · Implement", "/v1/navigation")).toBe("TEXT-14 · Implement");
  // Through a request by someone not seen yet this minute (its own person,
  // so no earlier test has touched them): the row everyone's Online list
  // reads says only that.
  const seen = await createApiKey({ organizationId: ORG, name: "Sam" });
  await owner`INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at)
    VALUES (${SESSION}, ${seen.personId}, ${ORG}, 'read', now())`;
  const res = await call(seen, "GET", `/v1/brainstorms/${SESSION}`, undefined, { "x-dude-where": TITLE });
  expect(res.status).toBe(200);
  const [row] = await owner`SELECT last_seen_where FROM people WHERE id = ${seen.personId}` as Array<{ last_seen_where: string }>;
  expect(row!.last_seen_where).toBe(SESSION_WHERE);
});

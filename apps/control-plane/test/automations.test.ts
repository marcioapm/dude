/**
 * Automations through the public API: who may change, read and transfer
 * them, the list's filters, "Also running here", and an owner leaving.
 *
 * Against a database of its own, migrated as a release is.
 *
 * Requires DATABASE_URL: a role that can create databases (the owner).
 */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { join } from "node:path";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import { Config, useConfig } from "../src/config.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");
const NAME = `dude_automations_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_aut";
const OTHER = "org_aut_other";
const WEB = "prj_aut_web";
const API = "prj_aut_api";

function databaseUrl(user: string, name: string): string {
  const url = new URL(OWNER_URL);
  if (user === "app") {
    url.username = "dude_app";
    url.password = "dude_app";
  }
  url.pathname = `/${name}`;
  return url.toString();
}

let admin: SQL;
let owner: SQL;
let app: SQL;
let router: Router;
let orchestratorServer: ReturnType<typeof Bun.serve>;
const asked: Array<{ method: string; path: string; person: string | null; body: string }> = [];
const keys: Record<string, string> = {};
const people: Record<string, string> = {};

type Json = any; // eslint-disable-line @typescript-eslint/no-explicit-any
const body = async (res: Response): Promise<Json> => res.json();

function call(who: string, method: string, path: string, payload?: unknown) {
  return router.handle(
    new Request(`http://dude.test${path}`, {
      method,
      headers: { authorization: `Bearer ${keys[who]}`, "content-type": "application/json" },
      ...(payload === undefined ? {} : { body: JSON.stringify(payload) }),
    }),
  );
}

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const migrate = Bun.spawnSync(["bun", "run", "apps/control-plane/src/db/migrate.ts"], {
    cwd: ROOT,
    env: { ...process.env, DATABASE_URL: databaseUrl("owner", NAME) },
  });
  if (migrate.exitCode !== 0) throw new Error(`migrate: ${migrate.stderr.toString()}`);
  owner = new SQL(databaseUrl("owner", NAME));
  for (const id of [ORG, OTHER]) await owner`INSERT INTO organizations (id, name, slug) VALUES (${id}, ${id}, ${id})`;
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES
    (${WEB}, ${ORG}, 'web', 'web', 'WEB'), (${API}, ${ORG}, 'api', 'api', 'API')`;
  await owner`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES
    ('repo_w', ${ORG}, ${WEB}, 'web', 'https://github.com/acme/web.git'),
    ('repo_s', ${ORG}, ${API}, 'svc-billing', 'git@github.com:acme/svc-billing.git'),
    ('repo_d', ${ORG}, ${API}, 'docs', 'https://github.com/acme/docs')`;

  app = new SQL(databaseUrl("app", NAME));
  setPool(app);
  orchestratorServer = Bun.serve({
    port: 0,
    async fetch(req) {
      const url = new URL(req.url);
      asked.push({ method: req.method, path: url.pathname, person: req.headers.get("x-dude-person"), body: await req.text() });
      if (url.pathname.endsWith("/start-fresh")) return new Response(null, { status: 204 });
      if (url.pathname.endsWith("/evaluate")) return Response.json({ matches: false, filter: "author", because: "it is by dependabot[bot], who is excluded" });
      if (url.pathname.endsWith("/subscriptions")) return Response.json({ events: ["pull_request", "push"], source: "app", settingsUrl: null });
      return Response.json({ runId: "arn_new" }, { status: 202 });
    },
  });
  // The organization's first person is its admin; the next ones members.
  for (const [who, org] of [["ana", ORG], ["bo", ORG], ["cy", ORG], ["di", OTHER]] as const) {
    const k = await createApiKey({ organizationId: org, name: who });
    keys[who] = k.key;
    people[who] = k.personId;
  }
  useConfig(Config.load({ env: { ...process.env,
    DUDE_ORCHESTRATOR_URL: `http://localhost:${orchestratorServer.port}`, DUDE_ORCHESTRATOR_TOKEN: "svc" } }));
  router = buildRouter("");
});

afterAll(async () => {
  useConfig(null);
  await orchestratorServer?.stop(true);
  await closePool(app);
  await owner?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

async function create(who: string, over: Record<string, unknown> = {}) {
  const res = await call(who, "POST", "/v1/automations", { name: "PR triage", kind: "agent", trigger: "github", ...over });
  return { status: res.status, body: await body(res) };
}

describe("creating", () => {
  test("an admin creates an organisation automation: off, owned by them, with defaults", async () => {
    const { status, body: a } = await create("ana");
    expect(status).toBe(201);
    expect(a).toMatchObject({ projectId: null, kind: "agent", enabled: false, owner: { id: people.ana, name: "ana" },
      ownerLeftAt: null, canEdit: true, canTransfer: true, sessionMode: "ephemeral", concurrency: "queue",
      trigger: { kind: "github", event: "pull_request" }, agent: { own: { prompt: "" } } });
  });

  test("a member cannot create one, in the organisation or a project", async () => {
    expect((await create("bo")).status).toBe(403);
    expect((await create("bo", { projectId: WEB })).status).toBe(403);
  });

  test("a project automation names a project of this organisation", async () => {
    expect((await create("ana", { projectId: "prj_nope" })).status).toBe(404);
    const { status, body: a } = await create("ana", { name: "Changelog builder", kind: "server", trigger: "schedule", projectId: WEB });
    expect(status).toBe(201);
    // A server answers events, whatever the dialog said.
    expect(a.trigger.kind).toBe("github");
    expect(a.projectName).toBe("web");
  });

  test("a webhook automation gets a signing secret, shown only by its hint", async () => {
    const { body: a } = await create("ana", { name: "Release notes", kind: "webhook", trigger: "github" });
    expect(a.webhookSecretHint).toHaveLength(4);
    expect(JSON.stringify(a)).not.toContain("whsec_");
  });
});

describe("changing", () => {
  test("org admins change an org automation; members read it and get 403 changing it", async () => {
    const { body: a } = await create("ana", { name: "Nightly audit", trigger: "schedule" });
    const read = await call("bo", "GET", `/v1/automations/${a.id}`);
    expect(read.status).toBe(200);
    expect(await body(read)).toMatchObject({ canEdit: false, canTransfer: false });
    const settings = { name: "Nightly audit", kind: "agent", trigger: { kind: "schedule", cron: "0 3 * * *", tz: "Europe/Lisbon" },
      agent: { own: { prompt: "Audit dependencies of {{repository}}." } }, repoFilter: { mode: "only", repos: ["acme/svc-*"] } };
    expect((await call("bo", "PUT", `/v1/automations/${a.id}`, settings)).status).toBe(403);
    expect((await call("bo", "PUT", `/v1/automations/${a.id}/enabled`, { enabled: true })).status).toBe(403);
    expect((await call("bo", "DELETE", `/v1/automations/${a.id}`)).status).toBe(403);
    const saved = await call("ana", "PUT", `/v1/automations/${a.id}`, settings);
    expect(saved.status).toBe(200);
    expect(await body(saved)).toMatchObject({ repoFilter: { mode: "only", repos: ["acme/svc-*"] }, trigger: { tz: "Europe/Lisbon" } });
    const on = await call("ana", "PUT", `/v1/automations/${a.id}/enabled`, { enabled: true });
    expect((await body(on)).enabled).toBe(true);
  });

  test("a project automation refuses a repository filter and an expiry past 30 days", async () => {
    const { body: a } = await create("ana", { name: "Lint", kind: "command", trigger: "schedule", projectId: WEB });
    const base = { name: "Lint", kind: "command", trigger: { kind: "schedule", cron: "0 * * * *", tz: "UTC" }, command: "bun run lint" };
    const filtered = await call("ana", "PUT", `/v1/automations/${a.id}`, { ...base, repoFilter: { mode: "all", repos: [] } });
    expect(filtered.status).toBe(400);
    expect(JSON.stringify(await body(filtered))).toContain("only an organisation automation filters them");
    const long = await call("ana", "PUT", `/v1/automations/${a.id}`, { ...base, sessionMode: "resumable", expiryDays: 31 });
    expect(long.status).toBe(400);
    // A command runs to its end: there is no session of it to resume.
    const resumable = await call("ana", "PUT", `/v1/automations/${a.id}`, { ...base, sessionMode: "resumable", expiryDays: 30 });
    expect(resumable.status).toBe(400);
    expect(JSON.stringify(await body(resumable))).toContain("a command runs to its end; it keeps no session to resume");
    const ok = await call("ana", "PUT", `/v1/automations/${a.id}`, { ...base, repos: ["repo_w"] });
    expect(ok.status).toBe(200);
    const otherProjects = await call("ana", "PUT", `/v1/automations/${a.id}`, { ...base, repos: ["repo_s"] });
    expect(otherProjects.status).toBe(400);
  });

  test("the repositories a project automation checks out are stored and read back as given", async () => {
    const { body: a } = await create("ana", { name: "Checkout", kind: "command", trigger: "schedule", projectId: WEB });
    const base = { name: "Checkout", kind: "command", trigger: { kind: "schedule", cron: "0 * * * *", tz: "UTC" }, command: "true" };
    for (const [repos, type] of [["all", "string"], ["none", "string"], [["repo_w"], "array"]] as const) {
      expect((await call("ana", "PUT", `/v1/automations/${a.id}`, { ...base, repos })).status).toBe(200);
      expect((await body(await call("ana", "GET", `/v1/automations/${a.id}`))).repos).toEqual(repos);
      const [stored] = await owner`SELECT jsonb_typeof(repos) AS type, repos FROM automations WHERE id = ${a.id}`;
      expect(stored).toEqual({ type, repos });
    }
    // The lists below count the project's automations.
    expect((await call("ana", "DELETE", `/v1/automations/${a.id}`)).status).toBeLessThan(300);
  });

  test("the Reviewer preset is refused on anything but a pull_request event (Q6)", async () => {
    const { body: a } = await create("ana", { name: "Review", preset: "reviewer" });
    const reviewer = { name: "Review", kind: "agent", agent: { preset: { role: "reviewer", overrides: {} }, brief: "" } };
    const issues = await call("ana", "PUT", `/v1/automations/${a.id}`, { ...reviewer, trigger: { kind: "github", event: "issues", actions: [], filters: {} } });
    expect(issues.status).toBe(400);
    const ok = await call("ana", "PUT", `/v1/automations/${a.id}`, { ...reviewer, trigger: { kind: "github", event: "pull_request_review", actions: [], filters: {} } });
    expect(ok.status).toBe(200);
  });

  test("another organisation sees none of them", async () => {
    const { body: a } = await create("ana", { name: "Private" });
    expect((await call("di", "GET", `/v1/automations/${a.id}`)).status).toBe(404);
    expect((await body(await call("di", "GET", "/v1/automations"))).automations).toEqual([]);
  });
});

describe("the lists", () => {
  test("filter by scope, project, trigger, kind, role, enabled, owner and name", async () => {
    const names = async (q: string) =>
      ((await body(await call("ana", "GET", `/v1/automations${q}`))).automations as Array<{ name: string }>).map((a) => a.name).sort();
    const all = await names("");
    expect(all.length).toBeGreaterThan(4);
    expect(await names("?scope=project")).toEqual(["Changelog builder", "Lint"]);
    expect(await names(`?project=${WEB}`)).toEqual(["Changelog builder", "Lint"]);
    expect(await names("?trigger=schedule")).toEqual(["Lint", "Nightly audit"]);
    expect(await names("?kind=server")).toEqual(["Changelog builder"]);
    expect(await names("?kind=webhook")).toEqual(["Release notes"]);
    expect(await names("?enabled=true")).toEqual(["Nightly audit"]);
    expect(await names("?q=night")).toEqual(["Nightly audit"]);
    expect(await names("?owner=mine")).toEqual(all);

    const { body: tester } = await create("ana", { name: "Nightly QA pass", preset: "qa_browser", trigger: "schedule", projectId: WEB });
    expect(tester.agent).toEqual({ preset: { role: "qa_browser", overrides: {} }, brief: "" });
    expect(await names("?kind=agent:qa_browser")).toEqual(["Nightly QA pass"]);
    expect(await names("?kind=agent:own")).not.toContain("Nightly QA pass");
    const bo = await call("bo", "GET", "/v1/automations?owner=mine");
    expect((await body(bo)).automations).toEqual([]);
    expect((await call("ana", "GET", "/v1/automations?kind=robot")).status).toBe(400);
  });

  test("a project lists its own and, read-only, the organisation's whose filter reaches its repositories", async () => {
    const { body: everywhere } = await create("ana", { name: "Everywhere" });
    const { body: svc } = await create("ana", { name: "Services only" });
    await call("ana", "PUT", `/v1/automations/${svc.id}`, { name: "Services only", kind: "agent",
      trigger: { kind: "github", event: "push" }, agent: { own: {} }, repoFilter: { mode: "only", repos: ["acme/svc-*"] } });
    const { body: notDocs } = await create("ana", { name: "Not docs" });
    await call("ana", "PUT", `/v1/automations/${notDocs.id}`, { name: "Not docs", kind: "agent",
      trigger: { kind: "github", event: "push" }, agent: { own: {} }, repoFilter: { mode: "except", repos: ["acme/docs", "acme/svc-*"] } });

    const web = await body(await call("bo", "GET", `/v1/projects/${WEB}/automations`));
    expect(web.automations.map((a: { name: string }) => a.name).sort()).toEqual(["Changelog builder", "Lint", "Nightly QA pass"]);
    const webHere = web.alsoRunningHere.map((a: { name: string }) => a.name);
    expect(webHere).toContain(everywhere.name);
    expect(webHere).toContain("Not docs");
    expect(webHere).not.toContain("Services only");
    expect(web.alsoRunningHere.every((a: { canEdit: boolean }) => !a.canEdit)).toBe(true);

    const apiHere = (await body(await call("bo", "GET", `/v1/projects/${API}/automations`))).alsoRunningHere.map((a: { name: string }) => a.name);
    expect(apiHere).toContain("Services only");
    expect(apiHere).not.toContain("Not docs");

    // A repository added later that a glob names is covered at once.
    await owner`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES
      ('repo_w2', ${ORG}, ${WEB}, 'svc-web', 'https://github.com/acme/svc-web')`;
    const later = (await body(await call("bo", "GET", `/v1/projects/${WEB}/automations`))).alsoRunningHere.map((a: { name: string }) => a.name);
    expect(later).toContain("Services only");
  });

  test("the repository filter's suggestions are every project's repositories, by project", async () => {
    const { groups } = await body(await call("bo", "GET", "/v1/automations/repositories"));
    expect(groups).toEqual([
      { projectId: API, project: "api", repos: ["acme/docs", "acme/svc-billing"] },
      { projectId: WEB, project: "web", repos: ["acme/svc-web", "acme/web"] },
    ]);
  });
});

describe("the owner", () => {
  test("admins and the owner transfer it, to a current member of this organisation", async () => {
    const { body: a } = await create("ana", { name: "Handed on" });
    expect((await call("bo", "PUT", `/v1/automations/${a.id}/owner`, { personId: people.bo })).status).toBe(403);
    expect((await call("ana", "PUT", `/v1/automations/${a.id}/owner`, { personId: people.di })).status).toBe(400);
    const toBo = await call("ana", "PUT", `/v1/automations/${a.id}/owner`, { personId: people.bo });
    expect(toBo.status).toBe(200);
    expect((await body(toBo)).owner.id).toBe(people.bo);
    // Bo owns it now and may hand it on, though he cannot edit it (Q20).
    const asBo = await body(await call("bo", "GET", `/v1/automations/${a.id}`));
    expect(asBo).toMatchObject({ canEdit: false, canTransfer: true });
    expect((await call("cy", "PUT", `/v1/automations/${a.id}/owner`, { personId: people.cy })).status).toBe(403);
    expect((await call("bo", "PUT", `/v1/automations/${a.id}/owner`, { personId: people.cy })).status).toBe(200);
    const [event] = await owner`SELECT payload FROM events WHERE event_type = 'automation.changed'
      AND payload->>'automationId' = ${a.id} AND payload->'changed' ? 'owner' ORDER BY cursor DESC LIMIT 1`;
    expect(event.payload.changed.owner).toEqual({ from: people.bo, to: people.cy });
  });

  test("an owner who leaves hands every automation of theirs to the admins together; it stays on", async () => {
    const { body: a } = await create("ana", { name: "Weekly cost report", trigger: "schedule" });
    await call("ana", "PUT", `/v1/automations/${a.id}/enabled`, { enabled: true });
    await call("ana", "PUT", `/v1/automations/${a.id}/owner`, { personId: people.cy });
    // A project automation of theirs is held the same way (Q18).
    const { body: p } = await create("ana", { name: "Project lint", kind: "command", trigger: "schedule", projectId: WEB });
    await call("ana", "PUT", `/v1/automations/${p.id}/owner`, { personId: people.cy });
    expect((await call("ana", "DELETE", `/v1/people/${people.cy}`)).status).toBe(204);
    const project = await body(await call("ana", "GET", `/v1/automations/${p.id}`));
    expect(project.owner).toBeNull();
    expect(project.ownerLeftAt).not.toBeNull();
    const after = await body(await call("ana", "GET", `/v1/automations/${a.id}`));
    expect(after.owner).toBeNull();
    expect(after.ownerLeftAt).not.toBeNull();
    expect(after.enabled).toBe(true);
    // The admins hand it to someone, which ends "held".
    const given = await body(await call("ana", "PUT", `/v1/automations/${a.id}/owner`, { personId: people.bo }));
    expect(given).toMatchObject({ owner: { id: people.bo }, ownerLeftAt: null });
  });
});

describe("runs", () => {
  test("list newest first, filter by status, keep skipped and queued reasons, and show the kept session", async () => {
    const { body: a } = await create("ana", { name: "Run lister" });
    await owner`INSERT INTO automation_runs (id, organization_id, automation_id, status, reason, source, payloads, session, cost_usd, created_at) VALUES
      ('arn_a', ${ORG}, ${a.id}, 'succeeded', NULL, 'event', '[{"deliveryId":"d1","event":"pull_request","action":"opened","body":{}}]', 'new', 0.42, now() - interval '3 minutes'),
      ('arn_b', ${ORG}, ${a.id}, 'skipped', 'a run was still going (Concurrency: skip)', 'event', '[{"deliveryId":"d2","event":"pull_request","action":"synchronize","body":{}}]', NULL, NULL, now() - interval '2 minutes'),
      ('arn_c', ${ORG}, ${a.id}, 'queued', 'waiting for the run before it', 'run_now', '[]', NULL, NULL, now() - interval '1 minute')`;
    await owner`INSERT INTO automation_sessions (automation_id, organization_id, lux_run_id, created_at, last_run_at)
      VALUES (${a.id}, ${ORG}, 'lrun_9', now() - interval '1 day', now() - interval '3 minutes')`;
    await owner`UPDATE automations SET session_mode = 'resumable', expiry_days = 7 WHERE id = ${a.id}`;

    const all = await body(await call("bo", "GET", `/v1/automations/${a.id}/runs`));
    expect(all.runs.map((r: { id: string }) => r.id)).toEqual(["arn_c", "arn_b", "arn_a"]);
    expect(all.runs[1]).toMatchObject({ status: "skipped", reason: "a run was still going (Concurrency: skip)", event: "pull_request", action: "synchronize" });
    expect(all.runs[2]).toMatchObject({ session: "new", costUsd: 0.42, payloadCount: 1 });
    expect(all.runs[2].luxRunId).toBeUndefined();
    expect(all.session).toMatchObject({ resumes: 0 });
    expect(Date.parse(all.session.expiresAt) - Date.parse(all.session.lastRunAt)).toBe(7 * 86_400_000);

    const skipped = await body(await call("bo", "GET", `/v1/automations/${a.id}/runs?status=skipped`));
    expect(skipped.runs.map((r: { id: string }) => r.id)).toEqual(["arn_b"]);
    expect((await call("bo", "GET", `/v1/automations/${a.id}/runs?status=weird`)).status).toBe(400);
  });

  test("a run's detail carries its payloads, log tail and a Server's requests", async () => {
    const { body: a } = await create("ana", { name: "Changelog server", kind: "server" });
    await owner`INSERT INTO automation_runs (id, organization_id, automation_id, status, source, payloads, log_tail,
        container_started_at, container_stopped_at, stop_reason)
      VALUES ('arn_s', ${ORG}, ${a.id}, 'succeeded', 'event', '[{"deliveryId":"d9","event":"pull_request","action":"opened","body":{"number":7}}]',
        'listening on 8080', now() - interval '1 minute', now(), 'idle')`;
    await owner`INSERT INTO automation_run_requests (id, organization_id, run_id, delivery_id, try, http_status, ms, at) VALUES
      ('arq_1', ${ORG}, 'arn_s', 'd9', 1, 500, 12, now() - interval '50 seconds'), ('arq_2', ${ORG}, 'arn_s', 'd9', 2, 202, 9, now() - interval '49 seconds')`;
    const run = await body(await call("bo", "GET", `/v1/automations/${a.id}/runs/arn_s`));
    expect(run).toMatchObject({ stopReason: "idle", logTail: "listening on 8080", payloadsPurged: false,
      payloads: [{ deliveryId: "d9", event: "pull_request", body: { number: 7 } }] });
    expect(run.requests.map((q: { try: number; httpStatus: number }) => [q.try, q.httpStatus])).toEqual([[1, 500], [2, 202]]);
    expect((await call("bo", "GET", `/v1/automations/${a.id}/runs/arn_nope`)).status).toBe(404);
  });

  test("a run's detail lists its agent's tool calls: a post with its channel and link, a refused post with its words, never a post's text", async () => {
    const { body: a } = await create("ana", { name: "Weekly PR digest" });
    await owner`INSERT INTO automation_runs (id, organization_id, automation_id, status, source) VALUES ('arn_d', ${ORG}, ${a.id}, 'succeeded', 'schedule')`;
    const ev = (id: string, type: string, at: string, payload: Record<string, unknown>) =>
      owner`INSERT INTO events (id, organization_id, event_type, actor_type, actor_id, source, occurred_at, run_id, payload)
        VALUES (${id}, ${ORG}, ${type}, 'agent', 'arn_d', 'orchestrator', ${at}::timestamptz, 'arn_d', ${{ ...payload, automationRunId: "arn_d" }})`;
    await ev("evt_d1", "agent.tool.dude", "2026-10-23T15:00:05Z", { tool: "github_comment", arguments: { issue: 12, body: "Looks good" }, result: {} });
    await ev("evt_d2", "agent.tool.attempted", "2026-10-23T15:03:12Z", { tool: "post_to_chat", arguments: { channel: "#engineering", text: "*Digest*" } });
    await ev("evt_d3", "slack.message.posted", "2026-10-23T15:03:12.640Z", { channel: "#engineering", channelId: "C1", ts: "1.000100", permalink: "https://acme.slack.com/archives/C1/p1" });
    await ev("evt_d4", "agent.tool.dude", "2026-10-23T15:03:12.640Z", { tool: "post_to_chat", arguments: { channel: "#engineering", text: "*Digest*" },
      result: { withheld: true }, attempted: true });
    // A post Slack refused spent its attempt first: timed from it.
    await ev("evt_d4a", "agent.tool.attempted", "2026-10-23T15:03:59.700Z", { tool: "post_to_chat", arguments: { channel: "#random", text: "x" } });
    await ev("evt_d5", "slack.message.refused", "2026-10-23T15:04:00Z", { tool: "post_to_chat", channel: "#random",
      error: "dude's Slack bot is not in #random: ask someone in the channel to run /invite @dude" });
    // Another run's event is not this run's, whatever its payload says: calls are read by run_id.
    await owner`INSERT INTO events (id, organization_id, event_type, actor_type, actor_id, source, occurred_at, run_id, payload)
      VALUES ('evt_x1', ${ORG}, 'agent.tool.dude', 'agent', 'arn_x', 'orchestrator', '2026-10-23T15:05:00Z', 'arn_x',
        ${{ tool: "github_search", arguments: {}, result: {}, automationRunId: "arn_d" }})`;
    const run = await body(await call("bo", "GET", `/v1/automations/${a.id}/runs/arn_d`));
    expect(run.toolCalls).toEqual([
      { id: "evt_d1", tool: "github_comment", at: "2026-10-23T15:00:05.000Z", status: "completed", args: { issue: 12 }, durationMs: null, error: null, posted: null },
      { id: "evt_d4", tool: "post_to_chat", at: "2026-10-23T15:03:12.000Z", status: "completed", args: { channel: "#engineering" }, durationMs: 640,
        error: null, posted: { channel: "#engineering", channelId: "C1", ts: "1.000100", permalink: "https://acme.slack.com/archives/C1/p1" } },
      { id: "evt_d5", tool: "post_to_chat", at: "2026-10-23T15:03:59.700Z", status: "failed", args: { channel: "#random" }, durationMs: 300,
        error: "dude's Slack bot is not in #random: ask someone in the channel to run /invite @dude", posted: null },
    ]);
    expect(run.failedCallId).toBeNull();
  });

  test("a run failed on a post without Slack names the call it failed on; a refusal the agent could fix names none", async () => {
    const { body: a } = await create("ana", { name: "Digest without Slack" });
    await owner`INSERT INTO automation_runs (id, organization_id, automation_id, status, reason, source) VALUES
      ('arn_ns', ${ORG}, ${a.id}, 'failed', 'post_to_chat failed: Slack is not connected for Acme', 'schedule'),
      ('arn_fx', ${ORG}, ${a.id}, 'failed', 'the agent gave up', 'schedule')`;
    const ev = (id: string, run: string, at: string, payload: Record<string, unknown>) =>
      owner`INSERT INTO events (id, organization_id, event_type, actor_type, actor_id, source, occurred_at, run_id, payload)
        VALUES (${id}, ${ORG}, 'slack.message.refused', 'agent', ${run}, 'orchestrator', ${at}::timestamptz, ${run}, ${{ ...payload, automationRunId: run }})`;
    await ev("evt_ns1", "arn_ns", "2026-10-23T15:03:00Z", { tool: "post_to_chat", channel: "#engineering", code: "not_connected",
      reason: "Slack is not connected for Acme", error: "Slack is not connected for Acme: an organisation admin adds dude's Slack app" });
    await owner`INSERT INTO events (id, organization_id, event_type, actor_type, actor_id, source, occurred_at, run_id, payload)
      VALUES ('evt_ns2', ${ORG}, 'agent.tool.dude', 'agent', 'arn_ns', 'orchestrator', '2026-10-23T15:04:00Z', 'arn_ns',
        ${{ tool: "github_search", arguments: { q: "is:pr" }, result: {}, automationRunId: "arn_ns" }})`;
    await ev("evt_fx1", "arn_fx", "2026-10-23T15:03:00Z", { tool: "post_to_chat", channel: "#nowhere", error: "there is no channel #nowhere" });
    // Refused for want of Slack, then posted once it was connected: the run did not fail on the refusal.
    await owner`INSERT INTO automation_runs (id, organization_id, automation_id, status, reason, source) VALUES
      ('arn_lt', ${ORG}, ${a.id}, 'failed', 'the agent gave up', 'schedule')`;
    await ev("evt_lt1", "arn_lt", "2026-10-23T15:03:00Z", { tool: "post_to_chat", channel: "#engineering", code: "not_connected",
      reason: "Slack is not connected for Acme", error: "Slack is not connected for Acme" });
    await owner`INSERT INTO events (id, organization_id, event_type, actor_type, actor_id, source, occurred_at, run_id, payload)
      VALUES ('evt_lt2', ${ORG}, 'slack.message.posted', 'agent', 'arn_lt', 'orchestrator', '2026-10-23T15:05:00Z', 'arn_lt',
        ${{ channel: "#engineering", permalink: "https://acme.slack.com/archives/C1/p2", automationRunId: "arn_lt" }})`;
    expect((await body(await call("bo", "GET", `/v1/automations/${a.id}/runs/arn_lt`))).failedCallId).toBeNull();
    // The orchestrator records the refusal it failed the run on; the page names that one.
    await owner`INSERT INTO events (id, organization_id, event_type, actor_type, actor_id, source, occurred_at, run_id, payload)
      VALUES ('evt_ns3', ${ORG}, 'automation.run.failed_on_post', 'system', 'dude', 'orchestrator', '2026-10-23T15:05:00Z', 'arn_ns',
        ${{ automationId: a.id, automationRunId: "arn_ns", eventId: "evt_ns1" }})`;
    expect((await body(await call("bo", "GET", `/v1/automations/${a.id}/runs/arn_ns`))).failedCallId).toBe("evt_ns1");
    expect((await body(await call("bo", "GET", `/v1/automations/${a.id}/runs/arn_fx`))).failedCallId).toBeNull();
    // Failed for its own reason (timed out), with a not-connected refusal and no post: it did not fail on the refusal.
    await owner`INSERT INTO automation_runs (id, organization_id, automation_id, status, reason, source) VALUES
      ('arn_to', ${ORG}, ${a.id}, 'failed', 'timed out after 30 minutes', 'schedule')`;
    await ev("evt_to1", "arn_to", "2026-10-23T15:03:00Z", { tool: "post_to_chat", channel: "#engineering", code: "not_connected",
      reason: "Slack is not connected for Acme", error: "Slack is not connected for Acme" });
    expect((await body(await call("bo", "GET", `/v1/automations/${a.id}/runs/arn_to`))).failedCallId).toBeNull();
  });

  test("Run now, test-event evaluation and Start fresh go to the orchestrator as the person; members who neither edit nor own it are refused", async () => {
    const { body: a } = await create("ana", { name: "Runnable" });
    asked.length = 0;
    expect((await call("bo", "POST", `/v1/automations/${a.id}/runs`, {})).status).toBe(403);
    expect((await call("bo", "POST", `/v1/automations/${a.id}/start-fresh`)).status).toBe(403);
    const test = { testEvent: { event: "pull_request", action: "opened", deliveryId: "test-1", body: { action: "opened" } } };
    const ran = await call("ana", "POST", `/v1/automations/${a.id}/runs`, test);
    expect(ran.status).toBe(202);
    expect(asked).toEqual([{ method: "POST", path: `/internal/automations/${a.id}/runs`, person: people.ana!, body: JSON.stringify(test) }]);
    expect((await call("ana", "POST", `/v1/automations/${a.id}/runs`, { testEvent: { ...test.testEvent, body: [1] } })).status).toBe(400);

    // The owner who cannot edit may still run it.
    await call("ana", "PUT", `/v1/automations/${a.id}/owner`, { personId: people.bo });
    expect((await call("bo", "POST", `/v1/automations/${a.id}/runs`, { deliveryId: "d-1" })).status).toBe(202);

    const verdict = await body(await call("bo", "POST", `/v1/automations/${a.id}/test-event/evaluate`, { body: { sender: { login: "dependabot[bot]" } } }));
    expect(verdict).toEqual({ matches: false, filter: "author", because: "it is by dependabot[bot], who is excluded" });

    // A schedule's next times are the orchestrator's; a body that is not {cron, tz} never reaches it.
    asked.length = 0;
    expect((await call("bo", "POST", "/v1/automations/schedule/next", { cron: "0 3 * * *", tz: "UTC" })).status).toBe(202);
    expect(asked.map((q) => [q.path, q.body])).toEqual([["/internal/automations/schedule/next", JSON.stringify({ cron: "0 3 * * *", tz: "UTC" })]]);
    expect((await call("bo", "POST", "/v1/automations/schedule/next", { cron: "0 3 * * *" })).status).toBe(400);
    expect(asked.length).toBe(1);
    expect((await call("bo", "POST", `/v1/automations/${a.id}/start-fresh`)).status).toBe(204);

    const subs = await body(await call("bo", "GET", "/v1/automations/subscriptions"));
    expect(subs).toEqual({ events: ["pull_request", "push"], source: "app", settingsUrl: null });
  });

  test("past deliveries of the automation's event, from the last 30 days", async () => {
    const { body: a } = await create("ana", { name: "Past" });
    await owner`INSERT INTO webhook_deliveries (id, organization_id, event, payload, received_at) VALUES
      ('pd-1', ${ORG}, 'pull_request', '{"action":"opened","repository":{"full_name":"acme/web"},"sender":{"login":"ana"}}', now() - interval '1 day'),
      ('pd-2', ${ORG}, 'pull_request', '{"action":"closed"}', now() - interval '31 days'),
      ('pd-3', ${ORG}, 'push', '{}', now())`;
    const { deliveries } = await body(await call("bo", "GET", `/v1/automations/${a.id}/deliveries`));
    expect(deliveries.map((d: { id: string }) => d.id)).toEqual(["pd-1"]);
    expect(deliveries[0]).toMatchObject({ action: "opened", repository: "acme/web", sender: "ana", verdict: null });
    expect("body" in deliveries[0]).toBe(false);
    // What the matcher made of it for this automation.
    await owner`INSERT INTO automation_github_verdicts (organization_id, automation_id, delivery_id, verdict, reason)
      VALUES (${ORG}, ${a.id}, 'pd-1', 'skipped', 'Author: it is by ana, who is excluded')`;
    const { deliveries: after } = await body(await call("bo", "GET", `/v1/automations/${a.id}/deliveries?verdict=skipped`));
    expect(after.map((d: { id: string; verdict: string; reason: string }) => [d.id, d.verdict, d.reason]))
      .toEqual([["pd-1", "skipped", "Author: it is by ana, who is excluded"]]);
    expect((await body(await call("bo", "GET", `/v1/automations/${a.id}/deliveries?verdict=started`))).deliveries).toEqual([]);
  });
});

describe("delete and duplicate", () => {
  test("Duplicate copies the settings off, as '<name> (copy)', owned by the caller, with no runs or history; members get 403", async () => {
    const { body: a } = await create("ana", { name: "Label PRs", projectId: WEB });
    const settings = { name: "Label PRs", kind: "agent", trigger: { kind: "github", event: "pull_request", actions: ["opened"], filters: {} },
      agent: { own: { prompt: "Label {{pr.title}}." } }, concurrency: "skip" };
    expect((await call("ana", "PUT", `/v1/automations/${a.id}`, settings)).status).toBe(200);
    await call("ana", "PUT", `/v1/automations/${a.id}/enabled`, { enabled: true });
    // Bo owns it, but members do not edit a project's automations.
    await call("ana", "PUT", `/v1/automations/${a.id}/owner`, { personId: people.bo });
    await owner`INSERT INTO automation_runs (id, organization_id, automation_id, status, source) VALUES ('arn_dup', ${ORG}, ${a.id}, 'succeeded', 'event')`;

    expect((await call("bo", "POST", `/v1/automations/${a.id}/duplicate`)).status).toBe(403);
    const res = await call("ana", "POST", `/v1/automations/${a.id}/duplicate`);
    expect(res.status).toBe(201);
    const copy = await body(res);
    expect(copy).toMatchObject({ name: "Label PRs (copy)", enabled: false, projectId: WEB, owner: { id: people.ana }, concurrency: "skip",
      trigger: { event: "pull_request", actions: ["opened"] }, agent: { own: { prompt: "Label {{pr.title}}." } }, lastRun: null });
    expect(copy.id).not.toBe(a.id);
    expect((await body(await call("ana", "GET", `/v1/automations/${copy.id}/runs`))).total).toBe(0);
    expect((await body(await call("ana", "GET", `/v1/automations/${copy.id}/prompt-history`))).versions).toEqual([]);
  });

  test("a duplicated webhook gets a signing secret of its own", async () => {
    const { body: a } = await create("ana", { name: "Notify", kind: "webhook", trigger: "github" });
    const copy = await body(await call("ana", "POST", `/v1/automations/${a.id}/duplicate`));
    const [x, y] = await owner`SELECT id, webhook_secret FROM automations WHERE id IN (${a.id}, ${copy.id}) ORDER BY id = ${a.id} DESC` as Array<{ webhook_secret: string }>;
    expect(x!.webhook_secret).toStartWith("whsec_");
    expect(y!.webhook_secret).toStartWith("whsec_");
    expect(x!.webhook_secret).not.toBe(y!.webhook_secret);
  });

  test("Delete removes it with its runs; a kept session is ended first through the orchestrator; members get 403", async () => {
    const { body: a } = await create("ana", { name: "Kept" });
    await owner`INSERT INTO automation_runs (id, organization_id, automation_id, status, source) VALUES ('arn_del', ${ORG}, ${a.id}, 'failed', 'event')`;
    await owner`INSERT INTO automation_sessions (automation_id, organization_id, lux_run_id) VALUES (${a.id}, ${ORG}, 'lux_kept')`;
    expect((await call("bo", "DELETE", `/v1/automations/${a.id}`)).status).toBe(403);
    asked.length = 0;
    expect((await call("ana", "DELETE", `/v1/automations/${a.id}`)).status).toBe(204);
    expect(asked.map((q) => [q.method, q.path, q.person ?? ""])).toEqual([["POST", `/internal/automations/${a.id}/start-fresh`, people.ana!]]);
    expect((await call("ana", "GET", `/v1/automations/${a.id}`)).status).toBe(404);
    expect([...await owner`SELECT count(*)::int AS n FROM automation_runs WHERE id = 'arn_del'`]).toEqual([{ n: 0 }]);
    // Without a kept session, nothing is asked of the orchestrator.
    const { body: b } = await create("ana", { name: "Ephemeral" });
    asked.length = 0;
    expect((await call("ana", "DELETE", `/v1/automations/${b.id}`)).status).toBe(204);
    expect(asked).toEqual([]);
  });
});

describe("secrets scope", () => {
  const settings = (secrets: unknown) => ({ name: "Secrets", kind: "command", trigger: { kind: "schedule", cron: "0 3 * * *", tz: "UTC" },
    command: "run-report", secrets });

  test("an organisation automation picks the organisation's secrets and refuses a project's", async () => {
    const { body: org } = await create("ana", { name: "Secrets", kind: "command", trigger: "schedule" });
    const denied = await call("ana", "PUT", `/v1/automations/${org.id}`, settings([{ source: "project", name: "ACME_TOKEN" }]));
    expect(denied.status).toBe(400);
    const saved = await call("ana", "PUT", `/v1/automations/${org.id}`, settings([{ source: "organization", name: "GH_TOKEN" }]));
    expect(saved.status).toBe(200);
    expect((await body(saved)).secrets).toEqual([{ source: "organization", name: "GH_TOKEN" }]);
  });

  test("a project automation picks both levels' secrets, each by its source", async () => {
    const picks = [{ source: "organization", name: "GH_TOKEN" }, { source: "project", name: "GH_TOKEN" }];
    const { body: project } = await create("ana", { name: "Secrets", kind: "command", trigger: "schedule", projectId: WEB });
    const saved = await call("ana", "PUT", `/v1/automations/${project.id}`, settings(picks));
    expect(saved.status).toBe(200);
    expect((await body(saved)).secrets).toEqual(picks);
    // A bare name, the old shape, is refused: the pick must say which level.
    expect((await call("ana", "PUT", `/v1/automations/${project.id}`, settings(["GH_TOKEN"]))).status).toBe(400);
  });
});

describe("prompt history", () => {
  const settingsWith = (prompt: string) => ({ name: "Triage", kind: "agent", trigger: { kind: "github", event: "pull_request", actions: [], filters: {} },
    agent: { own: { prompt } } });

  test("each save that changes the prompt is a version, by whom and when; restore saves an old one again as the newest", async () => {
    const { body: a } = await create("ana", { name: "Triage" });
    for (const p of ["Label {{pr.title}}.", "Label {{pr.title}} and {{repository}}.", "Label {{pr.title}} and {{repository}}."]) {
      expect((await call("ana", "PUT", `/v1/automations/${a.id}`, settingsWith(p))).status).toBe(200);
    }
    // A member reads it.
    const { versions } = await body(await call("bo", "GET", `/v1/automations/${a.id}/prompt-history`));
    // The empty prompt it was created with is the first version; the unchanged third save adds none.
    expect(versions.map((v: { number: number; body: string }) => [v.number, v.body])).toEqual([
      [3, "Label {{pr.title}} and {{repository}}."],
      [2, "Label {{pr.title}}."],
      [1, ""],
    ]);
    expect(versions[0]).toMatchObject({ current: true, createdBy: { id: people.ana, name: "ana" }, restoredFrom: null });
    expect(versions[2].createdBy).toBeNull();
    expect(typeof versions[0].createdAt).toBe("string");

    const first = versions[1].id;
    expect((await call("bo", "POST", `/v1/automations/${a.id}/prompt-history/restore`, { versionId: first })).status).toBe(403);
    expect((await call("ana", "POST", `/v1/automations/${a.id}/prompt-history/restore`, { versionId: "apv_nope" })).status).toBe(404);
    const restored = await call("ana", "POST", `/v1/automations/${a.id}/prompt-history/restore`, { versionId: first });
    expect(restored.status).toBe(200);
    expect((await body(restored)).agent).toEqual({ own: { prompt: "Label {{pr.title}}." } });
    const after = (await body(await call("ana", "GET", `/v1/automations/${a.id}/prompt-history`))).versions;
    expect(after.map((v: { number: number; restoredFrom: number | null; current: boolean }) => [v.number, v.restoredFrom, v.current]))
      .toEqual([[4, 2, true], [3, null, false], [2, null, false], [1, null, false]]);
  });

  test("a preset's brief is versioned the same way", async () => {
    const { body: a } = await create("ana", { name: "QA", preset: "qa_browser", trigger: "schedule" });
    const preset = (brief: string) => ({ name: "QA", kind: "agent", trigger: { kind: "schedule", cron: "0 3 * * *", tz: "UTC" },
      agent: { preset: { role: "qa_browser", overrides: {} }, brief } });
    expect((await call("ana", "PUT", `/v1/automations/${a.id}`, preset("Walk the checkout."))).status).toBe(200);
    const { versions } = await body(await call("ana", "GET", `/v1/automations/${a.id}/prompt-history`));
    // The empty brief it was created with, then the saved one.
    expect(versions.map((v: { number: number; body: string; createdBy: unknown }) => [v.number, v.body, v.createdBy === null]))
      .toEqual([[2, "Walk the checkout.", false], [1, "", true]]);
  });
});

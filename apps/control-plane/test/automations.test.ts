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
  // The organization's first person is its admin; the next ones members.
  for (const [who, org] of [["ana", ORG], ["bo", ORG], ["cy", ORG], ["di", OTHER]] as const) {
    const k = await createApiKey({ organizationId: org, name: who });
    keys[who] = k.key;
    people[who] = k.personId;
  }
  useConfig(Config.load({ env: { ...process.env } }));
  router = buildRouter("");
});

afterAll(async () => {
  useConfig(null);
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
    const ok = await call("ana", "PUT", `/v1/automations/${a.id}`, { ...base, sessionMode: "resumable", expiryDays: 30, repos: ["repo_w"] });
    expect(ok.status).toBe(200);
    const otherProjects = await call("ana", "PUT", `/v1/automations/${a.id}`, { ...base, repos: ["repo_s"] });
    expect(otherProjects.status).toBe(400);
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
    expect((await call("ana", "DELETE", `/v1/people/${people.cy}`)).status).toBe(204);
    const after = await body(await call("ana", "GET", `/v1/automations/${a.id}`));
    expect(after.owner).toBeNull();
    expect(after.ownerLeftAt).not.toBeNull();
    expect(after.enabled).toBe(true);
    // The admins hand it to someone, which ends "held".
    const given = await body(await call("ana", "PUT", `/v1/automations/${a.id}/owner`, { personId: people.bo }));
    expect(given).toMatchObject({ owner: { id: people.bo }, ownerLeftAt: null });
  });
});

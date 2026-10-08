/**
 * A project's repositories are checked out by their Runs under lux_name
 * (migration 090), which rewrites a name lux refuses and can land on another
 * repository's own ("Web" is web-29751047). Adding, renaming or creating a
 * project with a repository whose checkout name another of the project's
 * already has is refused with 409 naming both, and changes nothing; the
 * same name in another project is fine.
 *
 * Requires DATABASE_URL: the owner of a database migrated to the release.
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { personPrincipal } from "../src/api/auth.ts";
import { Router } from "../src/api/router.ts";
import { registerProjectRoutes } from "../src/api/routes/projects.ts";
import { registerStructureRoutes } from "../src/api/routes/structure.ts";
import { closePool, setPool } from "../src/db/client.ts";

const org = `org_luxname_${Bun.randomUUIDv7("hex").slice(-12)}`;
const person = `${org}_person`;
const project = `${org}_project`;
const other = `${org}_other`;
let owner: SQL;
let app: SQL;
let router: Router;

beforeAll(async () => {
  owner = new SQL(process.env.DATABASE_URL!);
  const url = new URL(process.env.DATABASE_URL!);
  url.username = "dude_app"; url.password = "dude_app";
  app = new SQL(url.toString()); setPool(app);
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${org}, ${org}, ${org})`;
  await owner`INSERT INTO people (id, organization_id, name, role) VALUES (${person}, ${org}, 'Person', 'admin')`;
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES
    (${project}, ${org}, 'Web', ${`${org}-web`}, 'WEB'), (${other}, ${org}, 'Other', ${`${org}-other`}, 'OTH')`;
  const principal = await personPrincipal(org, person);
  router = new Router(async () => principal);
  registerProjectRoutes(router);
  registerStructureRoutes(router);
});
afterAll(async () => {
  await closePool(app);
  await owner`DELETE FROM organizations WHERE id = ${org}`;
  await owner.end();
});

const call = (method: string, path: string, body: Record<string, unknown>) => router.handle(new Request(`http://dude.test${path}`, {
  method, headers: { "content-type": "application/json" }, body: JSON.stringify(body),
}));
const add = (projectId: string, name: string) =>
  call("POST", `/v1/projects/${projectId}/repositories`, { name, url: `https://github.com/acme/${name.toLowerCase()}.git` });
const names = async (projectId: string) =>
  (await owner`SELECT name FROM repositories WHERE project_id = ${projectId} ORDER BY name`).map((r: { name: string }) => r.name);

async function refused(res: Response, message: string) {
  expect(res.status).toBe(409);
  expect(((await res.json()) as { error: { message: string } }).error.message).toBe(message);
}

test("adding a repository its Runs would check out under another's name is refused, saying which; elsewhere it is added", async () => {
  expect((await add(project, "Web")).status).toBe(201);
  await refused(await add(project, "web-29751047"),
    "web-29751047 would be checked out as web-29751047, which Web already is; rename one of them.");
  expect(await names(project)).toEqual(["Web"]);
  // A name lux takes as it is, and the same name in another project, are added.
  expect((await add(project, "web-app")).status).toBe(201);
  expect((await add(other, "web-29751047")).status).toBe(201);
  expect(await names(project)).toEqual(["Web", "web-app"]);
});

test("renaming a repository onto another's checkout name is refused; renaming it to itself is not", async () => {
  const [app] = await owner`SELECT id FROM repositories WHERE project_id = ${project} AND name = 'web-app'` as Array<{ id: string }>;
  await refused(await call("PATCH", `/v1/repositories/${app!.id}`, { name: "web-29751047" }),
    "web-29751047 would be checked out as web-29751047, which Web already is; rename one of them.");
  expect(await names(project)).toEqual(["Web", "web-app"]);
  const [web] = await owner`SELECT id FROM repositories WHERE project_id = ${project} AND name = 'Web'` as Array<{ id: string }>;
  expect((await call("PATCH", `/v1/repositories/${web!.id}`, { name: "Web" })).status).toBe(200);
});

test("a new project whose repositories would share a checkout name is refused whole", async () => {
  const slug = `pair-${Bun.randomUUIDv7("hex").slice(-12)}`;
  await refused(await call("POST", "/v1/projects", { name: "Pair", slug, repositories: [
    { name: "Web", url: "https://github.com/acme/a.git" }, { name: "web-29751047", url: "https://github.com/acme/b.git" }] }),
  "web-29751047 would be checked out as web-29751047, which Web already is; rename one of them.");
  expect((await owner`SELECT id FROM projects WHERE slug = ${slug}`).length).toBe(0);
});

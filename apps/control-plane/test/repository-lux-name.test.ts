/**
 * A project's repositories are checked out by their Runs under lux_name
 * (migration 090), which rewrites a name lux refuses and can land on another
 * repository's own ("Web" is web-29751047). Adding, renaming or creating a
 * project with a repository whose checkout name another of the project's
 * already has is refused with 409 naming both, and changes nothing; the
 * same name in another project is fine. Two such changes at once get one
 * 409 too, never a 500 from the index.
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
const names = async (projectId: string): Promise<string[]> =>
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

/** A project of its own with repositories by name, inserted directly; their ids by name. */
async function projectWith(...repos: string[]): Promise<{ id: string; repo: Record<string, string> }> {
  const id = `${org}_${Bun.randomUUIDv7("hex").slice(-12)}`;
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix)
    VALUES (${id}, ${org}, ${id}, ${id}, ${`P${id.slice(-5).toUpperCase()}`})`;
  const repo: Record<string, string> = {};
  for (const name of repos) {
    repo[name] = `${id}_${name}`;
    await owner`INSERT INTO repositories (id, organization_id, project_id, name, url)
      VALUES (${repo[name]}, ${org}, ${id}, ${name}, ${`https://github.com/acme/${name}.git`})`;
  }
  return { id, repo };
}

test("renaming a repository onto another's checkout name is refused; renaming it to itself is not", async () => {
  const { id, repo } = await projectWith("Web", "web-app");
  await refused(await call("PATCH", `/v1/repositories/${repo["web-app"]}`, { name: "web-29751047" }),
    "web-29751047 would be checked out as web-29751047, which Web already is; rename one of them.");
  expect(await names(id)).toEqual(["Web", "web-app"]);
  expect((await call("PATCH", `/v1/repositories/${repo["Web"]}`, { name: "Web" })).status).toBe(200);
});

/**
 * Holds every write to `projectId`'s repositories for a second, so two
 * requests both read before either writes: each one's own check sees no
 * clash. Returns its removal.
 */
async function slowWrites(projectId: string): Promise<() => Promise<void>> {
  const fn = `slow_${projectId.replace(/[^a-z0-9_]/g, "_")}`;
  await owner.unsafe(`CREATE FUNCTION ${fn}() RETURNS trigger LANGUAGE plpgsql AS $$
    BEGIN IF NEW.project_id = '${projectId}' THEN PERFORM pg_sleep(1); END IF; RETURN NEW; END $$`);
  await owner.unsafe(`CREATE TRIGGER ${fn} BEFORE INSERT OR UPDATE ON repositories FOR EACH ROW EXECUTE FUNCTION ${fn}()`);
  return async () => {
    await owner.unsafe(`DROP TRIGGER ${fn} ON repositories`);
    await owner.unsafe(`DROP FUNCTION ${fn}()`);
  };
}

const statuses = async (responses: Response[]) => {
  const out = [];
  for (const r of responses) out.push([r.status, ((await r.json()) as { error?: { message: string } }).error?.message ?? null]);
  return out;
};

test("two renames at once onto one checkout name: one is renamed, the other refused with 409, never 500", async () => {
  const { id, repo } = await projectWith("first", "second");
  const restore = await slowWrites(id);
  try {
    const got = await statuses(await Promise.all([
      call("PATCH", `/v1/repositories/${repo["first"]}`, { name: "Web" }),
      call("PATCH", `/v1/repositories/${repo["second"]}`, { name: "web-29751047" }),
    ]));
    expect(got.map(([s]) => s).sort()).toEqual([200, 409]);
    const [, message] = got.find(([s]) => s === 409)!;
    expect([
      "Web would be checked out as web-29751047, which web-29751047 already is; rename one of them.",
      "web-29751047 would be checked out as web-29751047, which Web already is; rename one of them.",
    ]).toContain(message as string);
  } finally {
    await restore();
  }
  const left = await names(id);
  expect(left.length).toBe(2);
  expect(left.filter((n) => n === "Web" || n === "web-29751047").length).toBe(1);
}, 20_000);

test("an add and a rename at once onto one checkout name: one lands, the other is refused with 409, never 500", async () => {
  const { id, repo } = await projectWith("first");
  const restore = await slowWrites(id);
  try {
    const got = await statuses(await Promise.all([
      add(id, "Web"),
      call("PATCH", `/v1/repositories/${repo["first"]}`, { name: "web-29751047" }),
    ]));
    // The add answers 201 when it lands, the rename 200.
    const landed = got[0]![0] === 201 ? [201, 409] : [409, 200];
    expect(got.map(([s]) => s)).toEqual(landed);
    const [, message] = got.find(([s]) => s === 409)!;
    expect([
      "Web would be checked out as web-29751047, which web-29751047 already is; rename one of them.",
      "web-29751047 would be checked out as web-29751047, which Web already is; rename one of them.",
    ]).toContain(message as string);
  } finally {
    await restore();
  }
  expect((await names(id)).filter((n) => n === "Web" || n === "web-29751047").length).toBe(1);
}, 20_000);

test("a new project whose repositories would share a checkout name is refused whole", async () => {
  const slug = `pair-${Bun.randomUUIDv7("hex").slice(-12)}`;
  await refused(await call("POST", "/v1/projects", { name: "Pair", slug, repositories: [
    { name: "Web", url: "https://github.com/acme/a.git" }, { name: "web-29751047", url: "https://github.com/acme/b.git" }] }),
  "web-29751047 would be checked out as web-29751047, which Web already is; rename one of them.");
  expect((await owner`SELECT id FROM projects WHERE slug = ${slug}`).length).toBe(0);
});

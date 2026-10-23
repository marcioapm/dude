/**
 * GET /v1/tasks/:id takes a task's key as well as its id (the dude CLI's
 * TASK): case-insensitive, leading zeros allowed, archived tasks included,
 * only in the caller's organisation.
 *
 * Against a database of its own, migrated as a release is. Requires
 * DATABASE_URL: a role that can create databases.
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { join } from "node:path";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");
const NAME = `dude_task_key_test_${Bun.randomUUIDv7("hex").slice(-12)}`;

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
let ana: { key: string };

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const migrate = Bun.spawnSync(["bun", "run", "apps/control-plane/src/db/migrate.ts"], {
    cwd: ROOT,
    env: { ...process.env, DATABASE_URL: databaseUrl("owner", NAME) },
  });
  if (migrate.exitCode !== 0) throw new Error(`migrate: ${migrate.stderr.toString()}`);
  owner = new SQL(databaseUrl("owner", NAME));
  for (const [org, project, prefix] of [["org_a", "prj_a", "WEB"], ["org_b", "prj_b", "API"]] as const) {
    await owner`INSERT INTO organizations (id, name, slug) VALUES (${org}, ${org}, ${org})`;
    await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES (${project}, ${org}, ${prefix}, ${prefix.toLowerCase()}, ${prefix})`;
  }
  const task = (id: string, org: string, project: string, n: number) =>
    owner`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status)
      VALUES (${id}, ${org}, ${project}, ${n}, ${`Task ${n}`}, 'A goal long enough to save.', 'received')`;
  await task("wi_a7", "org_a", "prj_a", 7);
  await task("wi_a9", "org_a", "prj_a", 9);
  await owner`UPDATE tasks SET status = 'done', archived_at = now() WHERE id = 'wi_a9'`;
  await task("wi_b3", "org_b", "prj_b", 3);
  app = new SQL(databaseUrl("app", NAME));
  setPool(app);
  ana = await createApiKey({ organizationId: "org_a", name: "Ana" });
  router = buildRouter("");
});

afterAll(async () => {
  await closePool(app);
  await owner?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

async function get(ref: string): Promise<{ status: number; id: string | undefined }> {
  const res = await router.handle(new Request(`http://dude.test/v1/tasks/${encodeURIComponent(ref)}`, {
    headers: { authorization: `Bearer ${ana.key}` },
  }));
  const body = (await res.json()) as { id?: string };
  return { status: res.status, id: body.id };
}

test("a task is found by its id or its key, in any case and with leading zeros", async () => {
  for (const ref of ["wi_a7", "WEB-7", "web-7", "WEB-007"]) {
    expect({ ref, ...(await get(ref)) }).toEqual({ ref, status: 200, id: "wi_a7" });
  }
});

test("an archived task is found by its key", async () => {
  expect(await get("WEB-9")).toEqual({ status: 200, id: "wi_a9" });
});

test("an unknown key, and another organisation's task, are not found", async () => {
  for (const ref of ["WEB-8", "API-3", "wi_b3"]) {
    expect({ ref, status: (await get(ref)).status }).toEqual({ ref, status: 404 });
  }
});

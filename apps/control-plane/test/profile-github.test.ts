/**
 * Your GitHub username and commit email, on your own profile: a username
 * is resolved to GitHub's numeric id through the orchestrator, an unknown
 * one refused, and one GitHub could not be asked about kept without its id.
 *
 * Against a database of its own and a stand-in orchestrator that answers
 * as GitHub would through it. Requires DATABASE_URL: a role that can
 * create databases.
 */

import { afterAll, beforeAll, beforeEach, expect, test } from "bun:test";
import { SQL } from "bun";
import { closePool, setPool } from "../src/db/client.ts";
import { migrate } from "../src/db/migrate.ts";
import { buildRouter } from "../src/index.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";
import { Config, useConfig } from "../src/config.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const NAME = `dude_profile_github_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_profile_github";

function databaseUrl(user: "owner" | "app"): string {
  const url = new URL(OWNER_URL);
  if (user === "app") {
    url.username = "dude_app";
    url.password = "dude_app";
  }
  url.pathname = `/${NAME}`;
  return url.toString();
}

let admin: SQL;
let owner: SQL;
let app: SQL;
let router: Router;
let key: string;
let personId: string;
let orchestrator: ReturnType<typeof Bun.serve>;
// How the stand-in answers: GitHub knows these logins; down, it answers 502.
const users: Record<string, number> = { "ana-dev": 5150 };
let githubDown = false;

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  await migrate(databaseUrl("owner"), { log() {} });
  owner = new SQL(databaseUrl("owner"));
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${ORG}, ${ORG}, ${ORG})`;
  app = new SQL(databaseUrl("app"));
  setPool(app);
  const made = await createApiKey({ organizationId: ORG, name: "Ana" });
  key = made.key;
  personId = made.personId;
  orchestrator = Bun.serve({ port: 0, fetch(request) {
    const login = decodeURIComponent(new URL(request.url).pathname.split("/").pop() ?? "");
    if (githubDown) return Response.json({ error: { code: "github_unavailable", message: "down" } }, { status: 502 });
    const id = users[login.toLowerCase()];
    return id
      ? Response.json({ login, id })
      : Response.json({ error: { code: "github_user_unknown", message: `GitHub has no user "${login}"` } }, { status: 404 });
  } });
  useConfig(Config.load({ env: { ...process.env, DUDE_ORCHESTRATOR_URL: `http://localhost:${orchestrator.port}`, DUDE_ORCHESTRATOR_TOKEN: "t" } }));
  router = buildRouter("");
});

afterAll(async () => {
  orchestrator?.stop(true);
  useConfig(null);
  await closePool(app);
  await owner?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

const patchMe = (body: Record<string, unknown>) => router.handle(new Request("http://dude.test/v1/me", {
  method: "PATCH",
  headers: { authorization: `Bearer ${key}`, "content-type": "application/json" },
  body: JSON.stringify(body),
}));

const stored = async () => (await owner`
  SELECT github_login, github_user_id::int AS github_user_id, commit_email::text FROM people WHERE id = ${personId}`)[0];

// Each test starts from the same profile: a login GitHub knows, and a
// commit email.
beforeEach(async () => {
  githubDown = false;
  await owner`UPDATE people SET github_login = 'ana-dev', github_user_id = 5150, commit_email = 'ana@users.example' WHERE id = ${personId}`;
});

test("a GitHub username is saved with GitHub's id for it, and shown back on your profile", async () => {
  await owner`UPDATE people SET github_login = NULL, github_user_id = NULL, commit_email = NULL WHERE id = ${personId}`;
  const res = await patchMe({ githubLogin: "ana-dev", commitEmail: "ana@users.example" });
  expect(res.status).toBe(200);
  const { person } = (await res.json()) as { person: { githubLogin: string; commitEmail: string } };
  expect(person).toMatchObject({ githubLogin: "ana-dev", commitEmail: "ana@users.example" });
  expect(await stored()).toEqual({ github_login: "ana-dev", github_user_id: 5150, commit_email: "ana@users.example" });
});

test("a username GitHub does not know is refused, 422, and nothing changes", async () => {
  const res = await patchMe({ githubLogin: "nobody-here" });
  expect(res.status).toBe(422);
  expect(((await res.json()) as { error: { message: string } }).error.message).toContain("GitHub has no user named nobody-here");
  expect(await stored()).toEqual({ github_login: "ana-dev", github_user_id: 5150, commit_email: "ana@users.example" });
});

test("a username saved while GitHub cannot be asked is kept, without an id, and the commit email is untouched", async () => {
  githubDown = true;
  const res = await patchMe({ githubLogin: "someone-else" });
  expect(res.status).toBe(200);
  expect(await stored()).toEqual({ github_login: "someone-else", github_user_id: null, commit_email: "ana@users.example" });
});

test("an empty username or commit email clears it", async () => {
  expect((await patchMe({ githubLogin: "", commitEmail: "" })).status).toBe(200);
  expect(await stored()).toEqual({ github_login: null, github_user_id: null, commit_email: null });
  expect((await patchMe({ githubLogin: "not a login" })).status).toBe(400);
});

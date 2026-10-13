/**
 * Connecting GitHub with where GitHub reaches dude: the token is saved and
 * the answer comes back without waiting on GitHub, the orchestrator asked
 * to register the organization's hooks in the background.
 *
 * Against a database of its own, migrated as a release is, and a stand-in
 * orchestrator that takes seconds to answer a registration that waits.
 * Requires DATABASE_URL: a role that can create databases.
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { createHmac } from "node:crypto";
import { join } from "node:path";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";
import { Config, useConfig } from "../src/config.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");
const NAME = `dude_connect_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_connect";

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
let key: { key: string };
let orchestrator: ReturnType<typeof Bun.serve>;
const forwarded: Array<{ path: string; body: Record<string, unknown> }> = [];
// How the stand-in orchestrator answers an App's verify.
let verifyApp: () => Response = () => Response.json({ ok: true, repository: "acme/api", bot: "dude-acme[bot]" });

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const migrate = Bun.spawnSync(["bun", "run", "apps/control-plane/src/db/migrate.ts"], {
    cwd: ROOT,
    env: { ...process.env, DATABASE_URL: databaseUrl("owner", NAME) },
  });
  if (migrate.exitCode !== 0) throw new Error(`migrate: ${migrate.stderr.toString()}`);
  owner = new SQL(databaseUrl("owner", NAME));
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${ORG}, ${ORG}, ${ORG})`;
  app = new SQL(databaseUrl("app", NAME));
  setPool(app);
  const made = await createApiKey({ organizationId: ORG, name: "Ana" });
  await owner`UPDATE people SET role = 'admin' WHERE id = ${made.personId}`;
  key = made;
  orchestrator = Bun.serve({ port: 0, async fetch(request) {
    const body = (await request.json().catch(() => ({}))) as Record<string, unknown>;
    const path = new URL(request.url).pathname;
    if (path === "/internal/github/verify-app") return verifyApp();
    forwarded.push({ path, body });
    // A registration that waits takes as long as forty repositories would.
    if (!body.background) await Bun.sleep(3_000);
    return Response.json(body.background ? { background: true } : { repositories: [] }, { status: body.background ? 202 : 200 });
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

test("connecting GitHub saves the token and asks for the hooks in the background, without waiting on GitHub", async () => {
  const started = Date.now();
  const res = await router.handle(new Request("http://dude.test/v1/forge/credential", {
    method: "POST",
    headers: { authorization: `Bearer ${key.key}`, "content-type": "application/json" },
    body: JSON.stringify({ auth: "pat", secret: "ghp_new", publicUrl: "https://dude.example.com/" }),
  }));
  expect(res.status).toBe(200);
  expect(Date.now() - started).toBeLessThan(1_500);
  expect(((await res.json()) as { registered: unknown }).registered).toEqual({ background: true });
  expect(forwarded).toEqual([{ path: "/internal/webhooks/register",
    body: { url: `https://dude.example.com/v1/webhooks/github/${ORG}`, background: true } }]);
  const [cred] = await owner`SELECT secret, public_url FROM forge_credentials WHERE organization_id = ${ORG}`;
  expect(cred).toEqual({ secret: "ghp_new", public_url: "https://dude.example.com" });
});

test("a GitHub App connects with its App ID alone; the installation is found per repository owner", async () => {
  const post = (body: Record<string, unknown>) => router.handle(new Request("http://dude.test/v1/forge/credential", {
    method: "POST",
    headers: { authorization: `Bearer ${key.key}`, "content-type": "application/json" },
    body: JSON.stringify(body),
  }));
  const refused = await post({ auth: "github_app", secret: "-----BEGIN RSA PRIVATE KEY-----" });
  expect(refused.status).toBe(400);
  const res = await post({ auth: "github_app", secret: "-----BEGIN RSA PRIVATE KEY-----", appId: "9001" });
  expect(res.status).toBe(200);
  const [cred] = await owner`SELECT auth::text, app_id, installation_id FROM forge_credentials WHERE organization_id = ${ORG}`;
  expect(cred).toEqual({ auth: "github_app", app_id: "9001", installation_id: null });
  // The key is write-only: no part of it comes back.
  const shown = await router.handle(new Request("http://dude.test/v1/forge/credential", { headers: { authorization: `Bearer ${key.key}` } }));
  const body = (await shown.json()) as Record<string, unknown>;
  expect(body).toMatchObject({ connected: true, auth: "github_app", appId: "9001", installationId: null, secretHint: "" });
  expect(JSON.stringify(body)).not.toContain("BEGIN");
});

test("OAuth client secret is write-only, blank preserves it, and explicit clear removes it", async () => {
  const post = (extra: Record<string, unknown>) => router.handle(new Request("http://dude.test/v1/forge/credential", {
    method: "POST", headers: { authorization: `Bearer ${key.key}`, "content-type": "application/json" },
    body: JSON.stringify({ auth: "github_app", secret: "pem", appId: "9001", ...extra }),
  }));
  const saved = await post({ oauthClientId: "client", oauthClientSecret: "never-return-this-secret" });
  expect(saved.status).toBe(200); expect(await saved.text()).not.toContain("never-return-this-secret");
  expect((await post({ oauthClientSecret: "" })).status).toBe(200);
  const [kept] = await owner`SELECT oauth_client_secret FROM forge_credentials WHERE organization_id = ${ORG}`;
  expect(kept.oauth_client_secret).toBe("never-return-this-secret");
  const shown = await router.handle(new Request("http://dude.test/v1/forge/credential", { headers: { authorization: `Bearer ${key.key}` } }));
  expect(await shown.text()).not.toContain("never-return-this-secret");
  expect((await post({ clearOauthClientSecret: true })).status).toBe(200);
  const [cleared] = await owner`SELECT oauth_client_secret FROM forge_credentials WHERE organization_id = ${ORG}`;
  expect(cleared.oauth_client_secret).toBeNull();
});

test("Verify asks the orchestrator to mint an App's token, and says what it answered", async () => {
  const verify = async () => {
    const res = await router.handle(new Request("http://dude.test/v1/forge/credential/verify", {
      method: "POST", headers: { authorization: `Bearer ${key.key}` } }));
    expect(res.status).toBe(200);
    return res.json();
  };
  expect(await verify()).toEqual({ ok: true, app: true, bot: "dude-acme[bot]", repository: "acme/api" });
  verifyApp = () => Response.json({ ok: false, repository: "acme/api", reason: "GitHub would not give the App a token for acme/api" });
  expect(await verify()).toEqual({ ok: false, reason: "GitHub would not give the App a token for acme/api" });
  verifyApp = () => Response.json({ error: { code: "conflict", message: "GitHub is connected with a token, not an App" } }, { status: 409 });
  expect(await verify()).toEqual({ ok: false, reason: "GitHub is connected with a token, not an App" });
});

test("an App's deliveries are taken with the credential's secret; its installation events are answered and ignored", async () => {
  const [{ secret }] = await owner`SELECT webhook_secret AS secret FROM forge_credentials WHERE organization_id = ${ORG}`;
  const deliver = (event: string, payload: unknown, id: string) => {
    const body = JSON.stringify(payload);
    return router.handle(new Request(`http://dude.test/v1/webhooks/github/${ORG}`, {
      method: "POST",
      headers: { "content-type": "application/json", "x-github-event": event, "x-github-delivery": id,
        "x-hub-signature-256": `sha256=${createHmac("sha256", secret).update(body).digest("hex")}` },
      body,
    }));
  };
  const installation = { id: 71, account: { login: "acme" } };
  for (const event of ["installation", "installation_repositories"]) {
    const res = await deliver(event, { action: "created", installation }, `d-${event}`);
    expect(res.status).toBe(200);
    expect(await res.json()).toEqual({ ok: true, ignored: event });
  }
  const pr = await deliver("pull_request", { action: "opened", installation, number: 1, pull_request: { number: 1 },
    repository: { full_name: "acme/api" } }, "d-pr");
  expect(pr.status).toBe(202);
  const stored = await owner`SELECT id, event FROM webhook_deliveries WHERE organization_id = ${ORG} ORDER BY id`;
  expect([...stored]).toEqual([{ id: "d-pr", event: "pull_request" }]);
});

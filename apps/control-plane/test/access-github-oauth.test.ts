/**
 * Connect GitHub behind the Access gate, for a person in two organisations:
 * Connect names its organisation like any tenant call, and GitHub's redirect
 * back (cross-site, no organisation header) completes in the organisation
 * the state was started in and lands on Your settings there.
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import type { SQL } from "bun";
import { requestAuthenticator } from "../src/api/auth.ts";
import { accessAuthenticator, accessProfiles, accessVerifier } from "../src/api/cloudflareAccess.ts";
import { Router } from "../src/api/router.ts";
import { registerGithubOAuthRoutes } from "../src/api/routes/githubOAuth.ts";
import { Config, useConfig } from "../src/config.ts";
import { closePool, setPool } from "../src/db/client.ts";
import { AUD, Edge, ORIGIN, TEAM, accessConfig, cookie, databases, member, rsaKey, sign, type Key } from "./access-harness.ts";

const suffix = Bun.randomUUIDv7("hex").slice(-12);
const acme = `org_gho_${suffix}`;
const northwind = `${acme}_nw`;
const at = (local: string) => `${local}@${suffix}.gho.test`;
let owner: SQL;
let app: SQL;
let signer: Key;
let router: Router;
let acmePerson: string;
let northwindPerson: string;

const github = (async (input: string | URL | Request) => String(input) === "https://github.com/login/oauth/access_token"
  ? Response.json({ access_token: "access", refresh_token: "refresh", expires_in: 28800, refresh_token_expires_in: 15552000 })
  : Response.json({ id: 4242, login: "OctoNorth" })) as typeof fetch;

beforeAll(async () => {
  ({ owner, app } = databases());
  setPool(app);
  useConfig(Config.load({ env: { ...process.env, DUDE_AUTH_PROVIDER: "cloudflare_access", DUDE_AUTH_PUBLIC_URL: ORIGIN,
    DUDE_AUTH_CLOUDFLARE_ACCESS_TEAM: TEAM, DUDE_AUTH_CLOUDFLARE_ACCESS_AUD: AUD } }));
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${acme}, 'Acme', ${`acme-${suffix}`}), (${northwind}, 'Northwind', ${`northwind-${suffix}`})`;
  for (const org of [acme, northwind]) {
    await owner`INSERT INTO forge_credentials (id, organization_id, forge, auth, secret, app_id, oauth_client_id, oauth_client_secret)
      VALUES (${`fc_${org}`}, ${org}, 'github', 'github_app', 'pem', '1', 'client', 'secret')`;
  }
  acmePerson = await member(owner, acme, at("two"), { name: "Two Orgs" });
  northwindPerson = await member(owner, northwind, at("two"), { name: "Two Orgs" });
  await member(owner, northwind, at("other"), { name: "Other" });
  signer = await rsaKey("k1");
  const edge = new Edge();
  edge.keys = [signer.jwk];
  router = new Router(requestAuthenticator(accessAuthenticator(accessConfig(), "", {
    verify: accessVerifier(TEAM, AUD, edge.fetch), profile: accessProfiles(TEAM, edge.fetch),
  })));
  registerGithubOAuthRoutes(router, github);
});
afterAll(async () => {
  useConfig(null);
  await closePool(app);
  for (const table of ["github_user_authorizations", "github_oauth_connections", "github_oauth_transactions", "forge_credentials", "people"]) {
    await owner.unsafe(`DELETE FROM ${table} WHERE organization_id IN ($1, $2)`, [acme, northwind]);
  }
  await owner`DELETE FROM organizations WHERE id IN (${acme}, ${northwind})`;
  await owner`DELETE FROM accounts WHERE email LIKE ${`%@${suffix}.gho.test`}`;
  await owner.end();
});

async function connect(email: string, organizationId: string) {
  const res = await router.handle(new Request(`${ORIGIN}/v1/me/github/connect`, { method: "POST",
    headers: { ...cookie(await sign(signer, { email })), origin: ORIGIN, "x-dude-organization": organizationId } }));
  expect(res.status).toBe(200);
  return new URL(((await res.json()) as { url: string }).url).searchParams.get("state")!;
}

// GitHub's redirect: a top-level navigation from github.com, with the Access cookie and nothing else of dude's.
async function returnFromGithub(email: string, state: string) {
  return router.handle(new Request(`${ORIGIN}/v1/me/github/callback?state=${state}&code=code`,
    { headers: { ...cookie(await sign(signer, { email })), "sec-fetch-site": "cross-site", "sec-fetch-mode": "navigate" } }));
}

async function status(email: string, organizationId: string) {
  const res = await router.handle(new Request(`${ORIGIN}/v1/me/github`,
    { headers: { ...cookie(await sign(signer, { email })), "x-dude-organization": organizationId } }));
  return (await res.json() as { connection: { login: string } | null }).connection;
}

test("a callback with no organisation completes in the one Connect named, and lands on Your settings there", async () => {
  const state = await connect(at("two"), northwind);
  const landed = await returnFromGithub(at("two"), state);
  expect(landed.status).toBe(303);
  expect(landed.headers.get("location")).toBe(`/?github=connected#/o/northwind-${suffix}/me/settings`);
  expect(await status(at("two"), northwind)).toMatchObject({ login: "OctoNorth" });
  expect(await status(at("two"), acme)).toBeNull();
  const stored = await owner`SELECT organization_id, person_id FROM github_user_authorizations WHERE organization_id IN (${acme}, ${northwind})`;
  expect([...stored]).toEqual([{ organization_id: northwind, person_id: northwindPerson }]);
  expect(acmePerson).not.toBe(northwindPerson);
});

test("someone else's state is consumed by no one and lands on the bare settings address", async () => {
  const state = await connect(at("other"), northwind);
  const landed = await returnFromGithub(at("two"), state);
  expect(landed.headers.get("location")).toBe("/?github=invalid_or_expired#/me/settings");
  // Still the other person's to complete.
  expect((await returnFromGithub(at("other"), state)).headers.get("location")).toBe(`/?github=connected#/o/northwind-${suffix}/me/settings`);
});

test("only GitHub's return is let in cross-site: a cross-site read of the status is still refused", async () => {
  const res = await router.handle(new Request(`${ORIGIN}/v1/me/github`, { headers: { ...cookie(await sign(signer, { email: at("two") })),
    "x-dude-organization": northwind, "sec-fetch-site": "cross-site" } }));
  expect(res.status).toBe(403);
  const connectCrossSite = await router.handle(new Request(`${ORIGIN}/v1/me/github/connect`, { method: "POST",
    headers: { ...cookie(await sign(signer, { email: at("two") })), origin: "https://evil.example", "x-dude-organization": northwind } }));
  expect(connectCrossSite.status).toBe(403);
});

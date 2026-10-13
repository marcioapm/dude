import { afterAll, beforeAll, expect, test, spyOn } from "bun:test";
import { SQL } from "bun";
import { createHash } from "node:crypto";
import { closePool, setPool } from "../src/db/client.ts";
import { migrate } from "../src/db/migrate.ts";
import { Config, useConfig } from "../src/config.ts";
import { Router } from "../src/api/router.ts";
import { registerGithubOAuthRoutes } from "../src/api/routes/githubOAuth.ts";

const url = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const name = `dude_owner_oauth_${Bun.randomUUIDv7("hex").slice(-12)}`;
let admin: SQL, owner: SQL, app: SQL, router: Router;
let person = "per_owner", org = "org_owner";
let denied = false, exchanged = 0;
let exchangeHook: ((body: Record<string, string>) => Promise<Response>) | null = null;
function database(appRole = false) {
  const u = new URL(url); u.pathname = `/${name}`;
  if (appRole) { u.username = "dude_app"; u.password = "dude_app"; }
  return u.toString();
}
const fakeFetch = (async (input: string | URL | Request, init?: RequestInit) => {
  const target = String(input);
  if (target === "https://github.com/login/oauth/access_token") {
    exchanged++;
    const body = JSON.parse(String(init?.body));
    expect(body.client_id).toBe("client"); expect(body.client_secret).toBe("hidden-secret");
    expect(body.code_verifier).toHaveLength(43); expect(body.redirect_uri).toBe("https://dude.example/v1/me/github/callback");
    if (exchangeHook) return exchangeHook(body);
    return Response.json({ access_token: "hidden-access", refresh_token: "hidden-refresh", expires_in: 28800, refresh_token_expires_in: 15552000 });
  }
  if (init?.method === "DELETE") {
    expect(target).toBe("https://api.github.com/applications/client/token");
    return new Response(null, { status: 204 });
  }
  expect(target).toBe("https://api.github.com/user");
  expect((init?.headers as Record<string,string>).authorization).toBe("Bearer hidden-access");
  return denied ? Response.json({}, { status: 401 }) : Response.json({ id: 123, login: "CanonicalOwner" });
}) as typeof fetch;

beforeAll(async () => {
  admin = new SQL(url); await admin.unsafe(`CREATE DATABASE "${name}"`);
  await migrate(database(), { log() {} }); owner = new SQL(database()); app = new SQL(database(true)); setPool(app);
  await owner`INSERT INTO organizations (id,name,slug) VALUES ('org_owner','Owner','owner'),('org_other','Other','other')`;
  await owner`INSERT INTO people (id,organization_id,name,email,commit_email) VALUES
    ('per_owner','org_owner','Owner','owner@example.com','explicit@example.com'),
    ('per_other','org_owner','Other','other@example.com',NULL),('per_otherorg','org_other','Other org','otherorg@example.com',NULL)`;
  await owner`INSERT INTO forge_credentials (id,organization_id,forge,auth,secret,app_id,oauth_client_id,oauth_client_secret)
    VALUES ('fc_owner','org_owner','github','github_app','pem','1','client','hidden-secret')`;
  useConfig(Config.load({ env: { ...process.env, DUDE_AUTH_PROVIDER: "cloudflare_access", DUDE_AUTH_PUBLIC_URL: "https://dude.example",
    DUDE_AUTH_DEFAULT_ORGANIZATION: "owner", DUDE_AUTH_CLOUDFLARE_ACCESS_TEAM: "test", DUDE_AUTH_CLOUDFLARE_ACCESS_AUD: "aud" } }));
  router = new Router(async () => ({ credentialKind: "person", organizationId: org, personId: person, kind: "user", name: "Owner", role: "admin", resolved: true }));
  registerGithubOAuthRoutes(router, fakeFetch);
});
afterAll(async () => { useConfig(null); await closePool(app); await owner?.end(); await admin?.unsafe(`DROP DATABASE IF EXISTS "${name}" WITH (FORCE)`); await admin?.end(); });
const request = (path: string, method = "GET") => router.handle(new Request(`https://dude.example${path}`, { method }));
async function start() {
  const res = await request("/v1/me/github/connect", "POST"); expect(res.status).toBe(200);
  const authorize = new URL(((await res.json()) as { url: string }).url);
  expect(authorize.origin).toBe("https://github.com"); expect(authorize.searchParams.get("scope")).toBe("offline_access");
  expect(authorize.searchParams.get("code_challenge_method")).toBe("S256");
  const state = authorize.searchParams.get("state")!;
  const [stored] = await owner`SELECT verifier FROM github_oauth_transactions WHERE state_hash = ${createHash("sha256").update(state).digest("hex")}`;
  expect(authorize.searchParams.get("code_challenge")).toBe(createHash("sha256").update(stored!.verifier).digest("base64url"));
  return state;
}
const callback = (state: string, extra = "code=code") => request(`/v1/me/github/callback?state=${state}&${extra}`);

test("state is person/org-bound, single-use, and verifies identity without exposing secrets", async () => {
  const state = await start();
  expect((await callback("wrong")).headers.get("location")).toContain("invalid_or_expired");
  person = "per_other"; expect((await callback(state)).headers.get("location")).toContain("invalid_or_expired");
  person = "per_otherorg"; org = "org_other"; expect((await callback(state)).headers.get("location")).toContain("invalid_or_expired");
  person = "per_owner"; org = "org_owner";
  expect((await callback(state)).headers.get("location")).toBe("/?github=connected#/me/settings");
  const [profile] = await owner`SELECT github_login,github_user_id::int AS id,commit_email::text FROM people WHERE id='per_owner'`;
  expect(profile).toEqual({github_login:"CanonicalOwner",id:123,commit_email:"explicit@example.com"});
  const status = await request("/v1/me/github"); const text = await status.text();
  const shown = JSON.parse(text) as { available: boolean; connection: Record<string, unknown> };
  expect(Object.keys(shown).sort()).toEqual(["available", "connection"]);
  expect(Object.keys(shown.connection).sort()).toEqual(["expiresAt", "login", "reconnectRequired", "refreshExpiresAt"]);
  expect(text).toContain("CanonicalOwner"); for (const secret of ["hidden-secret","hidden-access","hidden-refresh"]) expect(text).not.toContain(secret);
  const before = exchanged; expect((await callback(state)).headers.get("location")).toContain("invalid_or_expired"); expect(exchanged).toBe(before);
});
test("expired, denied, failed identity and replaced credentials do not store a connection", async () => {
  await request("/v1/me/github","DELETE");
  const expired = await start(); await owner`UPDATE github_oauth_transactions SET expires_at=now()-interval '1 second'`;
  expect((await callback(expired)).headers.get("location")).toContain("invalid_or_expired");
  const rejected = await start(); expect((await callback(rejected,"error=access_denied")).headers.get("location")).toContain("authorization_denied");
  expect((await callback(rejected)).headers.get("location")).toContain("invalid_or_expired");
  const identity = await start(); denied=true; expect((await callback(identity)).headers.get("location")).toContain("identity_failed"); denied=false;
  const replaced=await start(); await owner`UPDATE forge_credentials SET app_id='2' WHERE id='fc_owner'`;
  expect((await callback(replaced)).headers.get("location")).toContain("invalid_or_expired");
  const [{count}] = await owner`SELECT count(*)::int AS count FROM github_user_authorizations`; expect(count).toBe(0);
});
test("disconnect removes connections and outstanding transactions immediately",async()=>{
  const state=await start(); expect((await callback(state)).headers.get("location")).toContain("connected");
  const pending=await start(); expect((await request("/v1/me/github","DELETE")).status).toBe(204);
  expect((await callback(pending)).headers.get("location")).toContain("invalid_or_expired");
});

test("disconnect fences an already consumed callback even without a stored authorization", async () => {
  await request("/v1/me/github", "DELETE");
  const state = await start();
  let entered!: () => void, release!: () => void;
  const held = new Promise<void>((resolve) => { entered = resolve; });
  const gate = new Promise<void>((resolve) => { release = resolve; });
  exchangeHook = async () => { entered(); await gate; return Response.json({ access_token: "hidden-access", refresh_token: "hidden-refresh", expires_in: 28800, refresh_token_expires_in: 15552000 }); };
  try {
    const pending = callback(state); await held;
    expect((await request("/v1/me/github", "DELETE")).status).toBe(204);
    release(); expect((await pending).headers.get("location")).toContain("superseded");
    expect(await (await request("/v1/me/github")).json()).toEqual({ available: true, connection: null });
  } finally { release(); exchangeHook = null; }
});

test("latest initiated Connect wins when the older exchange finishes last", async () => {
  let entered!: () => void, release!: () => void;
  const held = new Promise<void>((resolve) => { entered = resolve; });
  const gate = new Promise<void>((resolve) => { release = resolve; });
  exchangeHook = async (body) => {
    if (body.code === "older") { entered(); await gate; }
    return Response.json({ access_token: "hidden-access", refresh_token: body.code, expires_in: 28800, refresh_token_expires_in: 15552000 });
  };
  try {
    const a = await start(); const pending = callback(a, "code=older"); await held;
    const b = await start(); expect((await callback(b, "code=newer")).headers.get("location")).toContain("connected");
    release(); expect((await pending).headers.get("location")).toContain("superseded");
    const [stored] = await owner`SELECT refresh_token FROM github_user_authorizations WHERE person_id='per_owner'`;
    expect(stored.refresh_token).toBe("newer");
  } finally { release(); exchangeHook = null; }
});

test("callback authentication is not bypassed and unexpected middleware failures do not log the callback URL", async () => {
  const logs = spyOn(console, "error").mockImplementation(() => {});
  try {
    const middlewareFailure = new Router(async (_credential, req) => { throw new Error(`DB failed for ${req?.url}`); });
    registerGithubOAuthRoutes(middlewareFailure, fakeFetch);
    const deniedRouter = new Router(async () => null);
    registerGithubOAuthRoutes(deniedRouter, fakeFetch);
    const req = new Request("https://dude.example/v1/me/github/callback?state=secret-state&code=secret-code");
    expect((await deniedRouter.handle(req)).status).toBe(401);
    const outcome = await middlewareFailure.handle(req);
    expect(outcome.status).toBe(303);
    expect(outcome.headers.get("location")).toBe("/?github=authorization_failed#/me/settings");
    expect(logs.mock.calls).toEqual([]);
  } finally { logs.mockRestore(); }
});

test("callback consume DB errors and invalid grants return fixed outcomes without sensitive logging", async () => {
  const state = await start();
  const logs = spyOn(console, "error").mockImplementation(() => {});
  try {
    await owner`REVOKE DELETE ON github_oauth_transactions FROM dude_app`;
    const failed = await callback(state, "code=sensitive-code");
    expect(failed.status).toBe(303); expect(failed.headers.get("location")).toBe("/?github=authorization_failed#/me/settings");
    await owner`GRANT DELETE ON github_oauth_transactions TO dude_app`;
    exchangeHook = async () => Response.json({ error: "invalid_grant", error_description: "sensitive-code hidden-secret hidden-refresh" }, { status: 400 });
    expect((await callback(state, "code=sensitive-code")).headers.get("location")).toBe("/?github=authorization_failed#/me/settings");
    expect(logs.mock.calls).toEqual([]);
  } finally {
    await owner`GRANT DELETE ON github_oauth_transactions TO dude_app`;
    exchangeHook = null; logs.mockRestore();
  }
});

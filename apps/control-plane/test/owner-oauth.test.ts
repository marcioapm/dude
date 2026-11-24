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
const landed = (status: string) => `/?github=${status}#/me/settings`;

test("state is person/org-bound, single-use, and verifies identity without exposing secrets", async () => {
  const state = await start();
  expect((await callback("wrong")).headers.get("location")).toBe(landed("invalid_or_expired"));
  person = "per_other"; expect((await callback(state)).headers.get("location")).toBe(landed("invalid_or_expired"));
  person = "per_otherorg"; org = "org_other"; expect((await callback(state)).headers.get("location")).toBe(landed("invalid_or_expired"));
  person = "per_owner"; org = "org_owner";
  expect((await callback(state)).headers.get("location")).toBe("/?github=connected#/me/settings");
  const [profile] = await owner`SELECT github_login,github_user_id::int AS id,commit_email::text FROM people WHERE id='per_owner'`;
  expect(profile).toEqual({github_login:"CanonicalOwner",id:123,commit_email:"explicit@example.com"});
  const status = await request("/v1/me/github"); const text = await status.text();
  const shown = JSON.parse(text) as { available: boolean; connection: Record<string, unknown> };
  expect(Object.keys(shown).sort()).toEqual(["available", "connection"]);
  expect(Object.keys(shown.connection).sort()).toEqual(["expiresAt", "login", "reconnectRequired", "refreshExpiresAt"]);
  expect(text).toContain("CanonicalOwner"); for (const secret of ["hidden-secret","hidden-access","hidden-refresh"]) expect(text).not.toContain(secret);
  const before = exchanged; expect((await callback(state)).headers.get("location")).toBe(landed("invalid_or_expired")); expect(exchanged).toBe(before);
});
test("expired, denied, failed identity and replaced credentials do not store a connection", async () => {
  await request("/v1/me/github","DELETE");
  const expired = await start(); await owner`UPDATE github_oauth_transactions SET expires_at=now()-interval '1 second'`;
  expect((await callback(expired)).headers.get("location")).toBe(landed("invalid_or_expired"));
  const rejected = await start(); expect((await callback(rejected,"error=access_denied")).headers.get("location")).toBe(landed("authorization_denied"));
  expect((await callback(rejected)).headers.get("location")).toBe(landed("invalid_or_expired"));
  const identity = await start(); denied=true; expect((await callback(identity)).headers.get("location")).toBe(landed("identity_failed")); denied=false;
  exchangeHook = async () => Response.json({ access_token: "hidden-access", token_type: "bearer", scope: "" });
  try { expect((await callback(await start())).headers.get("location")).toBe(landed("enable_expiring_tokens")); }
  finally { exchangeHook = null; }
  const replaced=await start(); await owner`UPDATE forge_credentials SET app_id='2' WHERE id='fc_owner'`;
  expect((await callback(replaced)).headers.get("location")).toBe(landed("invalid_or_expired"));
  const [{count}] = await owner`SELECT count(*)::int AS count FROM github_user_authorizations`; expect(count).toBe(0);
});
test("disconnect removes connections and outstanding transactions immediately",async()=>{
  const state=await start(); expect((await callback(state)).headers.get("location")).toBe(landed("connected"));
  const pending=await start(); expect((await request("/v1/me/github","DELETE")).status).toBe(204);
  expect((await callback(pending)).headers.get("location")).toBe(landed("invalid_or_expired"));
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
    release(); expect((await pending).headers.get("location")).toBe(landed("superseded"));
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
    const b = await start(); expect((await callback(b, "code=newer")).headers.get("location")).toBe(landed("connected"));
    release(); expect((await pending).headers.get("location")).toBe(landed("superseded"));
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

test("callback signals only live publications of the person's current frozen branch, parked or about to park", async () => {
  await owner`INSERT INTO projects (id,organization_id,name,slug,key_prefix) VALUES ('prj_wake','org_owner','Wake','wake','WAKE'),
    ('prj_wake_other','org_other','Wake','wake','WAKE')`;
  await owner`INSERT INTO tasks (id,organization_id,project_id,title,number) VALUES
    ('tsk_current','org_owner','prj_wake','Current',1),('tsk_other','org_owner','prj_wake','Other',2),
    ('tsk_racing','org_owner','prj_wake','Racing',3),('tsk_implementing','org_owner','prj_wake','Implementing',4),
    ('tsk_done','org_owner','prj_wake','Done',5),('tsk_held','org_owner','prj_wake','Held',6),
    ('tsk_foreign','org_other','prj_wake_other','Foreign',1)`;
  await owner`INSERT INTO pr_publications (organization_id,task_id,branch,owner_person_id) VALUES
    ('org_owner','tsk_current','current','per_owner'),('org_owner','tsk_other','historical','per_owner'),
    ('org_owner','tsk_other','current','per_other'),('org_owner','tsk_racing','current','per_owner'),
    ('org_owner','tsk_implementing','current','per_owner'),('org_owner','tsk_done','current','per_owner'),
    ('org_owner','tsk_held','current','per_owner'),('org_other','tsk_foreign','current','per_owner')`;
  await owner`INSERT INTO workflow_runs (id,organization_id,workflow_type,idempotency_key,status,step,state,awaiting_signals,task_id) VALUES
    ('wfr_current','org_owner','task.delivery','current','waiting','openPullRequest','{"branch":"current"}','["github.authorized"]','tsk_current'),
    ('wfr_other','org_owner','task.delivery','other','waiting','openPullRequest','{"branch":"current"}','["github.authorized"]','tsk_other'),
    ('wfr_racing','org_owner','task.delivery','racing','running','awaitPRFix','{"branch":"current"}','[]','tsk_racing'),
    ('wfr_implementing','org_owner','task.delivery','implementing','running','implement','{"branch":"current"}','[]','tsk_implementing'),
    ('wfr_done','org_owner','task.delivery','done','completed','openPullRequest','{"branch":"current"}','[]','tsk_done'),
    ('wfr_held','org_owner','task.delivery','held','waiting','openPullRequest','{"branch":"current"}','["github.authorized"]','tsk_held'),
    ('wfr_foreign','org_other','task.delivery','foreign','waiting','openPullRequest','{"branch":"current"}','["github.authorized"]','tsk_foreign')`;
  // A run held by a Wait gets its signal but is not woken before wait_until.
  await owner`UPDATE workflow_runs SET wait_until = now() + interval '1 hour' WHERE id='wfr_held'`;
  expect((await callback(await start())).headers.get("location")).toBe(landed("connected"));
  expect([...await owner`SELECT id,status::text AS status FROM workflow_runs ORDER BY id`]).toEqual([
    { id: "wfr_current", status: "running" }, { id: "wfr_done", status: "completed" }, { id: "wfr_foreign", status: "waiting" },
    { id: "wfr_held", status: "waiting" }, { id: "wfr_implementing", status: "running" }, { id: "wfr_other", status: "waiting" },
    { id: "wfr_racing", status: "running" }]);
  expect([...await owner`SELECT workflow_run_id AS run,name,payload,consumed_at FROM workflow_signals ORDER BY workflow_run_id`]).toEqual([
    { run: "wfr_current", name: "github.authorized", payload: { personId: "per_owner" }, consumed_at: null },
    { run: "wfr_held", name: "github.authorized", payload: { personId: "per_owner" }, consumed_at: null },
    { run: "wfr_racing", name: "github.authorized", payload: { personId: "per_owner" }, consumed_at: null }]);
});

test("githubConnected is a live authorization at the App's version: connected, never connected, reconnect required", async () => {
  const { registerPeopleRoutes } = await import("../src/api/routes/people.ts");
  const people = new Router(async () => ({ credentialKind: "person", organizationId: "org_owner", personId: "per_owner", kind: "user", name: "Owner", role: "admin", resolved: true }));
  registerPeopleRoutes(people);
  await owner`INSERT INTO people (id,organization_id,name,email) VALUES ('per_never','org_owner','Never','never@example.com'),
    ('per_reconnect','org_owner','Reconnect','reconnect@example.com'),('per_lapsed','org_owner','Lapsed','lapsed@example.com')`;
  await owner`DELETE FROM github_user_authorizations WHERE organization_id = 'org_owner'`;
  await owner`INSERT INTO github_user_authorizations (organization_id,person_id,credential_id,credential_version,github_user_id,github_login,
      access_token,refresh_token,expires_at,refresh_expires_at,reconnect_required)
    SELECT 'org_owner', p, 'fc_owner', c.oauth_version, n, p, 'a', 'r', now() + interval '1 hour', now() + r, x
    FROM forge_credentials c, (VALUES ('per_owner', 1, interval '1 day', false), ('per_reconnect', 2, interval '1 day', true),
      ('per_lapsed', 3, interval '-1 minute', false)) v(p, n, r, x) WHERE c.id = 'fc_owner'`;
  const listed = await (await people.handle(new Request("https://dude.example/v1/people"))).json() as { people: Array<{ id: string; githubConnected: boolean }> };
  const connected = Object.fromEntries(listed.people.map((p) => [p.id, p.githubConnected]));
  expect([connected.per_owner, connected.per_never, connected.per_reconnect, connected.per_lapsed]).toEqual([true, false, false, false]);
  // A replaced App (a new OAuth version) is no longer a connection.
  await owner`UPDATE forge_credentials SET oauth_version = oauth_version + 1 WHERE id = 'fc_owner'`;
  const after = await (await people.handle(new Request("https://dude.example/v1/people"))).json() as { people: Array<{ id: string; githubConnected: boolean }> };
  expect(after.people.find((p) => p.id === "per_owner")?.githubConnected).toBe(false);
});

test("a task's credentialWait shows while its delivery waits on the frozen owner, and clears as the step goes on", async () => {
  const { credentialWaitJson } = await import("../src/api/routes/work.ts");
  await owner`INSERT INTO projects (id,organization_id,name,slug,key_prefix) VALUES ('prj_cw','org_owner','CW','cw','CW')`;
  await owner`INSERT INTO tasks (id,organization_id,project_id,title,number,status) VALUES ('tsk_cw','org_owner','prj_cw','Waits',1,'awaiting_input')`;
  await owner`INSERT INTO pr_publications (organization_id,task_id,branch,owner_person_id) VALUES ('org_owner','tsk_cw','dude/cw-1-1','per_owner')`;
  await owner`INSERT INTO workflow_runs (id,organization_id,workflow_type,idempotency_key,status,step,state,awaiting_signals,task_id,created_at) VALUES
    ('wfr_cw','org_owner','task.delivery','cw','waiting','openPullRequest','{"branch":"dude/cw-1-1"}','["github.authorized"]','tsk_cw', now() - interval '1 hour')`;
  await owner`INSERT INTO events (id,organization_id,event_type,project_id,task_id,actor_type,actor_id,source,payload) VALUES
    ('evt_cw','org_owner','question.asked','prj_cw','tsk_cw','system','workflow','control-plane',
     ${{ kind: "credential", reason: "owner_github", personId: "per_owner", branch: "dude/cw-1-1", reconnect: false, key: "wfr_cw:per_owner:connect" }}::jsonb)`;
  const read = async () => (await owner.unsafe(`SELECT ${credentialWaitJson()} FROM tasks WHERE id = 'tsk_cw'`))[0].credentialWait;
  const shown = await read();
  expect({ ...shown, askedAt: undefined, person: shown.person.id }).toEqual({ reason: "owner_github", person: "per_owner", branch: "dude/cw-1-1",
    reconnect: false, askedAt: undefined });
  // GitHub refused the token she connected: the latest ask, to reconnect, is the one shown, since the first ask.
  await owner`INSERT INTO events (id,organization_id,event_type,project_id,task_id,actor_type,actor_id,source,payload) VALUES
    ('evt_cw_again','org_owner','question.asked','prj_cw','tsk_cw','system','workflow','control-plane',
     ${{ kind: "credential", reason: "owner_github", personId: "per_owner", branch: "dude/cw-1-1", reconnect: true, key: "wfr_cw:per_owner:reconnect" }}::jsonb)`;
  const again = await read();
  expect(again.reconnect).toBe(true);
  expect(again.askedAt).not.toBe(shown.askedAt);
  // Passed to someone else: the wait is no longer the frozen owner's.
  await owner`UPDATE pr_publications SET owner_person_id = 'per_other' WHERE task_id = 'tsk_cw'`;
  expect(await read()).toBeNull();
  await owner`UPDATE pr_publications SET owner_person_id = 'per_owner' WHERE task_id = 'tsk_cw'`;
  expect(await read()).not.toBeNull();
  // The step went on (connected): nothing waits.
  await owner`UPDATE workflow_runs SET status = 'running', awaiting_signals = '[]' WHERE id = 'wfr_cw'`;
  expect(await read()).toBeNull();
});

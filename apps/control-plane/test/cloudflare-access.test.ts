import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { SignJWT, exportJWK, generateKeyPair, type JWK } from "jose";
import { createApiKey, requestAuthenticator, type Principal } from "../src/api/auth.ts";
import {
  accessAuthenticator, accessProfiles, accessVerifier, type AccessDeps, type FetchLike, type Profile,
} from "../src/api/cloudflareAccess.ts";
import { Router } from "../src/api/router.ts";
import { registerPeopleRoutes } from "../src/api/routes/people.ts";
import { registerEventRoutes } from "../src/api/routes/events.ts";
import { type AccessConfig, ConfigError, parseConfig, readConfig } from "../src/config.ts";
import { authFor } from "../src/index.ts";
import { closePool, setPool } from "../src/db/client.ts";

const suffix = Bun.randomUUIDv7("hex").slice(-12);
const org = `org_access_${suffix}`;
const other = `${org}_other`;
const TEAM = "dudetest";
const ISSUER = `https://${TEAM}.cloudflareaccess.com`;
const AUD = "app-audience";
const ORIGIN = "https://dude.example.test";

let owner: SQL;
let app: SQL;

type Key = { privateKey: CryptoKey; jwk: JWK };
async function rsaKey(kid: string): Promise<Key> {
  const { privateKey, publicKey } = await generateKeyPair("RS256", { extractable: true });
  return { privateKey, jwk: { ...(await exportJWK(publicKey)), kid, alg: "RS256", use: "sig" } };
}

/** Stands in for the team's certs and identity endpoints; nothing leaves the process. */
class Edge {
  keys: JWK[] = [];
  certsDown = false;
  certsCalls = 0;
  profiles = new Map<string, unknown>();
  profileCalls = 0;
  profileStatus = 200;
  // While set, identity responses wait for it; `profileStarted` fires on each request.
  profileGate: Promise<void> | null = null;
  profileStarted: () => void = () => {};
  urls: string[] = [];
  fetch: FetchLike = async (input, init) => {
    this.urls.push(String(input));
    if (String(input) === `${ISSUER}/cdn-cgi/access/certs`) {
      this.certsCalls++;
      if (this.certsDown) throw new Error("connection refused");
      return Response.json({ keys: this.keys });
    }
    if (String(input) === `${ISSUER}/cdn-cgi/access/get-identity`) {
      this.profileCalls++;
      this.profileStarted();
      if (this.profileGate) await this.profileGate;
      const token = /CF_Authorization=([^;]+)/.exec(new Headers(init?.headers).get("cookie") ?? "")?.[1] ?? "";
      const email = JSON.parse(atob(token.split(".")[1]!.replace(/-/g, "+").replace(/_/g, "/"))).email;
      return Response.json(this.profiles.get(email.toLowerCase()) ?? { email }, { status: this.profileStatus });
    }
    return new Response("not here", { status: 404 });
  };
}

async function sign(key: Key, claims: Record<string, unknown>, opts: {
  iss?: string; aud?: string; exp?: number | null; nbf?: number; alg?: string;
} = {}) {
  const now = Math.floor(Date.now() / 1000);
  let jwt = new SignJWT({ type: "app", sub: `sub-${claims.email ?? "svc"}`, ...claims })
    .setProtectedHeader({ alg: opts.alg ?? "RS256", kid: key.jwk.kid! })
    .setIssuer(opts.iss ?? ISSUER).setAudience(opts.aud ?? AUD).setIssuedAt(now);
  if (opts.exp !== null) jwt = jwt.setExpirationTime(opts.exp ?? now + 600);
  if (opts.nbf !== undefined) jwt = jwt.setNotBefore(opts.nbf);
  return jwt.sign(key.privateKey);
}

const config = (over: Partial<AccessConfig> = {}): AccessConfig => ({
  provider: "cloudflare_access", public_url: ORIGIN, auto_create: true, default_organization: org,
  cloudflare_access: { team: TEAM, aud: AUD }, ...over,
});

function routerFor(edge: Edge, over: Partial<AccessConfig> = {}, organizationId = org,
  wrapProfile: (profile: AccessDeps["profile"]) => AccessDeps["profile"] = (p) => p) {
  const router = new Router(requestAuthenticator(accessAuthenticator(config(over), organizationId, {
    verify: accessVerifier(TEAM, AUD, edge.fetch),
    profile: wrapProfile(accessProfiles(TEAM, edge.fetch)),
  })));
  registerPeopleRoutes(router);
  registerEventRoutes(router);
  return router;
}

const cookie = (token: string) => ({ cookie: `other=1; CF_Authorization=${token}` });
const me = async (router: Router, headers: Record<string, string>) => {
  const res = await router.handle(new Request(`${ORIGIN}/v1/me`, { headers }));
  return { status: res.status, body: res.status === 200 ? await res.json() as {
    person: { id: string; name: string; role: string; photoUrl: string | null };
    authMethod: string; logoutUrl?: string; } : null };
};
type PersonRow = { id: string; name: string; role: string; photo_url: string | null; removed_at: Date | null };
const peopleByEmail = async (email: string, organizationId = org): Promise<PersonRow[]> =>
  (await owner`SELECT id, name, role, photo_url, removed_at FROM people WHERE organization_id = ${organizationId} AND email = ${email}`) as PersonRow[];
const keysOf = (personId: string) => owner`SELECT id FROM api_keys WHERE person_id = ${personId}`;

// Polls `ready` every 20 ms, failing after `ms`: for states only the database can report.
async function waitFor(ready: () => Promise<boolean>, ms = 5_000) {
  const until = Date.now() + ms;
  while (!(await ready())) {
    if (Date.now() > until) throw new Error("condition not reached");
    await Bun.sleep(20);
  }
}

let signer: Key;
let rotated: Key;
let stranger: Key;

beforeAll(async () => {
  owner = new SQL(process.env.DATABASE_URL!);
  const url = new URL(process.env.DATABASE_URL!);
  url.username = "dude_app";
  url.password = "dude_app";
  app = new SQL(url.toString());
  setPool(app);
  for (const id of [org, other]) await owner`INSERT INTO organizations (id, name, slug) VALUES (${id}, ${id}, ${id})`;
  [signer, rotated, stranger] = await Promise.all([rsaKey("k1"), rsaKey("k2"), rsaKey("k1")]);
});
afterAll(async () => {
  await closePool(app);
  await owner`DELETE FROM organizations WHERE id IN (${org}, ${other})`;
  await owner.end();
});

describe("configuration", () => {
  const valid = `
[auth]
provider = "cloudflare_access"
public_url = "https://dude.absmartly.dev"
default_organization = "absmartly"
[auth.cloudflare_access]
team = "absmartly"
aud = "application-audience"
`;
  test("no file keeps API keys; a valid file enables Access with auto_create by default", async () => {
    expect(await readConfig(undefined)).toEqual({ auth: { provider: "api_key" } });
    expect(parseConfig("").auth).toEqual({ provider: "api_key" });
    expect(parseConfig("[auth]\n").auth).toEqual({ provider: "api_key" });
    expect(parseConfig(valid).auth).toEqual({
      provider: "cloudflare_access", public_url: "https://dude.absmartly.dev", auto_create: true,
      default_organization: "absmartly", cloudflare_access: { team: "absmartly", aud: "application-audience" },
    });
    expect(await readConfig(`${import.meta.dir}/../../../docs/dude.example.toml`)).toEqual(parseConfig(valid));
  });

  test.each([
    ["malformed TOML", `[auth]\nprovider = "api_key`],
    ["duplicate key", `[auth]\nprovider = "api_key"\nprovider = "api_key"`],
    ["unknown provider", `[auth]\nprovider = "google"`],
    ["missing organization", valid.replace(/default_organization.*\n/, "")],
    ["missing public_url", valid.replace(/public_url.*\n/, "")],
    ["missing audience", valid.replace(/aud =.*\n/, "")],
    ["team as a URL", valid.replace(`team = "absmartly"`, `team = "https://evil.example"`)],
    ["plain http origin", valid.replace("https://dude", "http://dude")],
    ["origin with a path", valid.replace(".dev\"", ".dev/app\"")],
    ["wrong type", valid.replace(`default_organization = "absmartly"`, "default_organization = \"absmartly\"\nauto_create = \"yes\"")],
    ["unknown key", valid + "extra = 1\n"],
    ["unknown table", valid + "[other]\nx = 1\n"],
  ])("%s fails", (_, text) => {
    expect(() => parseConfig(text)).toThrow(ConfigError);
  });

  test("an unreadable file and an organization that does not exist fail startup", async () => {
    await expect(readConfig("/nonexistent/dude.toml")).rejects.toThrow(ConfigError);
    const before = await owner`SELECT count(*)::int AS n FROM organizations`;
    await expect(authFor(config({ default_organization: `missing_${suffix}` }))).rejects.toThrow(ConfigError);
    expect(await owner`SELECT count(*)::int AS n FROM organizations`).toEqual(before);
    expect(typeof await authFor(config())).toBe("function");
  });
});

describe("token verification", () => {
  test("accepts this team's signed, current human token and refuses every other kind", async () => {
    const edge = new Edge();
    edge.keys = [signer.jwk];
    const verify = accessVerifier(TEAM, AUD, edge.fetch);
    const now = Math.floor(Date.now() / 1000);
    const email = "Person@Example.com";
    expect(await verify(await sign(signer, { email }))).toEqual({ email: "person@example.com", expiresAt: now + 600 });

    const refused = {
      "other issuer": await sign(signer, { email }, { iss: "https://evil.cloudflareaccess.com" }),
      "other audience": await sign(signer, { email }, { aud: "other" }),
      expired: await sign(signer, { email }, { exp: now - 120 }),
      "no expiry": await sign(signer, { email }, { exp: null }),
      "not yet valid": await sign(signer, { email }, { nbf: now + 120 }),
      "unknown key": await sign(stranger, { email }),
      "service token": await sign(signer, { common_name: "client.access", email: "" }),
      "service token with an email": await sign(signer, { common_name: "client.access", email }),
      "no email": await sign(signer, {}),
      "not an email": await sign(signer, { email: "nobody" }),
      "org token type": await sign(signer, { email, type: "org" }),
      unsigned: `${btoa(JSON.stringify({ alg: "none" }))}.${btoa(JSON.stringify({ email, iss: ISSUER, aud: AUD, exp: now + 600 }))}.`,
      garbage: "not-a-jwt",
    };
    for (const [why, token] of Object.entries(refused)) {
      expect({ why, got: await verify(token) }).toEqual({ why, got: null });
    }
    // Within the clock tolerance.
    expect(await verify(await sign(signer, { email }, { exp: now - 5 }))).not.toBeNull();
    expect(edge.urls.every((u) => u === `${ISSUER}/cdn-cgi/access/certs`)).toBe(true);
  });

  test("refuses an HS256 token signed with the public key as secret", async () => {
    const edge = new Edge();
    edge.keys = [signer.jwk];
    const secret = new TextEncoder().encode(JSON.stringify(signer.jwk));
    const forged = await new SignJWT({ email: "a@example.com", sub: "x", type: "app" })
      .setProtectedHeader({ alg: "HS256", kid: "k1" }).setIssuer(ISSUER).setAudience(AUD).setExpirationTime("10m")
      .sign(secret);
    expect(await accessVerifier(TEAM, AUD, edge.fetch)(forged)).toBeNull();
  });

  test("a rotated key is fetched once the cooldown allows; an unreachable key set refuses", async () => {
    const edge = new Edge();
    edge.keys = [signer.jwk];
    const verify = accessVerifier(TEAM, AUD, edge.fetch, 50);
    expect(await verify(await sign(signer, { email: "a@example.com" }))).not.toBeNull();
    edge.keys = [rotated.jwk];
    await Bun.sleep(60);
    expect(await verify(await sign(rotated, { email: "a@example.com" }))).not.toBeNull();
    // The retired key is gone from the set now fetched.
    await Bun.sleep(60);
    expect(await verify(await sign(signer, { email: "a@example.com" }))).toBeNull();

    const down = new Edge();
    down.certsDown = true;
    expect(await accessVerifier(TEAM, AUD, down.fetch)(await sign(signer, { email: "a@example.com" }))).toBeNull();
  });
});

describe("profiles", () => {
  test("uses a matching profile, keeps only https pictures, and ignores someone else's", async () => {
    const edge = new Edge();
    const profiles = accessProfiles(TEAM, edge.fetch);
    const token = await sign(signer, { email: "p@example.com" });
    edge.profiles.set("p@example.com", { email: "P@example.com", name: " Pat ", oidc_fields: { picture: "https://img.example/p.png" } });
    expect(await profiles(token, "p@example.com")).toEqual({ name: "Pat", picture: "https://img.example/p.png" });
    await profiles(token, "p@example.com");
    expect(edge.profileCalls).toBe(1); // cached

    const plain = await sign(signer, { email: "q@example.com" });
    edge.profiles.set("q@example.com", { email: "q@example.com", name: "Q", oidc_fields: { picture: "http://img.example/q.png" } });
    expect(await profiles(plain, "q@example.com")).toEqual({ name: "Q", picture: null });

    const spoof = await sign(signer, { email: "r@example.com" });
    edge.profiles.set("r@example.com", { email: "admin@example.com", name: "Admin" });
    expect(await profiles(spoof, "r@example.com")).toBeNull();

    const failing = new Edge();
    failing.profileStatus = 500;
    expect(await accessProfiles(TEAM, failing.fetch)(token, "p@example.com")).toBeNull();
    const hanging: FetchLike = (_, init) => new Promise((_, reject) =>
      init?.signal?.addEventListener("abort", () => reject(new Error("timeout"))));
    expect(await accessProfiles(TEAM, hanging)(token, "p@example.com")).toBeNull();
  }, 10_000);
});

describe("signing in", () => {
  const edge = new Edge();
  let router: Router;
  beforeAll(() => {
    edge.keys = [signer.jwk];
    router = routerFor(edge);
  });

  test("an unknown email becomes a member with no key, seeded from the profile", async () => {
    edge.profiles.set("new@example.com", { email: "new@example.com", name: "New Person", oidc_fields: { picture: "https://img.example/n.png" } });
    const got = await me(router, cookie(await sign(signer, { email: "New@Example.com" })));
    expect(got.status).toBe(200);
    expect(got.body!.person).toMatchObject({ name: "New Person", role: "member", photoUrl: "https://img.example/n.png" });
    expect(got.body!.authMethod).toBe("cloudflare_access");
    expect(got.body!.logoutUrl).toBe("/cdn-cgi/access/logout");
    expect(JSON.stringify(got.body)).not.toContain("dude_sk_");
    const rows = await peopleByEmail("new@example.com");
    expect(rows).toHaveLength(1);
    expect(await keysOf(rows[0]!.id)).toHaveLength(0);
    // Second request: the same person.
    const again = await me(router, { "cf-access-jwt-assertion": await sign(signer, { email: "new@example.com" }) });
    expect(again.body!.person.id).toBe(rows[0]!.id);
  });

  test("an existing admin keeps id, role and the name they set; an empty photo is filled", async () => {
    const id = `${org}_marcio`;
    await owner`INSERT INTO people (id, organization_id, name, email, role) VALUES (${id}, ${org}, 'Márcio', 'marcio@example.com', 'admin')`;
    edge.profiles.set("marcio@example.com", { email: "marcio@example.com", name: "Marcio IdP", oidc_fields: { picture: "https://img.example/m.png" } });
    const got = await me(router, cookie(await sign(signer, { email: "MARCIO@example.com" })));
    expect(got.body!.person).toMatchObject({ id, name: "Márcio", role: "admin", photoUrl: "https://img.example/m.png" });
    // A photo set since is not replaced.
    await owner`UPDATE people SET photo_url = 'https://mine.example/me.png' WHERE id = ${id}`;
    edge.profiles.set("marcio@example.com", { email: "marcio@example.com", name: "X", oidc_fields: { picture: "https://img.example/other.png" } });
    const later = await routerFor(edge).handle(new Request(`${ORIGIN}/v1/me`, { headers: cookie(await sign(signer, { email: "marcio@example.com" })) }));
    expect((await later.json() as { person: { photoUrl: string } }).person.photoUrl).toBe("https://mine.example/me.png");
    expect(await keysOf(id)).toHaveLength(0);
  });

  test("a failed profile lookup does not deny a member, and seeds the email as name", async () => {
    const failing = new Edge();
    failing.keys = [signer.jwk];
    failing.profileStatus = 503;
    const got = await me(routerFor(failing), cookie(await sign(signer, { email: "noprofile@example.com" })));
    expect(got.status).toBe(200);
    expect(got.body!.person.name).toBe("noprofile@example.com");
    // A later profile fills the name that was only the email.
    edge.profiles.set("noprofile@example.com", { email: "noprofile@example.com", name: "Now Named" });
    const named = await me(router, cookie(await sign(signer, { email: "noprofile@example.com" })));
    expect(named.body!.person.name).toBe("Now Named");
  });

  test("a removed person is refused and not recreated; auto_create off refuses strangers", async () => {
    const id = `${org}_gone`;
    await owner`INSERT INTO people (id, organization_id, name, email, removed_at) VALUES (${id}, ${org}, 'Gone', 'gone@example.com', now())`;
    expect((await me(router, cookie(await sign(signer, { email: "gone@example.com" })))).status).toBe(401);
    expect(await peopleByEmail("gone@example.com")).toHaveLength(1);

    const closed = routerFor(edge, { auto_create: false });
    expect((await me(closed, cookie(await sign(signer, { email: "stranger@example.com" })))).status).toBe(401);
    expect(await peopleByEmail("stranger@example.com")).toHaveLength(0);
  });

  test("removing someone ends their next request", async () => {
    const token = await sign(signer, { email: "leaving@example.com" });
    const first = await me(router, cookie(token));
    await owner`UPDATE people SET removed_at = now() WHERE id = ${first.body!.person.id}`;
    expect((await me(router, cookie(token))).status).toBe(401);
  });

  test("concurrent first sign-ins make one person", async () => {
    const tokens = await Promise.all(Array.from({ length: 6 }, () => sign(signer, { email: "race@example.com" })));
    const results = await Promise.all(tokens.map((t) => me(routerFor(edge), cookie(t))));
    expect(results.map((r) => r.status)).toEqual(Array(6).fill(200));
    expect(new Set(results.map((r) => r.body!.person.id)).size).toBe(1);
    expect(await peopleByEmail("race@example.com")).toHaveLength(1);
  });

  test("a slow profile holds no database transaction while another request completes", async () => {
    const slow = new Edge();
    slow.keys = [signer.jwk];
    const gate = Promise.withResolvers<void>();
    const bothAsked = Promise.withResolvers<void>();
    slow.profileGate = gate.promise;
    slow.profileStarted = () => { if (slow.profileCalls === 2) bothAsked.resolve(); };
    // One first sign-in (the creation path) and one person whose name is still their email (the fill path).
    await owner`INSERT INTO people (id, organization_id, name, email) VALUES (${`${org}_unnamed`}, ${org}, 'unnamed@example.com', 'unnamed@example.com')`;
    slow.profiles.set("unnamed@example.com", { email: "unnamed@example.com", name: "Named Later" });
    const r = routerFor(slow);
    const waiting = Promise.all(["slowfirst@example.com", "unnamed@example.com"]
      .map(async (email) => me(r, cookie(await sign(signer, { email })))));
    await bothAsked.promise;
    const [open] = await owner`
      SELECT count(*)::int AS n FROM pg_stat_activity
      WHERE datname = current_database() AND usename = 'dude_app' AND state LIKE 'idle in transaction%'`;
    expect(open.n).toBe(0);
    expect((await me(router, cookie(await sign(signer, { email: "meanwhile@example.com" })))).status).toBe(200);
    gate.resolve();
    const [first, filled] = await waiting;
    expect(first!.status).toBe(200);
    expect(filled!.body!.person.name).toBe("Named Later");
  });

  // The profile step is the window between the first read and the locked
  // recheck: hold the sign-in there while someone else writes the email.
  async function signInAround(email: string, meanwhile: () => Promise<void>) {
    const inProfile = Promise.withResolvers<void>();
    const release = Promise.withResolvers<void>();
    const r = routerFor(edge, {}, org, (profile) => async (token, e) => {
      inProfile.resolve();
      await release.promise;
      return profile(token, e);
    });
    const signing = me(r, cookie(await sign(signer, { email })));
    await inProfile.promise;
    await meanwhile();
    release.resolve();
    return signing;
  }
  const insertLocked = (id: string, email: string, removed: boolean) => owner.begin(async (tx) => {
    await tx`SELECT pg_advisory_xact_lock(hashtext('people:' || ${org}))`;
    await tx`INSERT INTO people (id, organization_id, name, email, removed_at)
      VALUES (${id}, ${org}, 'Meanwhile', ${email}, ${removed ? new Date() : null})`;
  });

  test("someone removed while the first sign-in fetched its profile is refused, not recreated", async () => {
    const got = await signInAround("tombstone@example.com",
      () => insertLocked(`${org}_tombstone`, "tombstone@example.com", true));
    expect(got.status).toBe(401);
    const rows = await peopleByEmail("tombstone@example.com");
    expect(rows.map((r) => r.id)).toEqual([`${org}_tombstone`]);
  });

  test("a member added while the first sign-in fetched its profile is reused", async () => {
    const got = await signInAround("addedmeanwhile@example.com",
      () => insertLocked(`${org}_addedmeanwhile`, "addedmeanwhile@example.com", false));
    expect(got.status).toBe(200);
    expect(got.body!.person).toMatchObject({ id: `${org}_addedmeanwhile`, name: "Meanwhile" });
    expect(await peopleByEmail("addedmeanwhile@example.com")).toHaveLength(1);
  });

  test("concurrent first requests for one email share one profile fetch", async () => {
    const slow = new Edge();
    slow.keys = [signer.jwk];
    slow.profiles.set("herd@example.com", { email: "herd@example.com", name: "Herd" });
    const gate = Promise.withResolvers<void>();
    slow.profileGate = gate.promise;
    const allAsked = Promise.withResolvers<void>();
    let asks = 0;
    const r = routerFor(slow, {}, org, (profile) => (token, email) => {
      if (++asks === 6) allAsked.resolve();
      return profile(token, email);
    });
    const tokens = await Promise.all(Array.from({ length: 6 }, () => sign(signer, { email: "herd@example.com" })));
    const results = Promise.all(tokens.map((t) => me(r, cookie(t))));
    await allAsked.promise;
    gate.resolve();
    const done = await results;
    expect(done.map((d) => d.status)).toEqual(Array(6).fill(200));
    expect(new Set(done.map((d) => d.body!.person.id)).size).toBe(1);
    expect(done[0]!.body!.person.name).toBe("Herd");
    expect(slow.profileCalls).toBe(1);
    expect(await peopleByEmail("herd@example.com")).toHaveLength(1);
  });

  test("organizations stay apart; a person with no email is never linked", async () => {
    const foreign = `${other}_same`;
    await owner`INSERT INTO people (id, organization_id, name, email, role) VALUES (${foreign}, ${other}, 'Other', 'shared@example.com', 'admin')`;
    const nullEmail = `${org}_noemail`;
    await owner`INSERT INTO people (id, organization_id, name, role) VALUES (${nullEmail}, ${org}, 'shared@example.com', 'admin')`;
    const got = await me(router, cookie(await sign(signer, { email: "shared@example.com" })));
    expect(got.body!.person.id).not.toBe(foreign);
    expect(got.body!.person.id).not.toBe(nullEmail);
    expect(got.body!.person.role).toBe("member");
    expect(await peopleByEmail("shared@example.com", other)).toHaveLength(1);
  });

  test("the email lookup sees only the transaction's own organization", async () => {
    await owner`INSERT INTO people (id, organization_id, name, email) VALUES (${`${other}_scoped`}, ${other}, 'Scoped', 'scoped@example.com')`;
    const lookup = (organizationId: string | null) => app.begin(async (tx) => {
      if (organizationId) await tx`SELECT set_config('app.organization_id', ${organizationId}, true)`;
      return (await tx`SELECT id FROM people_by_email('SCOPED@example.com')`).map((r: { id: string }) => r.id);
    });
    expect(await lookup(other)).toEqual([`${other}_scoped`]);
    expect(await lookup(org)).toEqual([]);
    expect(await lookup(null)).toEqual([]);
  });

  test("the header wins over the cookie, and nothing but a verified token identifies anyone", async () => {
    const a = await sign(signer, { email: "new@example.com" });
    const b = await sign(signer, { email: "marcio@example.com" });
    const got = await me(router, { "cf-access-jwt-assertion": a, ...cookie(b) });
    expect(got.body!.person.name).toBe("New Person");
    // An invalid header does not fall back to a valid cookie.
    expect((await me(router, { "cf-access-jwt-assertion": "bad", ...cookie(b) })).status).toBe(401);
    for (const headers of [
      { "cf-access-authenticated-user-email": "marcio@example.com" },
      { "x-dude-person": `${org}_marcio`, "x-dude-organization": org },
      { cookie: "CF_Authorization=" },
    ]) expect((await me(router, headers)).status).toBe(401);
  });

  test("an explicit API key is used as one, and a bad one never falls back to the cookie", async () => {
    const made = await createApiKey({ organizationId: org, name: "Automation" });
    const session = cookie(await sign(signer, { email: "marcio@example.com" }));
    const keyed = await me(router, { authorization: `Bearer ${made.key}`, ...session });
    expect(keyed.body!.person.id).toBe(made.personId);
    expect(keyed.body!.authMethod).toBe("api_key");
    expect(keyed.body!).not.toHaveProperty("logoutUrl");
    for (const authorization of ["Bearer dude_sk_wrong", "Bearer ", ""]) {
      expect((await me(router, { authorization, ...session })).status).toBe(401);
    }
  });
});

describe("cross-site requests", () => {
  const edge = new Edge();
  let router: Router;
  beforeAll(() => {
    edge.keys = [signer.jwk];
    router = routerFor(edge);
  });
  const patch = (headers: Record<string, string>) =>
    router.handle(new Request(`${ORIGIN}/v1/me`, {
      method: "PATCH", headers: { "content-type": "application/json", ...headers }, body: JSON.stringify({ name: "Changed" }),
    }));

  test("a change by session needs this site's Origin, by cookie or by the edge's header", async () => {
    const token = await sign(signer, { email: "csrf@example.com" });
    for (const credential of [cookie(token), { "cf-access-jwt-assertion": token }]) {
      expect((await patch(credential)).status).toBe(403);
      expect((await patch({ ...credential, origin: "https://evil.example" })).status).toBe(403);
      expect((await patch({ ...credential, origin: "null" })).status).toBe(403);
      expect((await patch({ ...credential, origin: ORIGIN, "sec-fetch-site": "cross-site" })).status).toBe(403);
      expect((await patch({ ...credential, origin: ORIGIN, "sec-fetch-site": "same-site" })).status).toBe(403);
    }
    // Refused before anyone was created.
    expect(await peopleByEmail("csrf@example.com")).toHaveLength(0);
    expect((await patch({ ...cookie(token), origin: ORIGIN, "sec-fetch-site": "same-origin" })).status).toBe(200);
    expect((await patch({ "cf-access-jwt-assertion": token, origin: ORIGIN })).status).toBe(200);
  });

  test("another site's read is refused before sign-in creates anyone", async () => {
    const token = await sign(signer, { email: "crossread@example.com" });
    expect((await me(router, { ...cookie(token), "sec-fetch-site": "cross-site" })).status).toBe(403);
    expect((await me(router, { ...cookie(token), "sec-fetch-site": "same-site" })).status).toBe(403);
    expect(await peopleByEmail("crossread@example.com")).toHaveLength(0);
    expect((await me(router, { ...cookie(token), "sec-fetch-site": "none" })).status).toBe(200);
  });

  test("API-key automation needs no browser headers", async () => {
    const made = await createApiKey({ organizationId: org, name: "Script" });
    expect((await patch({ authorization: `Bearer ${made.key}` })).status).toBe(200);
    expect((await patch({ authorization: `Bearer ${made.key}`, origin: "https://evil.example" })).status).toBe(200);
  });
});

describe("the live stream", () => {
  test("a session stream needs no key and ends when its token does", async () => {
    const edge = new Edge();
    edge.keys = [signer.jwk];
    const router = routerFor(edge);
    const now = Math.floor(Date.now() / 1000);
    const token = await sign(signer, { email: "stream@example.com" }, { exp: now + 2 });
    const started = Date.now();
    const res = await router.handle(new Request(`${ORIGIN}/v1/events/stream?live=1`, { headers: cookie(token) }));
    expect(res.status).toBe(200);
    const body = await res.text(); // returns only once the server closes the stream
    expect(body.startsWith(": open")).toBe(true);
    const took = Date.now() - started;
    expect(took).toBeGreaterThan(500);
    expect(took).toBeLessThan(4_000);
    // Reconnecting with the expired token is refused, beyond the clock tolerance.
    const late = await sign(signer, { email: "stream@example.com" }, { exp: now - 60 });
    expect((await router.handle(new Request(`${ORIGIN}/v1/events/stream?live=1`, { headers: cookie(late) }))).status).toBe(401);
    // An explicit, bad key in the URL does not fall back to the cookie.
    const fresh = await sign(signer, { email: "stream@example.com" });
    expect((await router.handle(new Request(`${ORIGIN}/v1/events/stream?live=1&key=`, { headers: cookie(fresh) }))).status).toBe(401);
  }, 10_000);

  test("a token expiring during a slow backfill ends the stream without the backfill", async () => {
    const edge = new Edge();
    edge.keys = [signer.jwk];
    const router = routerFor(edge);
    await owner`INSERT INTO events (id, organization_id, event_type, actor_type, actor_id, source)
      SELECT 'evt_backfill_' || ${suffix} || '_' || n, ${org}, 'test.backfill', 'system', 'test', 'control-plane'
      FROM generate_series(1, 3) n`;
    const token = await sign(signer, { email: "slowbackfill@example.com" }, { exp: Math.floor(Date.now() / 1000) + 2 });
    // Signed in once beforehand, so the streamed request itself writes nothing.
    expect((await me(router, cookie(token))).status).toBe(200);
    const unlock = Promise.withResolvers<void>();
    const locked = Promise.withResolvers<void>();
    // Holds the ledger so the backfill's query waits until after the token has expired.
    const holder = owner.begin(async (tx) => {
      await tx`LOCK TABLE events IN ACCESS EXCLUSIVE MODE`;
      locked.resolve();
      await unlock.promise;
    });
    try {
      await locked.promise;
      const res = await router.handle(new Request(`${ORIGIN}/v1/events/stream`, { headers: cookie(token) }));
      expect(res.status).toBe(200);
      await waitFor(async () => (await owner`
        SELECT count(*)::int AS n FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
        WHERE NOT l.granted AND l.relation = 'events'::regclass AND a.usename = 'dude_app'`)[0].n > 0);
      // Ends at the token's expiry while the backfill is still blocked.
      const ended = await Promise.race([res.text(), Bun.sleep(6_000).then(() => null)]);
      expect(ended).toBe(": open\n\n");
    } finally {
      unlock.resolve();
      await holder;
    }
  }, 15_000);
});

test("a person principal's profile never sets their role", async () => {
  const edge = new Edge();
  edge.keys = [signer.jwk];
  edge.profiles.set("role@example.com", { email: "role@example.com", name: "R", role: "admin", groups: ["admin"] } as unknown as Profile);
  const got = await me(routerFor(edge), cookie(await sign(signer, { email: "role@example.com", role: "admin" })));
  expect(got.body!.person.role).toBe("member");
});

describe("the resolved principal", () => {
  test("carries the person's current name and role from the sign-in's own read", async () => {
    const edge = new Edge();
    edge.keys = [signer.jwk];
    const authenticate = accessAuthenticator(config(), org, {
      verify: accessVerifier(TEAM, AUD, edge.fetch), profile: accessProfiles(TEAM, edge.fetch),
    });
    const request = async (email: string) =>
      new Request(`${ORIGIN}/v1/me`, { headers: cookie(await sign(signer, { email })) });
    edge.profiles.set("resolved@example.com", { email: "resolved@example.com", name: "Resolved" });
    const created = await authenticate(await request("resolved@example.com"));
    expect(created).toMatchObject({ credentialKind: "person", name: "Resolved", role: "member", resolved: true });
    await owner`UPDATE people SET role = 'admin', name = 'Promoted' WHERE id = ${created!.personId}`;
    expect(await authenticate(await request("resolved@example.com")))
      .toMatchObject({ personId: created!.personId, name: "Promoted", role: "admin", resolved: true });
    await owner`UPDATE people SET removed_at = now() WHERE id = ${created!.personId}`;
    expect(await authenticate(await request("resolved@example.com"))).toBeNull();
  });

  test("the router rereads only people the authenticator did not resolve", async () => {
    // A person id with no row: rereading it refuses the request, trusting it does not.
    const ghost = { credentialKind: "person" as const, organizationId: org, personId: `${org}_ghost`,
      kind: "user" as const, name: "Ghost", role: "admin" as const };
    const status = async (principal: Principal) => {
      const r = new Router(async () => principal);
      registerPeopleRoutes(r);
      r.get("/v1/whoami", async (ctx) => Response.json(ctx.principal));
      const res = await r.handle(new Request(`${ORIGIN}/v1/whoami`));
      return { status: res.status, body: res.status === 200 ? await res.json() : null };
    };
    expect((await status(ghost)).status).toBe(401);
    expect(await status({ ...ghost, resolved: true })).toEqual({ status: 200, body: { ...ghost, resolved: true } });
  });
});

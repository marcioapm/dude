import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { SignJWT } from "jose";
import { createApiKey, type Principal } from "../src/api/auth.ts";
import { accessAuthenticator, accessProfiles, accessVerifier, type Profile } from "../src/api/cloudflareAccess.ts";
import { Router } from "../src/api/router.ts";
import { registerGithubOAuthRoutes } from "../src/api/routes/githubOAuth.ts";
import { registerPeopleRoutes } from "../src/api/routes/people.ts";
import { closePool, setPool } from "../src/db/client.ts";
import {
  AUD, Edge, ISSUER, ORIGIN, TEAM, accessConfig, accessRouter, cookie, databases, me, member, rsaKey, sign, waitFor, type Key,
} from "./access-harness.ts";

const suffix = Bun.randomUUIDv7("hex").slice(-12);
const org = `org_access_tok_${suffix}`;
const at = (local: string) => `${local}@${suffix}.tokens.test`;
let owner: SQL;
let app: SQL;
let signer: Key;
let rotated: Key;
let stranger: Key;

beforeAll(async () => {
  ({ owner, app } = databases());
  setPool(app);
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${org}, ${org}, ${org})`;
  [signer, rotated, stranger] = await Promise.all([rsaKey("k1"), rsaKey("k2"), rsaKey("k1")]);
});
afterAll(async () => {
  await closePool(app);
  await owner`DELETE FROM organizations WHERE id = ${org}`;
  await owner`DELETE FROM accounts WHERE email LIKE ${`%@${suffix}.tokens.test`}`;
  await owner.end();
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
    const hanging: Parameters<typeof accessProfiles>[1] = (_, init) => new Promise((_, reject) =>
      init?.signal?.addEventListener("abort", () => reject(new Error("timeout"))));
    expect(await accessProfiles(TEAM, hanging)(token, "p@example.com")).toBeNull();
  }, 10_000);
});

describe("cross-site requests", () => {
  const edge = new Edge();
  let router: Router;
  beforeAll(async () => {
    edge.keys = [signer.jwk];
    router = accessRouter(edge);
    await member(owner, org, at("csrf"));
  });
  const patch = (headers: Record<string, string>) =>
    router.handle(new Request(`${ORIGIN}/v1/me`, {
      method: "PATCH", headers: { "content-type": "application/json", ...headers }, body: JSON.stringify({ name: "Changed" }),
    }));

  test("a change by session needs this site's Origin, by cookie or by the edge's header", async () => {
    const token = await sign(signer, { email: at("csrf") });
    for (const credential of [cookie(token), { "cf-access-jwt-assertion": token }]) {
      expect((await patch(credential)).status).toBe(403);
      expect((await patch({ ...credential, origin: "https://evil.example" })).status).toBe(403);
      expect((await patch({ ...credential, origin: "null" })).status).toBe(403);
      expect((await patch({ ...credential, origin: ORIGIN, "sec-fetch-site": "cross-site" })).status).toBe(403);
      expect((await patch({ ...credential, origin: ORIGIN, "sec-fetch-site": "same-site" })).status).toBe(403);
    }
    // Refused before anything was written.
    expect((await owner`SELECT name FROM accounts WHERE email = ${at("csrf")}`)[0].name).toBe(at("csrf"));
    expect((await patch({ ...cookie(token), origin: ORIGIN, "sec-fetch-site": "same-origin" })).status).toBe(200);
    expect((await patch({ "cf-access-jwt-assertion": token, origin: ORIGIN })).status).toBe(200);
    expect((await owner`SELECT name FROM accounts WHERE email = ${at("csrf")}`)[0].name).toBe("Changed");
  });

  // Was: "another site's read is refused before sign-in creates anyone". Sign-in creates nobody
  // now; the refusal still comes before the token is even verified.
  test("another site's read is refused before the token is verified or anything is read", async () => {
    const counting = new Edge();
    counting.keys = [signer.jwk];
    const r = accessRouter(counting);
    const token = await sign(signer, { email: at("crossread") });
    expect((await me(r, { ...cookie(token), "sec-fetch-site": "cross-site" })).status).toBe(403);
    expect((await me(r, { ...cookie(token), "sec-fetch-site": "same-site" })).status).toBe(403);
    expect(counting.certsCalls + counting.profileCalls).toBe(0);
    expect(await owner`SELECT id FROM accounts WHERE email = ${at("crossread")}`).toHaveLength(0);
    expect((await me(r, { ...cookie(token), "sec-fetch-site": "none" })).status).toBe(200);
  });

  describe("GitHub's redirect to the OAuth callback", () => {
    const navigation = { "sec-fetch-site": "cross-site", "sec-fetch-mode": "navigate", "sec-fetch-dest": "document" };
    let oauth: Router;
    beforeAll(async () => {
      oauth = accessRouter(edge);
      registerGithubOAuthRoutes(oauth);
      await member(owner, org, at("oauthnav"));
    });
    const visit = async (path: string, headers: Record<string, string>, method = "GET") => {
      const res = await oauth.handle(new Request(`${ORIGIN}${path}`, { method, headers }));
      const body = res.headers.get("content-type")?.includes("json") ? await res.json() as { error?: { code?: string } } : null;
      return { status: res.status, code: body?.error?.code ?? null, location: res.headers.get("location") };
    };
    const callback = "/v1/me/github/callback?state=x&code=y";
    const refused = { status: 403, code: "cross_site", location: null };

    test("a cross-site top-level navigation reaches the callback, which lands on settings", async () => {
      const token = await sign(signer, { email: at("oauthnav") });
      expect(await visit(callback, { ...cookie(token), ...navigation }))
        .toEqual({ status: 303, code: null, location: "/?github=invalid_or_expired#/me/settings" });
    });

    test("a cross-site fetch or frame of the callback is refused", async () => {
      const token = await sign(signer, { email: at("oauthnav") });
      expect(await visit(callback, { ...cookie(token), ...navigation, "sec-fetch-mode": "cors" })).toEqual(refused);
      expect(await visit(callback, { ...cookie(token), ...navigation, "sec-fetch-dest": "iframe" })).toEqual(refused);
    });

    test("a same-site navigation to the callback is refused", async () => {
      const token = await sign(signer, { email: at("oauthnav") });
      expect(await visit(callback, { ...cookie(token), ...navigation, "sec-fetch-site": "same-site" })).toEqual(refused);
    });

    test("a cross-site navigation to any other path is refused", async () => {
      const token = await sign(signer, { email: at("oauthnav") });
      expect(await visit("/v1/me", { ...cookie(token), ...navigation })).toEqual(refused);
      expect(await visit("/v1/me/github/callback/x?state=x&code=y", { ...cookie(token), ...navigation })).toEqual(refused);
      expect(await visit("/v1/me/github/callbacks?state=x&code=y", { ...cookie(token), ...navigation })).toEqual(refused);
      expect(await visit("/v1/me/github/CALLBACK?state=x&code=y", { ...cookie(token), ...navigation })).toEqual(refused);
    });

    test("a cross-site form POST or HEAD to the callback is refused", async () => {
      const token = await sign(signer, { email: at("oauthnav") });
      expect(await visit(callback, { ...cookie(token), ...navigation, origin: "https://github.com" }, "POST")).toEqual(refused);
      expect((await visit(callback, { ...cookie(token), ...navigation }, "HEAD")).status).toBe(403);
    });

    test("a cross-site navigation to the callback still needs a verified Access token", async () => {
      expect((await visit(callback, { ...cookie("not-a-jwt"), ...navigation })).status).toBe(401);
      const foreign = await sign(stranger, { email: at("oauthnav") });
      expect((await visit(callback, { ...cookie(foreign), ...navigation })).status).toBe(401);
    });
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
    const router = accessRouter(edge);
    await member(owner, org, at("stream"));
    const now = Math.floor(Date.now() / 1000);
    const token = await sign(signer, { email: at("stream") }, { exp: now + 2 });
    const started = Date.now();
    const res = await router.handle(new Request(`${ORIGIN}/v1/events/stream?live=1`, { headers: cookie(token) }));
    expect(res.status).toBe(200);
    const body = await res.text(); // returns only once the server closes the stream
    expect(body.startsWith(": open")).toBe(true);
    const took = Date.now() - started;
    expect(took).toBeGreaterThan(500);
    expect(took).toBeLessThan(4_000);
    // Reconnecting with the expired token is refused, beyond the clock tolerance.
    const late = await sign(signer, { email: at("stream") }, { exp: now - 60 });
    expect((await router.handle(new Request(`${ORIGIN}/v1/events/stream?live=1`, { headers: cookie(late) }))).status).toBe(401);
    // An explicit, bad key in the URL does not fall back to the cookie.
    const fresh = await sign(signer, { email: at("stream") });
    expect((await router.handle(new Request(`${ORIGIN}/v1/events/stream?live=1&key=`, { headers: cookie(fresh) }))).status).toBe(401);
  }, 10_000);

  test("a token expiring during a slow backfill ends the stream without the backfill", async () => {
    const edge = new Edge();
    edge.keys = [signer.jwk];
    const router = accessRouter(edge);
    await owner`INSERT INTO events (id, organization_id, event_type, actor_type, actor_id, source)
      SELECT 'evt_backfill_' || ${suffix} || '_' || n, ${org}, 'test.backfill', 'system', 'test', 'control-plane'
      FROM generate_series(1, 3) n`;
    await member(owner, org, at("slowbackfill"), { name: "Slow Backfill", photoUrl: "https://img.example/s.png" });
    const token = await sign(signer, { email: at("slowbackfill") }, { exp: Math.floor(Date.now() / 1000) + 2 });
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
  await member(owner, org, at("role"));
  edge.profiles.set(at("role"), { email: at("role"), name: "R", role: "admin", groups: ["admin"] } as unknown as Profile);
  const got = await me(accessRouter(edge), cookie(await sign(signer, { email: at("role"), role: "admin" })));
  expect(got.body!.person.role).toBe("member");
});

describe("the resolved principal", () => {
  test("carries the person's current name and role from the sign-in's own read", async () => {
    const edge = new Edge();
    edge.keys = [signer.jwk];
    const authenticate = accessAuthenticator(accessConfig(), "", {
      verify: accessVerifier(TEAM, AUD, edge.fetch), profile: accessProfiles(TEAM, edge.fetch),
    });
    const request = async (email: string) =>
      new Request(`${ORIGIN}/v1/me`, { headers: cookie(await sign(signer, { email })) });
    edge.profiles.set(at("resolved"), { email: at("resolved"), name: "Resolved" });
    // Was made by the sign-in itself; now it is an accepted invitation's member, named as the address.
    const id = await member(owner, org, at("resolved"));
    const created = await authenticate(await request(at("resolved")));
    expect(created).toMatchObject({ credentialKind: "person", personId: id, name: "Resolved", role: "member", resolved: true });
    await owner`UPDATE people SET role = 'admin', name = 'Promoted' WHERE id = ${id}`;
    expect(await authenticate(await request(at("resolved"))))
      .toMatchObject({ personId: id, name: "Promoted", role: "admin", resolved: true });
    // Removed from their only organisation: still a verified identity, but admitted nowhere.
    await owner`UPDATE people SET removed_at = now() WHERE id = ${id}`;
    expect(await authenticate(await request(at("resolved"))))
      .toMatchObject({ personId: "", organizationId: "", access: { gate: "none" } });
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

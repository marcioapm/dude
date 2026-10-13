import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { createApiKey } from "../src/api/auth.ts";
import type { Router } from "../src/api/router.ts";
import { closePool, setPool } from "../src/db/client.ts";
import {
  Edge, ORIGIN, accessRouter, cookie, databases, invitation, me, member, rsaKey, sign, type Key,
} from "./access-harness.ts";

const suffix = Bun.randomUUIDv7("hex").slice(-12);
const org = `org_access_${suffix}`;
const other = `${org}_other`;
const at = (local: string) => `${local}@${suffix}.signin.test`;
let owner: SQL;
let app: SQL;
let signer: Key;

const accountsOf = (email: string) => owner`SELECT id, name, photo_url FROM accounts WHERE email = ${email}`;
type PersonRow = { id: string; name: string; role: string; photo_url: string | null; removed_at: Date | null };
const peopleByEmail = async (email: string, organizationId = org): Promise<PersonRow[]> =>
  (await owner`SELECT id, name, role, photo_url, removed_at FROM people WHERE organization_id = ${organizationId} AND email = ${email}`) as PersonRow[];
const keysOf = (personId: string) => owner`SELECT id FROM api_keys WHERE person_id = ${personId}`;

beforeAll(async () => {
  ({ owner, app } = databases());
  setPool(app);
  for (const id of [org, other]) await owner`INSERT INTO organizations (id, name, slug) VALUES (${id}, ${id}, ${id})`;
  signer = await rsaKey("k1");
});
afterAll(async () => {
  await closePool(app);
  await owner`DELETE FROM organizations WHERE id IN (${org}, ${other})`;
  await owner`DELETE FROM accounts WHERE email LIKE ${`%@${suffix}.signin.test`}`;
  await owner.end();
});

describe("signing in", () => {
  const edge = new Edge();
  let router: Router;
  beforeAll(() => {
    edge.keys = [signer.jwk];
    router = accessRouter(edge);
  });

  // Was: "an unknown email becomes a member with no key, seeded from the profile".
  test("an unknown email gets gate none, its own identity only, and nothing is written", async () => {
    edge.profiles.set(at("new"), { email: at("new"), name: "New Person", oidc_fields: { picture: "https://img.example/n.png" } });
    const got = await me(router, cookie(await sign(signer, { email: at("NEW") })));
    expect(got.status).toBe(200);
    expect(got.body).toEqual({ gate: "none", email: at("new"), name: "New Person", photoUrl: "https://img.example/n.png" } as never);
    expect(JSON.stringify(got.body)).not.toContain("dude_sk_");
    expect(await accountsOf(at("new"))).toHaveLength(0);
    expect(await peopleByEmail(at("new"))).toHaveLength(0);
    // A second request: still nobody, still nothing.
    const again = await me(router, { "cf-access-jwt-assertion": await sign(signer, { email: at("new") }) });
    expect(again.body!.gate).toBe("none");
    expect(await accountsOf(at("new"))).toHaveLength(0);
  });

  test("an existing admin keeps id, role and the name they set; an empty photo is filled", async () => {
    // Migration 113 made the account from the person the org already had.
    const id = await member(owner, org, at("marcio"), { name: "Márcio", role: "admin" });
    edge.profiles.set(at("marcio"), { email: at("marcio"), name: "Marcio IdP", oidc_fields: { picture: "https://img.example/m.png" } });
    const got = await me(router, cookie(await sign(signer, { email: at("MARCIO") })));
    expect(got.body!.person).toMatchObject({ id, name: "Márcio", role: "admin", photoUrl: "https://img.example/m.png" });
    // A photo set since is not replaced.
    await owner`UPDATE accounts SET photo_url = 'https://mine.example/me.png' WHERE email = ${at("marcio")}`;
    edge.profiles.set(at("marcio"), { email: at("marcio"), name: "X", oidc_fields: { picture: "https://img.example/other.png" } });
    const later = await accessRouter(edge).handle(new Request(`${ORIGIN}/v1/me`, { headers: cookie(await sign(signer, { email: at("marcio") })) }));
    expect((await later.json() as { person: { photoUrl: string } }).person.photoUrl).toBe("https://mine.example/me.png");
    expect(await keysOf(id)).toHaveLength(0);
  });

  // Was: "a failed profile lookup does not deny a member, and seeds the email as name".
  test("a failed profile lookup does not deny a member; a later profile fills a name that was only the email", async () => {
    const failing = new Edge();
    failing.keys = [signer.jwk];
    failing.profileStatus = 503;
    await member(owner, org, at("noprofile"));
    const got = await me(accessRouter(failing), cookie(await sign(signer, { email: at("noprofile") })));
    expect(got.status).toBe(200);
    expect(got.body!.person.name).toBe(at("noprofile"));
    edge.profiles.set(at("noprofile"), { email: at("noprofile"), name: "Now Named" });
    const named = await me(router, cookie(await sign(signer, { email: at("noprofile") })));
    expect(named.body!.person.name).toBe("Now Named");
  });

  // Was: "a removed person is refused and not recreated; auto_create off refuses strangers".
  test("a removed person gets gate none and is not recreated; strangers are refused whatever the old settings say", async () => {
    const id = await member(owner, org, at("gone"), { name: "Gone" });
    await owner`UPDATE people SET removed_at = now() WHERE id = ${id}`;
    const got = await me(router, cookie(await sign(signer, { email: at("gone") })));
    expect(got.body).toMatchObject({ gate: "none" });
    expect(got.body).not.toHaveProperty("person");
    expect(await peopleByEmail(at("gone"))).toHaveLength(1);
    expect((await peopleByEmail(at("gone")))[0]!.removed_at).not.toBeNull();
    // auto_create is retired; a stranger reaches nothing of an organisation's.
    const res = await router.handle(new Request(`${ORIGIN}/v1/people`, { headers: cookie(await sign(signer, { email: at("stranger") })) }));
    expect(res.status).toBe(403);
    expect(await peopleByEmail(at("stranger"))).toHaveLength(0);
    expect(await accountsOf(at("stranger"))).toHaveLength(0);
  });

  // Was: "removing someone ends their next request" (then a 401; the design makes it the gate's none line).
  test("removing someone ends their next request", async () => {
    const id = await member(owner, org, at("leaving"));
    const token = await sign(signer, { email: at("leaving") });
    expect((await router.handle(new Request(`${ORIGIN}/v1/people`, { headers: cookie(token) }))).status).toBe(200);
    await owner`UPDATE people SET removed_at = now() WHERE id = ${id}`;
    const next = await router.handle(new Request(`${ORIGIN}/v1/people`, { headers: cookie(token) }));
    expect(next.status).toBe(403);
    expect((await next.json() as { error: { code: string } }).error.code).toBe("not_admitted");
    expect((await me(router, cookie(token))).body!.gate).toBe("none");
  });

  test("a slow profile holds no database transaction while another request completes", async () => {
    const slow = new Edge();
    slow.keys = [signer.jwk];
    const gate = Promise.withResolvers<void>();
    const bothAsked = Promise.withResolvers<void>();
    slow.profileGate = gate.promise;
    slow.profileStarted = () => { if (slow.profileCalls === 2) bothAsked.resolve(); };
    // One first visitor (the pre-membership path, which shows the profile) and one member whose
    // name is still their email (the fill path).
    await member(owner, org, at("unnamed"));
    slow.profiles.set(at("unnamed"), { email: at("unnamed"), name: "Named Later" });
    const r = accessRouter(slow);
    const waiting = Promise.all([at("slowfirst"), at("unnamed")]
      .map(async (email) => me(r, cookie(await sign(signer, { email })))));
    await bothAsked.promise;
    const [open] = await owner`
      SELECT count(*)::int AS n FROM pg_stat_activity
      WHERE datname = current_database() AND usename = 'dude_app' AND state LIKE 'idle in transaction%'`;
    expect(open.n).toBe(0);
    await member(owner, org, at("meanwhile"), { name: "Meanwhile", photoUrl: "https://img.example/mw.png" });
    expect((await me(router, cookie(await sign(signer, { email: at("meanwhile") })))).status).toBe(200);
    gate.resolve();
    const [first, filled] = await waiting;
    expect(first!.status).toBe(200);
    expect(first!.body!.gate).toBe("none");
    expect(filled!.body!.person.name).toBe("Named Later");
  });

  // The profile step is the window between the first admission read and the
  // second: hold the sign-in there while someone else changes membership.
  async function signInAround(email: string, meanwhile: () => Promise<void>, failProfile = false) {
    const inProfile = Promise.withResolvers<void>();
    const release = Promise.withResolvers<void>();
    const r = accessRouter(edge, {}, (profile) => async (token, e) => {
      inProfile.resolve();
      await release.promise;
      return failProfile ? null : profile(token, e);
    });
    const signing = me(r, cookie(await sign(signer, { email })));
    await inProfile.promise;
    await meanwhile();
    release.resolve();
    return signing;
  }

  // Was: "someone removed while the first sign-in fetched its profile is refused, not recreated".
  test("someone removed while their sign-in fetched its profile is refused, and nothing is recreated", async () => {
    const id = await member(owner, org, at("tombstone"));
    const got = await signInAround(at("tombstone"),
      async () => { await owner`UPDATE people SET removed_at = now() WHERE id = ${id}`; });
    expect(got.body!.gate).toBe("none");
    expect((await peopleByEmail(at("tombstone"))).map((r) => r.id)).toEqual([id]);
  });

  // Was: "a first sign-in rechecks only after the removal holding the lock commits".
  test("a sign-in's recheck sees a removal only once the removal holding the people lock commits", async () => {
    const email = at("lockorder");
    const id = await member(owner, org, email);
    const locked = Promise.withResolvers<void>();
    const commit = Promise.withResolvers<boolean>();
    // Removes them under the organisation's people lock, committing only when told.
    const removal = owner.begin(async (tx) => {
      await tx`SELECT pg_advisory_xact_lock(hashtext('people:' || ${org}))`;
      await tx`UPDATE people SET removed_at = now() WHERE id = ${id}`;
      locked.resolve();
      if (!(await commit.promise)) throw new Error("rolled back");
    });
    try {
      const got = signInAround(email, () => locked.promise);
      // Uncommitted, the removal is invisible: the recheck (on release) still admits them.
      expect((await got).body!.gate).toBe("member");
      commit.resolve(true);
      await removal;
      expect((await me(router, cookie(await sign(signer, { email })))).body!.gate).toBe("none");
    } finally {
      commit.resolve(false);
      await removal.catch(() => {});
    }
  });

  // An existing member whose name is still their email and who has no photo gets the profile's;
  // removed meanwhile, the fill must change nothing and the request must not be admitted.
  for (const [branch, failProfile] of [["a matching profile", false], ["a failed profile", true]] as const) {
    test(`someone removed while ${branch} was fetched for them is refused and left unchanged`, async () => {
      const email = at(`fill${failProfile ? "failed" : "matched"}`);
      const id = await member(owner, org, email);
      edge.profiles.set(email, { email, name: "Filled In", oidc_fields: { picture: "https://img.example/f.png" } });
      const got = await signInAround(email,
        async () => { await owner`UPDATE people SET removed_at = now() WHERE id = ${id}`; }, failProfile);
      expect(got.body!.gate).toBe("none");
      const rows = await peopleByEmail(email);
      expect(rows).toHaveLength(1);
      expect(rows[0]).toMatchObject({ id, name: email, photo_url: null });
      expect(rows[0]!.removed_at).not.toBeNull();
      expect((await accountsOf(email))[0]).toMatchObject({ name: email, photo_url: null });
    });
  }

  // Was: "a member added while the first sign-in fetched its profile is reused".
  test("a membership accepted while a first sign-in fetched its profile admits that request", async () => {
    const email = at("addedmeanwhile");
    let id = "";
    const got = await signInAround(email, async () => { id = await member(owner, org, email, { name: "Meanwhile" }); });
    expect(got.status).toBe(200);
    expect(got.body!.person).toMatchObject({ id, name: "Meanwhile" });
    expect(await peopleByEmail(email)).toHaveLength(1);
  });

  test("concurrent first requests for one email share one profile fetch", async () => {
    const slow = new Edge();
    slow.keys = [signer.jwk];
    slow.profiles.set(at("herd"), { email: at("herd"), name: "Herd" });
    await member(owner, org, at("herd"));
    const gate = Promise.withResolvers<void>();
    slow.profileGate = gate.promise;
    const allAsked = Promise.withResolvers<void>();
    let asks = 0;
    const r = accessRouter(slow, {}, (profile) => (token, email) => {
      if (++asks === 6) allAsked.resolve();
      return profile(token, email);
    });
    const tokens = await Promise.all(Array.from({ length: 6 }, () => sign(signer, { email: at("herd") })));
    const results = Promise.all(tokens.map((t) => me(r, cookie(t))));
    await allAsked.promise;
    gate.resolve();
    const done = await results;
    expect(done.map((d) => d.status)).toEqual(Array(6).fill(200));
    expect(new Set(done.map((d) => d.body!.person.id)).size).toBe(1);
    expect(done[0]!.body!.person.name).toBe("Herd");
    expect(slow.profileCalls).toBe(1);
    expect(await peopleByEmail(at("herd"))).toHaveLength(1);
  });

  // Was: "organizations stay apart; a person with no email is never linked".
  test("organizations stay apart; a person with no email is never linked", async () => {
    // A same-address person in another organisation with no account link (never accepted there),
    // and an email-less person named as the address in this one.
    const foreign = `${other}_same`;
    await owner`INSERT INTO people (id, organization_id, name, email, role) VALUES (${foreign}, ${other}, 'Other', ${at("shared")}, 'admin')`;
    const nullEmail = `${org}_noemail`;
    await owner`INSERT INTO people (id, organization_id, name, role) VALUES (${nullEmail}, ${org}, ${at("shared")}, 'admin')`;
    const mine = await member(owner, org, at("shared"));
    const got = await me(router, cookie(await sign(signer, { email: at("shared") })));
    expect(got.body!.person.id).toBe(mine);
    expect(got.body!.person.role).toBe("member");
    expect(got.body!.organizations!.map((o) => o.id)).toEqual([org]);
    const refused = await router.handle(new Request(`${ORIGIN}/v1/people`, {
      headers: { ...cookie(await sign(signer, { email: at("shared") })), "x-dude-organization": other } }));
    expect(refused.status).toBe(403);
    expect(await peopleByEmail(at("shared"), other)).toHaveLength(1);
  });

  test("the email lookup sees only the transaction's own organization", async () => {
    await owner`INSERT INTO people (id, organization_id, name, email) VALUES (${`${other}_scoped`}, ${other}, 'Scoped', ${at("scoped")})`;
    const lookup = (organizationId: string | null) => app.begin(async (tx) => {
      if (organizationId) await tx`SELECT set_config('app.organization_id', ${organizationId}, true)`;
      return (await tx`SELECT id FROM people_by_email(${at("scoped").toUpperCase()})`).map((r: { id: string }) => r.id);
    });
    expect(await lookup(other)).toEqual([`${other}_scoped`]);
    expect(await lookup(org)).toEqual([]);
    expect(await lookup(null)).toEqual([]);
  });

  test("the header wins over the cookie, and nothing but a verified token identifies anyone", async () => {
    await member(owner, org, at("headerwins"), { name: "Header Wins", photoUrl: "https://img.example/h.png" });
    const marcio = await member(owner, other, at("cookieloses"), { name: "Cookie", role: "admin" });
    const a = await sign(signer, { email: at("headerwins") });
    const b = await sign(signer, { email: at("cookieloses") });
    const got = await me(router, { "cf-access-jwt-assertion": a, ...cookie(b) });
    expect(got.body!.person.name).toBe("Header Wins");
    // An invalid header does not fall back to a valid cookie.
    expect((await me(router, { "cf-access-jwt-assertion": "bad", ...cookie(b) })).status).toBe(401);
    for (const headers of [
      { "cf-access-authenticated-user-email": at("cookieloses") },
      { "x-dude-person": marcio, "x-dude-organization": other },
      { cookie: "CF_Authorization=" },
    ]) expect((await me(router, headers)).status).toBe(401);
  });

  test("an explicit API key is used as one, and a bad one never falls back to the cookie", async () => {
    const made = await createApiKey({ organizationId: org, name: "Automation" });
    await member(owner, org, at("explicit"), { name: "Explicit", photoUrl: "https://img.example/e.png" });
    const session = cookie(await sign(signer, { email: at("explicit") }));
    const keyed = await me(router, { authorization: `Bearer ${made.key}`, ...session });
    expect(keyed.body!.person.id).toBe(made.personId);
    expect(keyed.body!.authMethod).toBe("api_key");
    expect(keyed.body!).not.toHaveProperty("logoutUrl");
    for (const authorization of ["Bearer dude_sk_wrong", "Bearer ", ""]) {
      expect((await me(router, { authorization, ...session })).status).toBe(401);
    }
  });

  // Replaces "concurrent first sign-ins make one person": sign-in makes nobody; acceptance makes
  // the account and membership, so its concurrency is what must make exactly one of each.
  test("concurrent first acceptances make one account and one membership", async () => {
    const email = at("race");
    const code = await invitation(owner, org, email);
    const post = async () => router.handle(new Request(`${ORIGIN}/v1/invitations/${code}/accept`, {
      method: "POST", headers: { ...cookie(await sign(signer, { email })), origin: ORIGIN, "content-type": "application/json" }, body: "{}" }));
    const results = await Promise.all(Array.from({ length: 6 }, post));
    expect(results.map((r) => r.status)).toEqual(Array(6).fill(200));
    const bodies = await Promise.all(results.map((r) => r.json() as Promise<{ person: { id: string } }>));
    expect(new Set(bodies.map((b) => b.person.id)).size).toBe(1);
    expect(await accountsOf(email)).toHaveLength(1);
    expect(await peopleByEmail(email)).toHaveLength(1);
  });
});

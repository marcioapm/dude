import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { attentionItems, waitingSplit, type NavProject } from "../../../packages/design-system/src/util/navModel.ts";
import { createApiKey } from "../src/api/auth.ts";
import type { Router } from "../src/api/router.ts";
import { closePool, setPool } from "../src/db/client.ts";
import { Edge, OPERATOR, ORIGIN, cookie, databases, fullRouter, invitation, member, rsaKey, sign, type Key } from "./access-harness.ts";

const suffix = Bun.randomUUIDv7("hex").slice(-12);
const acme = `org_inv_${suffix}`;
const northwind = `${acme}_nw`;
const at = (local: string) => `${local}@${suffix}.inv.test`;
let owner: SQL;
let app: SQL;
let signer: Key;
let router: Router;
let adminId: string;
type Json = any;

beforeAll(async () => {
  ({ owner, app } = databases());
  setPool(app);
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${acme}, 'Acme', ${acme}), (${northwind}, 'Northwind', ${northwind})`;
  signer = await rsaKey("k1");
  const edge = new Edge();
  edge.keys = [signer.jwk];
  router = fullRouter(edge);
  adminId = await member(owner, acme, at("ana"), { name: "Ana", role: "admin", photoUrl: "https://img.example/a.png" });
});
afterAll(async () => {
  await closePool(app);
  await owner`DELETE FROM organizations WHERE id LIKE ${`${acme}%`}`;
  await owner`DELETE FROM accounts WHERE email LIKE ${`%@${suffix}.inv.test`}`;
  await owner.end();
});

async function as(email: string, method: string, path: string, body?: unknown, organization: string | null = acme) {
  const headers: Record<string, string> = { ...cookie(await sign(signer, { email })), origin: ORIGIN, "content-type": "application/json" };
  if (organization) headers["x-dude-organization"] = organization;
  const res = await router.handle(new Request(ORIGIN + path, { method, headers, ...(body === undefined ? {} : { body: JSON.stringify(body) }) }));
  return { status: res.status, body: res.status === 204 ? null : await res.json().catch(() => null) as Json };
}
const invite = (emails: string[], role = "member", by = at("ana"), organization = acme) =>
  as(by, "POST", "/v1/organization/invitations", { invitations: emails.map((email) => ({ email })), role }, organization);

describe("inviting", () => {
  test("every per-address result, with a link for the first three only", async () => {
    await invite([at("expiring")]);
    await owner`UPDATE invitations SET expires_at = now() - interval '1 day' WHERE email = ${at("expiring")}`;
    await invite([at("waiting")]);
    const got = await as(at("ana"), "POST", "/v1/organization/invitations", { role: "member", invitations: [
      { email: ` ${at("NEW")} `, name: "New" }, { email: at("new") }, { email: "not an email" }, { email: at("ana") },
      { email: at("waiting") }, { email: at("expiring") },
    ] });
    expect(got.status).toBe(200);
    const results = got.body.results as Array<{ email: string; result: string; link?: string; invitation?: { code: string } }>;
    expect(results.map((r) => [r.email, r.result])).toEqual([
      [at("new"), "created"], [at("new"), "duplicate"], ["not an email", "invalid"], [at("ana"), "member"],
      [at("waiting"), "existing"], [at("expiring"), "renewed"],
    ]);
    for (const r of results) expect(r.link !== undefined).toBe(["created", "existing", "renewed"].includes(r.result));
    expect(results[0]!.link).toBe(`#/invite/${results[0]!.invitation!.code}`);
    expect(results[0]!.invitation!.code).toMatch(/^[0-9A-Za-z]{22}$/);
  });

  test("renew keeps the same code; revoking twice is a 404; members may not invite", async () => {
    const made = (await invite([at("renew")])).body.results[0];
    const renewed = await as(at("ana"), "POST", `/v1/organization/invitations/${made.invitation.id}/renew`);
    expect(renewed).toMatchObject({ status: 200, body: { code: made.invitation.code } });
    expect((await as(at("ana"), "DELETE", `/v1/organization/invitations/${made.invitation.id}`)).status).toBe(204);
    expect((await as(at("ana"), "DELETE", `/v1/organization/invitations/${made.invitation.id}`)).status).toBe(404);
    expect((await as(at("ana"), "POST", `/v1/organization/invitations/${made.invitation.id}/renew`)).status).toBe(404);
    await member(owner, acme, at("plain"));
    const refused = await invite([at("x")], "member", at("plain"));
    expect(refused).toMatchObject({ status: 403, body: { error: { code: "not_admin" } } });
  });

  test("the list filters by status and search in SQL", async () => {
    const all = await as(at("ana"), "GET", "/v1/organization/invitations?status=all");
    expect(all.body.invitations.length).toBeGreaterThan(3);
    const revoked = await as(at("ana"), "GET", "/v1/organization/invitations?status=revoked");
    expect(revoked.body.invitations.map((i: Json) => i.email)).toEqual([at("renew")]);
    const searched = await as(at("ana"), "GET", `/v1/organization/invitations?status=pending&q=waiting`);
    expect(searched.body.invitations.map((i: Json) => i.email)).toEqual([at("waiting")]);
    expect((await as(at("ana"), "GET", "/v1/organization/invitations?status=bogus")).status).toBe(400);
  });
});

describe("accepting", () => {
  test("wrong email learns nothing; unusable codes are one identical 404", async () => {
    const code = await invitation(owner, acme, at("chloe"));
    const wrong = await as(at("someone"), "GET", `/v1/invitations/${code}`, undefined, null);
    expect(wrong).toEqual({ status: 403, body: { error: { code: "invitation_for_another_email", message: "invitation_for_another_email" } } });
    expect((await as(at("someone"), "POST", `/v1/invitations/${code}/accept`, {}, null)).status).toBe(403);
    const expired = await invitation(owner, acme, at("chloe-expired"));
    await owner`UPDATE invitations SET expires_at = now() WHERE code = ${expired}`;
    const revoked = await invitation(owner, acme, at("chloe-revoked"));
    await owner`UPDATE invitations SET revoked_at = now() WHERE code = ${revoked}`;
    const answers = [
      await as(at("chloe-expired"), "GET", `/v1/invitations/${expired}`, undefined, null),
      await as(at("chloe-revoked"), "GET", `/v1/invitations/${revoked}`, undefined, null),
      await as(at("chloe"), "GET", "/v1/invitations/nonexistentcode00000000", undefined, null),
      await as(at("chloe-expired"), "POST", `/v1/invitations/${expired}/accept`, {}, null),
    ];
    for (const a of answers) expect(a).toEqual({ status: 404, body: { error: { code: "invitation_unusable", message: "invitation_unusable" } } });
    expect(await owner`SELECT id FROM accounts WHERE email IN (${at("someone")}, ${at("chloe-expired")})`).toHaveLength(0);
  });

  test("accepting makes the account and membership; repeating it returns the membership; others see it as unusable", async () => {
    const code = (await invite([at("dee")], "admin")).body.results[0].invitation.code;
    const shown = await as(at("dee"), "GET", `/v1/invitations/${code}`, undefined, null);
    expect(shown.body).toMatchObject({ state: "pending", role: "admin", inviter: "Ana", organization: { id: acme, name: "Acme" }, email: at("dee") });
    const joined = await as(at("dee"), "POST", `/v1/invitations/${code}/accept`, { name: "Dee Marsh" }, null);
    expect(joined).toMatchObject({ status: 200, body: { person: { role: "admin", name: "Dee Marsh" }, organization: { id: acme } } });
    expect(await owner`SELECT name FROM accounts WHERE email = ${at("dee")}`).toEqual([{ name: "Dee Marsh" }] as never);
    await owner`UPDATE people SET role = 'member' WHERE id = ${joined.body.person.id}`;
    const again = await as(at("dee"), "POST", `/v1/invitations/${code}/accept`, {}, null);
    expect(again.body.person).toMatchObject({ id: joined.body.person.id, role: "member" });
    expect((await as(at("dee"), "GET", `/v1/invitations/${code}`, undefined, null)).body.state).toBe("member");
    // An accepted code is readable only by its active member.
    expect((await as(at("ana"), "GET", `/v1/invitations/${code}`, undefined, null)).status).toBe(404);
    await owner`UPDATE people SET removed_at = now() WHERE id = ${joined.body.person.id}`;
    expect((await as(at("dee"), "GET", `/v1/invitations/${code}`, undefined, null)).status).toBe(404);
  });

  test("a removed person comes back as the same row, and their old keys stay revoked", async () => {
    const first = (await invite([at("eli")])).body.results[0].invitation.code;
    const person = (await as(at("eli"), "POST", `/v1/invitations/${first}/accept`, {}, null)).body.person;
    const key = await as(at("eli"), "POST", "/v1/me/keys", { organizationId: acme, name: "Laptop" }, null);
    expect(key.status).toBe(201);
    expect((await as(at("ana"), "DELETE", `/v1/people/${person.id}`)).status).toBe(204);
    const second = (await invite([at("eli")])).body.results[0];
    expect(second.result).toBe("created");
    const back = await as(at("eli"), "POST", `/v1/invitations/${second.invitation.code}/accept`, {}, null);
    expect(back.body.person.id).toBe(person.id);
    const keyed = await router.handle(new Request(`${ORIGIN}/v1/me`, { headers: { authorization: `Bearer ${key.body.key}` } }));
    expect(keyed.status).toBe(401);
    expect((await as(at("eli"), "GET", "/v1/me/keys", undefined, null)).body.keys).toEqual([]);
  });
});

describe("your keys, in each organisation", () => {
  test("listed across memberships, made for one, and removal revokes only that organisation's", async () => {
    const email = at("fay");
    const inAcme = await member(owner, acme, email);
    await member(owner, northwind, email);
    const a = await as(email, "POST", "/v1/me/keys", { organizationId: acme, name: "Acme laptop" }, null);
    const n = await as(email, "POST", "/v1/me/keys", { organizationId: northwind, name: "Northwind laptop" }, null);
    expect([a.status, n.status]).toEqual([201, 201]);
    expect(a.body.key).toStartWith("dude_sk_");
    const listed = (await as(email, "GET", "/v1/me/keys", undefined, null)).body.keys as Json[];
    expect(listed.map((k) => [k.organizationId, k.name]).sort()).toEqual([[acme, "Acme laptop"], [northwind, "Northwind laptop"]].sort());
    expect(listed.some((k) => "key" in k)).toBe(false);
    expect((await as(email, "POST", "/v1/me/keys", { organizationId: `${acme}_nope`, name: "x" }, null)).status).toBe(403);
    expect((await as(at("ana"), "DELETE", `/v1/people/${inAcme}`)).status).toBe(204);
    const status = async (key: string) => (await router.handle(new Request(`${ORIGIN}/v1/me`, { headers: { authorization: `Bearer ${key}` } }))).status;
    expect(await status(a.body.key)).toBe(401);
    expect(await status(n.body.key)).toBe(200);
  });
});

describe("/v1/me across organisations", () => {
  test("each organisation's waiting count is the Waiting on you page's own count there", async () => {
    const email = at("gus");
    const inAcme = await member(owner, acme, email);
    const inNorthwind = await member(owner, northwind, email);
    const other = await member(owner, northwind, at("hal"));
    for (const [org, key] of [[acme, "AC"], [northwind, "NW"]] as const) {
      await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES (${`prj_${org}`}, ${org}, 'P', 'p', ${key})`;
    }
    const task = async (org: string, n: number, status: string, people: string[]) => {
      const id = `wi_${org}_${n}`;
      await owner`INSERT INTO tasks (id, organization_id, project_id, number, title, status)
        VALUES (${id}, ${org}, ${`prj_${org}`}, ${n}, ${`T${n}`}, ${status}::task_status)`;
      for (const [position, person] of people.entries()) {
        await owner`INSERT INTO task_people (task_id, person_id, organization_id, position) VALUES (${id}, ${person}, ${org}, ${position})`;
      }
    };
    // Acme: one of Gus's, one nobody's (anyone's); Northwind: three of Gus's, one of Hal's, one running.
    await task(acme, 1, "awaiting_confirmation", [inAcme]);
    await task(acme, 2, "awaiting_input", []);
    for (const n of [1, 2, 3]) await task(northwind, n, "awaiting_confirmation", [inNorthwind]);
    await task(northwind, 4, "awaiting_input", [other]);
    await task(northwind, 5, "running", [inNorthwind]);
    const got = await as(email, "GET", "/v1/me", undefined, null);
    expect(got.body.gate).toBe("member");
    const byId = new Map((got.body.organizations as Json[]).map((o) => [o.id, o]));
    expect(byId.get(acme)).toMatchObject({ name: "Acme", role: "member", personId: inAcme, waitingOnYou: 2 });
    expect(byId.get(northwind)).toMatchObject({ name: "Northwind", personId: inNorthwind, waitingOnYou: 3 });
    // The same numbers the page computes from the navigation it reads in that organisation.
    for (const [org, you] of [[acme, inAcme], [northwind, inNorthwind]] as const) {
      const nav = (await as(email, "GET", "/v1/navigation", undefined, org)).body as { projects: NavProject[] };
      expect(byId.get(org)!.waitingOnYou).toBe(waitingSplit(attentionItems(nav.projects), you).yours.length);
    }
  });

  test("locate finds an id in your other organisation, and says nothing of one that is not yours", async () => {
    const email = at("ida");
    await member(owner, acme, email);
    const found = await as(email, "GET", `/v1/locate/wi_${northwind}_1`, undefined, null);
    expect(found.status).toBe(404);
    await member(owner, northwind, email);
    const now = await as(email, "GET", `/v1/locate/wi_${northwind}_1`, undefined, null);
    expect(now).toEqual({ status: 200, body: { organization: { id: northwind, name: "Northwind", slug: northwind }, kind: "task", id: `wi_${northwind}_1` } });
    const nowhere = await as(email, "GET", "/v1/locate/wi_nowhere", undefined, null);
    expect(nowhere.status).toBe(404);
    // Not yours anywhere reads exactly as never existing.
    await owner`INSERT INTO organizations (id, name, slug) VALUES (${`${acme}_x`}, 'Hidden', ${`${acme}_x`})`;
    await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES (${`prj_${acme}_x`}, ${`${acme}_x`}, 'H', 'h', 'HH')`;
    const hidden = await as(email, "GET", `/v1/locate/prj_${acme}_x`, undefined, null);
    expect(hidden).toEqual(nowhere);
  });

  test("PATCH /v1/me writes the account; active memberships copy it, a removed one keeps its old name", async () => {
    const email = at("jo");
    const active = await member(owner, acme, email, { name: "Jo" });
    const removed = await member(owner, northwind, email);
    await owner`UPDATE people SET removed_at = now(), name = 'Jo Before' WHERE id = ${removed}`;
    const third = await member(owner, `${acme}_x`, email);
    // Two active memberships and no organisation named: the account is what changes.
    const named = await as(email, "PATCH", "/v1/me", { name: "Jo Renamed", photoUrl: "https://img.example/jo.png" }, null);
    expect(named).toMatchObject({ status: 200, body: { account: { name: "Jo Renamed", photoUrl: "https://img.example/jo.png" } } });
    // A membership's own GitHub identity needs its organisation.
    const login = await as(email, "PATCH", "/v1/me", { commitEmail: email }, null);
    expect(login.body.error.code).toBe("organization_required");
    expect(await owner`SELECT name, photo_url FROM accounts WHERE email = ${email}`).toEqual([{ name: "Jo Renamed", photo_url: "https://img.example/jo.png" }] as never);
    const rows = await owner`SELECT id, name, photo_url FROM people WHERE id IN (${active}, ${removed}, ${third}) ORDER BY id`;
    const byId = new Map(rows.map((r: Json) => [r.id, r]));
    expect(byId.get(active)).toMatchObject({ name: "Jo Renamed", photo_url: "https://img.example/jo.png" });
    expect(byId.get(third)).toMatchObject({ name: "Jo Renamed", photo_url: "https://img.example/jo.png" });
    expect(byId.get(removed)).toMatchObject({ name: "Jo Before", photo_url: null });
  });
});

describe("roles", () => {
  test("never the last admin; never yourself; any other change is an admin's", async () => {
    expect((await as(at("ana"), "PATCH", `/v1/people/${adminId}`, { role: "member" })).status).toBe(409);
    expect((await as(at("ana"), "DELETE", `/v1/people/${adminId}`)).status).toBe(409);
    const kim = await member(owner, acme, at("kim"), { role: "admin" });
    // Two admins now: still neither may demote or remove themselves.
    expect((await as(at("ana"), "PATCH", `/v1/people/${adminId}`, { role: "member" })).status).toBe(409);
    expect((await as(at("ana"), "DELETE", `/v1/people/${adminId}`)).status).toBe(409);
    expect((await as(at("ana"), "PATCH", `/v1/people/${kim}`, { role: "member" })).status).toBe(200);
  });
});

describe("the super-admin", () => {
  test("creates an organisation with default tiers and sizes and its first admin's link", async () => {
    const slug = `created-${suffix}`;
    const made = await as(OPERATOR, "POST", "/v1/admin/organizations", { name: "Created", slug, email: at("first") }, null);
    expect(made.status).toBe(201);
    expect(made.body).toMatchObject({ organization: { name: "Created", slug }, result: "created" });
    expect(made.body.link).toBe(`#/invite/${made.body.invitation.code}`);
    const id = made.body.organization.id;
    expect((await owner`SELECT count(*)::int AS n FROM machine_sizes WHERE organization_id = ${id}`)[0].n).toBeGreaterThan(0);
    expect((await owner`SELECT count(*)::int AS n FROM model_tiers WHERE organization_id = ${id}`)[0].n).toBeGreaterThan(0);
    expect((await owner`SELECT invited_by, role FROM invitations WHERE organization_id = ${id}`)).toEqual([{ invited_by: null, role: "admin" }] as never);
    expect((await as(OPERATOR, "POST", "/v1/admin/organizations", { name: "Again", slug, email: at("first") }, null)).status).toBe(409);
    const listed = await as(OPERATOR, "GET", "/v1/admin/organizations", undefined, null);
    expect(listed.body.organizations.find((o: Json) => o.id === id)).toMatchObject({ members: 0, admins: 0, pendingInvitations: 1 });
    const one = await as(OPERATOR, "GET", `/v1/admin/organizations/${id}?status=all`, undefined, null);
    expect(one.body.invitations.map((i: Json) => i.email)).toEqual([at("first")]);
    await owner`DELETE FROM organizations WHERE id = ${id}`;
  });

  test("makes someone admin where nobody is, and removes members of organisations they are not in", async () => {
    const lonely = `${acme}_lonely`;
    await owner`INSERT INTO organizations (id, name, slug) VALUES (${lonely}, 'Lonely', ${lonely})`;
    const lee = await member(owner, lonely, at("lee"));
    const mo = await member(owner, lonely, at("mo"));
    expect((await as(at("lee"), "PATCH", `/v1/people/${lee}`, { role: "admin" }, lonely)).status).toBe(403);
    const promoted = await as(OPERATOR, "PATCH", `/v1/admin/people/${lee}`, { role: "admin" }, null);
    expect(promoted).toMatchObject({ status: 200, body: { person: { id: lee, role: "admin" } } });
    expect((await as(OPERATOR, "DELETE", `/v1/admin/organizations/${lonely}/people/${mo}`, undefined, null)).status).toBe(204);
    // The last admin rule holds for the super-admin too.
    expect((await as(OPERATOR, "DELETE", `/v1/admin/organizations/${lonely}/people/${lee}`, undefined, null)).status).toBe(409);
    expect((await as(OPERATOR, "PATCH", `/v1/admin/people/${lee}`, { role: "member" }, null)).status).toBe(409);
  });

  test("sets the email of someone who signs in only with a key, linking an account", async () => {
    const keyed = await createApiKey({ organizationId: acme, name: "Seeded admin" });
    const set = await as(OPERATOR, "PATCH", `/v1/admin/people/${keyed.personId}`, { email: at("seeded") }, null);
    expect(set.status).toBe(200);
    const got = await as(at("seeded"), "GET", "/v1/me", undefined, null);
    expect(got.body).toMatchObject({ gate: "member", person: { id: keyed.personId } });
  });

  test("a key, or a member who is not one, is refused every admin route", async () => {
    const key = await createApiKey({ organizationId: acme, personId: adminId, name: "Admin key" });
    for (const [method, path] of [["GET", "/v1/admin/organizations"], ["POST", "/v1/admin/organizations"],
      ["GET", `/v1/admin/organizations/${acme}`], ["PATCH", `/v1/admin/people/${adminId}`]] as const) {
      const res = await router.handle(new Request(ORIGIN + path, { method,
        headers: { authorization: `Bearer ${key.key}`, "content-type": "application/json" }, ...(method === "GET" ? {} : { body: "{}" }) }));
      expect({ path, status: res.status }).toEqual({ path, status: 403 });
    }
    expect((await as(at("ana"), "GET", "/v1/admin/organizations", undefined, null)).status).toBe(403);
  });
});

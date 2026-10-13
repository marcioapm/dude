import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { createApiKey } from "../src/api/auth.ts";
import type { Router } from "../src/api/router.ts";
import { closePool, setPool } from "../src/db/client.ts";
import { Edge, OPERATOR, ORIGIN, cookie, databases, fullRouter, invitation, member, rsaKey, sign, type Key } from "./access-harness.ts";

const suffix = Bun.randomUUIDv7("hex").slice(-12);
const org = `org_gate_${suffix}`;
const other = `${org}_other`;
const at = (local: string) => `${local}@${suffix}.gate.test`;
let owner: SQL;
let app: SQL;
let signer: Key;
let router: Router;
let adminId: string;
type Json = Record<string, unknown> & { error?: { code: string; message: string } };

beforeAll(async () => {
  ({ owner, app } = databases());
  setPool(app);
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${org}, 'Acme Gate', ${org}), (${other}, 'Northwind Gate', ${other})`;
  signer = await rsaKey("k1");
  const edge = new Edge();
  edge.keys = [signer.jwk];
  edge.profiles.set(at("stranger"), { email: at("stranger"), name: "Verified profile" });
  edge.profiles.set(OPERATOR, { email: OPERATOR, name: "Verified profile" });
  router = fullRouter(edge);
  adminId = await member(owner, org, at("member"), { name: "Member", role: "admin", photoUrl: "https://img.example/m.png" });
  await invitation(owner, org, at("invited"));
});
afterAll(async () => {
  await closePool(app);
  await owner`DELETE FROM organizations WHERE id IN (${org}, ${other})`;
  await owner`DELETE FROM accounts WHERE email LIKE ${`%@${suffix}.gate.test`}`;
  await owner.end();
});

async function call(email: string, path = "/v1/me", headers: Record<string, string> = {}) {
  const res = await router.handle(new Request(ORIGIN + path, { headers: { ...cookie(await sign(signer, { email })), ...headers } }));
  return { status: res.status, body: await res.json().catch(() => null) as Json };
}

describe("the four lines", () => {
  test("member, invited, super-admin and none, each with exactly its contract's shape", async () => {
    expect((await call(at("member"))).body.gate).toBe("member");
    const invited = (await call(at("invited"))).body as Json & { invitations: Array<Record<string, unknown>> };
    expect(invited.gate).toBe("invited");
    expect(Object.keys(invited).sort()).toEqual(["email", "gate", "invitations", "name", "photoUrl", "superAdmin"]);
    expect(invited.invitations).toHaveLength(1);
    expect(invited.invitations[0]).toMatchObject({ organization: { id: org, name: "Acme Gate", slug: org }, role: "member", inviter: "the operator" });
    expect((await call(OPERATOR)).body).toEqual({ gate: "super_admin", email: OPERATOR, name: "Verified profile", photoUrl: null, superAdmin: true });
    expect((await call(at("stranger"))).body).toEqual({ gate: "none", email: at("stranger"), name: "Verified profile", photoUrl: null });
    expect(await owner`SELECT id FROM accounts WHERE email = ${at("stranger")}`).toHaveLength(0);
  });

  test("the first true line wins: a super-admin who is a member is a member, with their flag", async () => {
    const edge = new Edge();
    edge.keys = [signer.jwk];
    const r = fullRouter(edge, { super_admins: [at("member")] });
    const res = await r.handle(new Request(`${ORIGIN}/v1/me`, { headers: cookie(await sign(signer, { email: at("member") })) }));
    expect(await res.json()).toMatchObject({ gate: "member", superAdmin: true });
  });

  test("none and invited are refused before any lookup, with nothing about anyone in the error", async () => {
    const task = `wi_gate_${suffix}`;
    for (const email of [at("stranger"), at("invited")]) {
      const code = email === at("stranger") ? "not_admitted" : "not_a_member";
      for (const path of ["/v1/people", "/v1/navigation", `/v1/tasks/${task}`, "/v1/tasks/wi_missing", "/v1/unregistered",
        "/v1/me/keys", "/v1/locate/wi_missing", "/v1/admin/organizations", "/v1/organization/invitations"]) {
        const got = await call(email, path, { "x-dude-organization": org });
        expect({ path, status: got.status, code: got.body.error?.code }).toEqual({ path, status: 403, code });
        const text = JSON.stringify(got.body);
        for (const secret of ["Acme Gate", "Northwind Gate", org, OPERATOR, at("member"), "Member"]) expect(text).not.toContain(secret);
      }
    }
    // Writes are refused the same way, and write nothing.
    const res = await router.handle(new Request(`${ORIGIN}/v1/me`, { method: "PATCH",
      headers: { ...cookie(await sign(signer, { email: at("stranger") })), origin: ORIGIN, "content-type": "application/json" },
      body: JSON.stringify({ name: "Sneaky" }) }));
    expect(res.status).toBe(403);
    expect(await owner`SELECT id FROM accounts WHERE email = ${at("stranger")}`).toHaveLength(0);
  });

  test("an expired or revoked invitation stops admission at the next request, with no history", async () => {
    const code = await invitation(owner, org, at("lapsing"));
    expect((await call(at("lapsing"))).body.gate).toBe("invited");
    await owner`UPDATE invitations SET expires_at = now() WHERE code = ${code}`;
    expect((await call(at("lapsing"))).body).toEqual({ gate: "none", email: at("lapsing"), name: null, photoUrl: null });
    await owner`UPDATE invitations SET expires_at = now() + interval '14 days', revoked_at = now() WHERE code = ${code}`;
    expect((await call(at("lapsing"))).body.gate).toBe("none");
  });
});

describe("the organisation of a request", () => {
  test("a member's header names theirs, another's is refused, and a missing one picks the only one", async () => {
    expect((await call(at("member"), "/v1/people")).status).toBe(200);
    expect((await call(at("member"), "/v1/people", { "x-dude-organization": org })).status).toBe(200);
    const refused = await call(at("member"), "/v1/people", { "x-dude-organization": other });
    expect(refused).toEqual({ status: 403, body: { error: { code: "not_a_member", message: "not_a_member" } } });
    // The event stream takes it as org=.
    const stream = await router.handle(new Request(`${ORIGIN}/v1/events/stream?live=1&org=${other}`,
      { headers: cookie(await sign(signer, { email: at("member") })) }));
    expect(stream.status).toBe(403);
  });

  test("with several memberships a missing header is organization_required", async () => {
    const email = at("two");
    await member(owner, org, email);
    const second = await member(owner, other, email);
    expect((await call(email, "/v1/people")).body.error?.code).toBe("organization_required");
    const people = await call(email, "/v1/people", { "x-dude-organization": other });
    expect(people.status).toBe(200);
    expect((people.body as { you: string }).you).toBe(second);
    // Account routes need none.
    expect((await call(email, "/v1/me")).status).toBe(200);
  });

  test("a key implies its organisation; a header naming another is a 403", async () => {
    const key = await createApiKey({ organizationId: org, personId: adminId, name: "CLI" });
    const keyed = (headers: Record<string, string>) =>
      router.handle(new Request(`${ORIGIN}/v1/people`, { headers: { authorization: `Bearer ${key.key}`, ...headers } }));
    expect((await keyed({})).status).toBe(200);
    expect((await keyed({ "x-dude-organization": org })).status).toBe(200);
    expect((await keyed({ "x-dude-organization": other })).status).toBe(403);
  });
});

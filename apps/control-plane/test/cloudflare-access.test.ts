import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { SignJWT, exportJWK, generateKeyPair } from "jose";
import { accessAuthenticator, accessProfiles, accessVerifier, type FetchLike } from "../src/api/cloudflareAccess.ts";
import { createApiKey, requestAuthenticator } from "../src/api/auth.ts";
import { buildRouter } from "../src/index.ts";
import { closePool, setPool } from "../src/db/client.ts";

const suffix = crypto.randomUUID();
const org = `org_access_${suffix}`, other = `${org}_other`, account = `acc_${suffix}`;
const origin = "https://dude.example.test", issuer = "https://dudetest.cloudflareaccess.com";
let owner: SQL, app: SQL, privateKey: CryptoKey;
let fetchEdge: FetchLike;
let token: (email: string, audience?: string, expiration?: number) => Promise<string>;
beforeAll(async () => {
  owner = new SQL(process.env.DATABASE_URL!);
  const url = new URL(process.env.DATABASE_URL!); url.username = url.password = "dude_app";
  app = new SQL(url.toString()); setPool(app);
  for (const id of [org, other]) await owner`INSERT INTO organizations (id, name, slug) VALUES (${id}, ${id}, ${id})`;
  await owner`INSERT INTO accounts (id, email, name) VALUES (${account}, 'member@example.test', 'Member')`;
  await owner`INSERT INTO people (id, organization_id, account_id, email, name, role)
    VALUES (${`per_${suffix}`}, ${org}, ${account}, 'member@example.test', 'Member', 'admin')`;
  await owner`INSERT INTO invitations (id, organization_id, code, email, role)
    VALUES (${`inv_${suffix}`}, ${org}, ${suffix}, 'invited@example.test', 'member')`;
  const pair = await generateKeyPair("RS256", { extractable: true }); privateKey = pair.privateKey;
  const jwk = { ...await exportJWK(pair.publicKey), kid: "k1", alg: "RS256" };
  fetchEdge = async (url, init) => {
    if (url === `${issuer}/cdn-cgi/access/certs`) return Response.json({ keys: [jwk] });
    if (url === `${issuer}/cdn-cgi/access/get-identity`) {
      const raw = new Headers(init?.headers).get("cookie")!.slice("CF_Authorization=".length);
      const email = JSON.parse(Buffer.from(raw.split(".")[1]!, "base64url").toString()).email;
      return Response.json({ email, name: "Verified profile" });
    }
    throw new Error("unexpected edge endpoint");
  };
  token = (email, audience = "aud", expiration = Math.floor(Date.now()/1000)+600) =>
    new SignJWT({ email, sub: email, type: "app" }).setProtectedHeader({ alg: "RS256", kid: "k1" })
      .setIssuer(issuer).setAudience(audience).setExpirationTime(expiration).sign(privateKey);
});
afterAll(async () => {
  await closePool(app);
  await owner`DELETE FROM organizations WHERE id IN (${org}, ${other})`;
  await owner`DELETE FROM accounts WHERE id = ${account}`; await owner.end();
});
async function body(response: Response): Promise<any> { return response.json(); }
const router = () => buildRouter(undefined, requestAuthenticator(accessAuthenticator({
  provider: "cloudflare_access", public_url: origin, super_admins: ["operator@example.test"],
  cloudflare_access: { team: "dudetest", aud: "aud" },
}, "", { verify: accessVerifier("dudetest", "aud", fetchEdge), profile: accessProfiles("dudetest", fetchEdge) })));
async function call(email: string, path = "/v1/me", headers: Record<string,string> = {}) {
  return router().handle(new Request(origin+path, { headers: { cookie: `CF_Authorization=${await token(email)}`, ...headers } }));
}
test("JWT issuer/audience/expiry/signature and origin remain mandatory", async () => {
  const verify = accessVerifier("dudetest", "aud", fetchEdge);
  expect(await verify(await token("member@example.test"))).not.toBeNull();
  expect(await verify(await token("member@example.test", "wrong"))).toBeNull();
  expect(await verify(await token("member@example.test", "aud", 1))).toBeNull();
  expect(await verify("garbage")).toBeNull();
  expect((await call("member@example.test", "/v1/me", { "sec-fetch-site": "cross-site" })).status).toBe(403);
});
test("all four gate lines and exact pre-membership privacy", async () => {
  expect((await body(await call("member@example.test"))).gate).toBe("member");
  const invited = await body(await call("invited@example.test"));
  expect(invited.gate).toBe("invited"); expect(invited.invitations).toHaveLength(1);
  expect(invited).not.toHaveProperty("organizations"); expect(invited).not.toHaveProperty("account");
  expect(await (await call("operator@example.test")).json()).toEqual({ gate: "super_admin", email: "operator@example.test",
    name: "Verified profile", photoUrl: null, superAdmin: true });
  expect(await (await call("stranger@example.test")).json()).toEqual({ gate: "none", email: "stranger@example.test",
    name: "Verified profile", photoUrl: null });
  expect(await owner`SELECT id FROM accounts WHERE email = 'stranger@example.test'`).toHaveLength(0);
});
test("none and invited refuse existing and missing resources before lookup", async () => {
  for (const email of ["stranger@example.test", "invited@example.test"]) {
    for (const path of ["/v1/people", "/v1/projects/missing", "/v1/unregistered", "/v1/me/keys", "/v1/admin/organizations"]) {
      const res = await call(email, path); expect(res.status).toBe(403);
      expect((await body(res)).error.code).toBe(email.startsWith("stranger") ? "not_admitted" : "not_a_member");
    }
  }
});
test("organisation selection checks memberships and key mismatches", async () => {
  expect((await call("member@example.test", "/v1/people")).status).toBe(200);
  expect((await call("member@example.test", "/v1/people", { "X-Dude-Organization": other })).status).toBe(403);
  expect((await call("member@example.test", "/v1/people", { "X-Dude-Organization": org })).status).toBe(200);
  await owner`INSERT INTO people (id, organization_id, account_id, email, name)
    VALUES (${`per_other_${suffix}`}, ${other}, ${account}, 'member@example.test', 'Member')`;
  expect((await call("member@example.test", "/v1/people")).status).toBe(400);
  const key = await createApiKey({ organizationId: org, personId: `per_${suffix}`, name: "CLI" });
  const res = await router().handle(new Request(origin+"/v1/people", { headers: {
    authorization: `Bearer ${key.key}`, "X-Dude-Organization": other } }));
  expect(res.status).toBe(403);
});
test("revoked or expired invitations immediately stop admission without history", async () => {
  await owner`UPDATE invitations SET expires_at = now() WHERE id = ${`inv_${suffix}`}`;
  expect((await body(await call("invited@example.test"))).gate).toBe("none");
  await owner`UPDATE invitations SET expires_at = now() + interval '14 days', revoked_at = now() WHERE id = ${`inv_${suffix}`}`;
  expect((await body(await call("invited@example.test"))).gate).toBe("none");
});

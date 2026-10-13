/**
 * What the Access suites share: a stand-in for the team's certs and identity
 * endpoints, token signing, and members made as migration 118 or an accepted
 * invitation leaves them (an account, and an active person linked to it).
 */

import { SQL } from "bun";
import { SignJWT, exportJWK, generateKeyPair, type JWK } from "jose";
import { requestAuthenticator } from "../src/api/auth.ts";
import { accessAuthenticator, accessProfiles, accessVerifier, type AccessDeps, type FetchLike } from "../src/api/cloudflareAccess.ts";
import { Router } from "../src/api/router.ts";
import { registerPeopleRoutes } from "../src/api/routes/people.ts";
import { registerEventRoutes } from "../src/api/routes/events.ts";
import { registerInvitationRoutes } from "../src/api/routes/invitations.ts";
import { registerAdminRoutes } from "../src/api/routes/admin.ts";
import { registerNavigationRoutes } from "../src/api/routes/navigation.ts";
import { buildRouter } from "../src/index.ts";
import type { AccessConfig } from "../src/config.ts";

export const TEAM = "dudetest";
export const ISSUER = `https://${TEAM}.cloudflareaccess.com`;
export const AUD = "app-audience";
export const ORIGIN = "https://dude.example.test";
export const OPERATOR = "operator@example.test";

export type Key = { privateKey: CryptoKey; jwk: JWK };
export async function rsaKey(kid: string): Promise<Key> {
  const { privateKey, publicKey } = await generateKeyPair("RS256", { extractable: true });
  return { privateKey, jwk: { ...(await exportJWK(publicKey)), kid, alg: "RS256", use: "sig" } };
}

/** Stands in for the team's certs and identity endpoints; nothing leaves the process. */
export class Edge {
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

export async function sign(key: Key, claims: Record<string, unknown>, opts: {
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

export const accessConfig = (over: Partial<AccessConfig> = {}): AccessConfig => ({
  provider: "cloudflare_access", public_url: ORIGIN, super_admins: [OPERATOR],
  cloudflare_access: { team: TEAM, aud: AUD }, ...over,
});

/** The Access routes' router, with `wrapProfile` to hold or fail a profile fetch. */
export function accessRouter(edge: Edge, over: Partial<AccessConfig> = {},
  wrapProfile: (profile: AccessDeps["profile"]) => AccessDeps["profile"] = (p) => p) {
  const router = new Router(requestAuthenticator(accessAuthenticator(accessConfig(over), "", {
    verify: accessVerifier(TEAM, AUD, edge.fetch),
    profile: wrapProfile(accessProfiles(TEAM, edge.fetch)),
  })));
  registerPeopleRoutes(router);
  registerEventRoutes(router);
  registerInvitationRoutes(router);
  registerAdminRoutes(router);
  registerNavigationRoutes(router);
  return router;
}

/** The whole backend's routes behind Access, as index.ts serves them. */
export function fullRouter(edge: Edge, over: Partial<AccessConfig> = {}) {
  return buildRouter(undefined, requestAuthenticator(accessAuthenticator(accessConfig(over), "", {
    verify: accessVerifier(TEAM, AUD, edge.fetch), profile: accessProfiles(TEAM, edge.fetch),
  })));
}

export const cookie = (token: string) => ({ cookie: `other=1; CF_Authorization=${token}` });

export type MeBody = {
  gate: string; email?: string; name?: string | null; photoUrl?: string | null; superAdmin?: boolean;
  person: { id: string; name: string; role: string; photoUrl: string | null };
  organizations?: Array<{ id: string; slug: string; role: string; personId: string; waitingOnYou: number }>;
  invitations?: unknown[]; authMethod: string; logoutUrl?: string;
};
export const me = async (router: Router, headers: Record<string, string>) => {
  const res = await router.handle(new Request(`${ORIGIN}/v1/me`, { headers }));
  return { status: res.status, body: res.status === 200 ? await res.json() as MeBody : null };
};

export function databases() {
  const owner = new SQL(process.env.DATABASE_URL!);
  const url = new URL(process.env.DATABASE_URL!);
  url.username = "dude_app";
  url.password = "dude_app";
  return { owner, app: new SQL(url.toString()) };
}

/**
 * A member of `organizationId` as an accepted invitation (or migration 118)
 * leaves one: the account for `email`, made if needed, and its active person.
 */
export async function member(owner: SQL, organizationId: string, email: string,
  opts: { id?: string; name?: string; role?: "admin" | "member"; photoUrl?: string | null } = {}): Promise<string> {
  const lower = email.toLowerCase();
  const name = opts.name ?? lower;
  const [account] = await owner`INSERT INTO accounts (id, email, name, photo_url)
    VALUES (${`acc_${crypto.randomUUID()}`}, ${lower}, ${name}, ${opts.photoUrl ?? null})
    ON CONFLICT (email) DO UPDATE SET email = EXCLUDED.email RETURNING id, name, photo_url`;
  const id = opts.id ?? `per_${crypto.randomUUID()}`;
  await owner`INSERT INTO people (id, organization_id, account_id, name, email, role, photo_url)
    VALUES (${id}, ${organizationId}, ${account.id}, ${account.name}, ${lower}, ${opts.role ?? "member"}, ${account.photo_url})`;
  return id;
}

/** A pending invitation of `email` to `organizationId`; returns its code. */
export async function invitation(owner: SQL, organizationId: string, email: string, role: "admin" | "member" = "member") {
  const code = crypto.randomUUID().replaceAll("-", "");
  await owner`INSERT INTO invitations (id, organization_id, code, email, role)
    VALUES (${`inv_${crypto.randomUUID()}`}, ${organizationId}, ${code}, ${email.toLowerCase()}, ${role})`;
  return code;
}

// Polls `ready` every 20 ms, failing after `ms`: for states only the database can report.
export async function waitFor(ready: () => Promise<boolean>, ms = 5_000) {
  const until = Date.now() + ms;
  while (!(await ready())) {
    if (Date.now() > until) throw new Error("condition not reached");
    await Bun.sleep(20);
  }
}

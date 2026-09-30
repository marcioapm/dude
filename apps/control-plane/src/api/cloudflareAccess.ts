/**
 * Sign-in through Cloudflare Access.
 *
 * Access sits in front of the web app and hands the browser a signed JWT
 * (`CF_Authorization` cookie; `Cf-Access-Jwt-Assertion` header when the
 * request passed through the edge). The API does not rely on the edge having
 * checked it: every request's token is verified here against the team's
 * published keys, issuer and application audience, and the verified email
 * is then matched to one of the configured organization's people.
 *
 * Nothing here trusts a plain email header, an unverified claim, or the
 * request's own idea of its host.
 */

import { createRemoteJWKSet, customFetch, jwtVerify, type JWTPayload } from "jose";
import { newId } from "@dude/domain";
import { type OrgScope, withOrg } from "../db/client.ts";
import type { AccessConfig } from "../config.ts";
import type { Principal } from "./auth.ts";
import { HttpError } from "./http.ts";

export const ACCESS_LOGOUT_PATH = "/cdn-cgi/access/logout";

// A token whose nbf/exp is this close is still accepted: clocks drift.
const CLOCK_TOLERANCE_S = 30;
const JWKS_TIMEOUT_MS = 5_000;
const JWKS_COOLDOWN_MS = 30_000;
const JWKS_MAX_AGE_MS = 10 * 60_000;
const PROFILE_TIMEOUT_MS = 3_000;
const PROFILE_MAX_BYTES = 64 * 1024;
const PROFILE_TTL_MS = 60 * 60_000;
const PROFILE_FAILURE_TTL_MS = 60_000;
const PROFILE_CACHE_ENTRIES = 1_000;

export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

export interface VerifiedIdentity {
  email: string;
  /** Seconds since the epoch. */
  expiresAt: number;
}

export interface Profile {
  name: string | null;
  picture: string | null;
}

export function teamOrigin(team: string): string {
  return `https://${team}.cloudflareaccess.com`;
}

// One plain ASCII mailbox, as Access's identity providers issue them.
const EMAIL = /^[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+@[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)+$/;

/**
 * Verifies Access tokens: RS256 only, signed by a key the team publishes,
 * this team's issuer, this application's audience, an expiry. Null for any
 * token that is not a person's — a service token carries `common_name` and
 * no email. A key-set fetch that fails is a refusal, never a pass.
 */
export function accessVerifier(team: string, aud: string, fetchImpl: FetchLike = fetch,
  cooldownMs = JWKS_COOLDOWN_MS) {
  const issuer = teamOrigin(team);
  const keys = createRemoteJWKSet(new URL(`${issuer}/cdn-cgi/access/certs`), {
    timeoutDuration: JWKS_TIMEOUT_MS,
    cooldownDuration: cooldownMs,
    cacheMaxAge: JWKS_MAX_AGE_MS,
    [customFetch]: (url: string, init: RequestInit) => fetchImpl(url, init),
  });
  return async (token: string): Promise<VerifiedIdentity | null> => {
    let payload: JWTPayload;
    try {
      ({ payload } = await jwtVerify(token, keys, {
        algorithms: ["RS256"],
        issuer,
        audience: aud,
        requiredClaims: ["exp", "sub", "email"],
        clockTolerance: CLOCK_TOLERANCE_S,
      }));
    } catch {
      // The reason (expired, bad signature, key set unreachable) stays here:
      // it helps an attacker more than a user.
      return null;
    }
    const email = payload.email;
    if ("common_name" in payload || typeof email !== "string" || !EMAIL.test(email) || !payload.sub) return null;
    if (payload.type !== undefined && payload.type !== "app") return null;
    return { email: email.toLowerCase(), expiresAt: payload.exp! };
  };
}

/**
 * A person's name and picture from Access's identity endpoint, asked with
 * their own verified token. Only used to fill in a profile, so any failure
 * is simply no profile; a response about someone else is ignored. Cached
 * per email, briefly after a failure.
 */
export function accessProfiles(team: string, fetchImpl: FetchLike = fetch, now: () => number = Date.now) {
  const url = `${teamOrigin(team)}/cdn-cgi/access/get-identity`;
  const cache = new Map<string, { profile: Profile | null; until: number }>();
  const remember = (email: string, profile: Profile | null, ttl: number) => {
    cache.delete(email);
    if (cache.size >= PROFILE_CACHE_ENTRIES) cache.delete(cache.keys().next().value!);
    cache.set(email, { profile, until: now() + ttl });
    return profile;
  };
  // One fetch per email at a time: concurrent first requests share it.
  const pending = new Map<string, Promise<Profile | null>>();
  const ask = async (token: string, email: string): Promise<Profile | null> => {
    try {
      const response = await fetchImpl(url, {
        headers: { cookie: `CF_Authorization=${token}` },
        redirect: "manual",
        signal: AbortSignal.timeout(PROFILE_TIMEOUT_MS),
      });
      const text = response.ok ? await response.text() : "";
      if (!response.ok || text.length > PROFILE_MAX_BYTES) return remember(email, null, PROFILE_FAILURE_TTL_MS);
      const body = JSON.parse(text) as { email?: unknown; name?: unknown; oidc_fields?: { picture?: unknown } };
      if (typeof body.email !== "string" || body.email.toLowerCase() !== email) {
        return remember(email, null, PROFILE_FAILURE_TTL_MS);
      }
      const name = typeof body.name === "string" && body.name.trim() ? body.name.trim().slice(0, 100) : null;
      return remember(email, { name, picture: httpsUrl(body.oidc_fields?.picture) }, PROFILE_TTL_MS);
    } catch {
      return remember(email, null, PROFILE_FAILURE_TTL_MS);
    }
  };
  return (token: string, email: string): Promise<Profile | null> => {
    const hit = cache.get(email);
    if (hit && hit.until > now()) return Promise.resolve(hit.profile);
    const inFlight = pending.get(email);
    if (inFlight) return inFlight;
    const started = ask(token, email).finally(() => pending.delete(email));
    pending.set(email, started);
    return started;
  };
}

function httpsUrl(value: unknown): string | null {
  if (typeof value !== "string" || value.length > 2000) return null;
  try {
    const url = new URL(value);
    return url.protocol === "https:" && /^https:\/\/[^\s"'<>]+$/.test(value) ? value : null;
  } catch {
    return null;
  }
}

/** The Access token a request carries: the edge's header, else the cookie. */
export function accessToken(request: Request): string | null {
  const header = request.headers.get("cf-access-jwt-assertion");
  if (header !== null) return header;
  for (const part of (request.headers.get("cookie") ?? "").split(";")) {
    const eq = part.indexOf("=");
    if (eq > 0 && part.slice(0, eq).trim() === "CF_Authorization") return part.slice(eq + 1).trim();
  }
  return null;
}

const SAFE_METHODS = new Set(["GET", "HEAD", "OPTIONS"]);

/**
 * Refuse a request the browser made on another site's behalf. The browser
 * attaches the Access cookie (and the edge the header) to any request to
 * this host, so an Access identity alone does not show the person meant it.
 * A change must come from the app's own origin, as `Origin` says; a read
 * may be a direct visit but not another site's or a preview's fetch.
 */
export function checkBrowserOrigin(request: Request, publicOrigin: string): void {
  const site = request.headers.get("sec-fetch-site");
  if (SAFE_METHODS.has(request.method)) {
    if (site === "cross-site" || site === "same-site") throw crossSite();
    return;
  }
  if (request.headers.get("origin") !== publicOrigin) throw crossSite();
  if (site !== null && site !== "same-origin") throw crossSite();
}

const crossSite = () => new HttpError(403, "request refused: not from this site", "cross_site");

export interface AccessDeps {
  verify: (token: string) => Promise<VerifiedIdentity | null>;
  profile: (token: string, email: string) => Promise<Profile | null>;
}

export type Member = { personId: string; name: string; role: "admin" | "member" };
export type Membership = Member | "removed" | "unknown";

interface PersonRow {
  id: string;
  removed: boolean;
  name: string;
  role: "admin" | "member";
  email: string;
  photoUrl: string | null;
  photoKey: string | null;
}

// people_by_email (migration 059) reads the scope's tenant by index; row-level security would not.
const findByEmail = async (scope: OrgScope, email: string) => (await scope.sql`
  SELECT id, removed, name, role, email, photo_url AS "photoUrl", photo_key AS "photoKey"
  FROM people_by_email(${email})`) as PersonRow[];

const member = (row: { id: string; name: string; role: string }): Member =>
  ({ personId: row.id, name: row.name, role: row.role === "admin" ? "admin" : "member" });

/**
 * The organization's person with `email`, making them a member if nobody
 * has it and `autoCreate` allows. Removed people stay removed: only an
 * admin's invitation brings someone back. Creation takes the same lock as
 * any change to the organization's people, so a first sign-in and a
 * removal, or two first sign-ins, take turns.
 *
 * The identity endpoint can take seconds, so `profile` is only ever awaited
 * between transactions: a short read decides whether it is needed, and a
 * second short transaction rechecks membership before writing anything.
 */
export async function memberFor(organizationId: string, email: string, autoCreate: boolean,
  profile: () => Promise<Profile | null>): Promise<Membership> {
  const rows = await withOrg(organizationId, (scope) => findByEmail(scope, email));
  const active = rows.find((r) => !r.removed);
  if (active) {
    const wantsName = active.name.trim() === "" || active.name.toLowerCase() === active.email.toLowerCase();
    const wantsPhoto = !active.photoUrl && !active.photoKey;
    if (!wantsName && !wantsPhoto) return member(active);
    const got = await profile();
    const name = wantsName ? got?.name ?? null : null;
    const picture = wantsPhoto ? got?.picture ?? null : null;
    return withOrg(organizationId, async (scope) => {
      // Rechecked in the statement: the person may have been removed, or
      // set their own name or photo, while the profile was fetched.
      const kept = !name && !picture
        ? await scope.sql`SELECT id, name, role FROM people WHERE id = ${active.id} AND removed_at IS NULL`
        : await scope.sql`
        UPDATE people SET
          name = CASE WHEN ${name}::text IS NOT NULL AND (btrim(name) = '' OR lower(name) = lower(email::text))
                      THEN ${name}::text ELSE name END,
          photo_url = CASE WHEN photo_url IS NULL AND photo_key IS NULL THEN ${picture}::text ELSE photo_url END
        WHERE id = ${active.id} AND removed_at IS NULL
        RETURNING id, name, role`;
      return kept[0] ? member(kept[0]) : "removed";
    });
  }
  if (rows.length > 0) return "removed";
  if (!autoCreate) return "unknown";

  const seed = await profile();
  return withOrg(organizationId, async (scope) => {
    await scope.sql`SELECT pg_advisory_xact_lock(hashtext('people:' || ${scope.organizationId}))`;
    const now = await findByEmail(scope, email);
    const current = now.find((r) => !r.removed);
    if (current) return member(current);
    // Removed while the profile was fetched: stays removed, never recreated.
    if (now.length > 0) return "removed";
    const personId = newId("person");
    const name = seed?.name ?? email;
    await scope.sql`
      INSERT INTO people (id, organization_id, name, email, role, photo_url)
      VALUES (${personId}, ${scope.organizationId}, ${name}, ${email}, 'member', ${seed?.picture ?? null})`;
    return { personId, name, role: "member" };
  });
}

/**
 * Who a request is, by its Access token, or null. Throws 403 for a
 * cross-site request before anything is verified or written.
 */
export function accessAuthenticator(config: AccessConfig, organizationId: string, deps: AccessDeps) {
  return async (request: Request): Promise<Principal | null> => {
    const token = accessToken(request);
    if (!token) return null;
    checkBrowserOrigin(request, config.public_url);
    const identity = await deps.verify(token);
    if (!identity) return null;
    const membership = await memberFor(organizationId, identity.email, config.auto_create,
      () => deps.profile(token, identity.email));
    if (typeof membership === "string") return null;
    // Name and role come from the membership just read or written, so the
    // router need not resolve the person again.
    return {
      credentialKind: "person", organizationId, personId: membership.personId, kind: "user",
      name: membership.name, role: membership.role, expiresAt: identity.expiresAt, resolved: true,
    };
  };
}

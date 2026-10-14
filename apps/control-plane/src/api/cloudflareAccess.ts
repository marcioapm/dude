/**
 * Sign-in through Cloudflare Access.
 *
 * Access sits in front of the web app and hands the browser a signed JWT
 * (`CF_Authorization` cookie; `Cf-Access-Jwt-Assertion` header when the
 * request passed through the edge). The API does not rely on the edge having
 * checked it: every request's token is verified here against the team's
 * published keys, issuer and application audience, and the verified email
 * is then admitted by the gate (docs/design/accounts.md, "The sign-in gate"):
 * a member, an invitee, a configured super-admin, or nobody.
 *
 * Nothing here trusts a plain email header, an unverified claim, or the
 * request's own idea of its host.
 */

import { createRemoteJWKSet, customFetch, jwtVerify, type JWTPayload } from "jose";
import { withoutTenant } from "../db/client.ts";
import type { AccessConfig } from "../config.ts";
import type { AccessAdmission, Principal } from "./auth.ts";
import { HttpError } from "./http.ts";
import { GITHUB_CALLBACK_PATH } from "./routes/githubOAuth.ts";

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

/** The gate's four lines for one verified email, read in one short transaction. */
async function admissionFor(config: AccessConfig, email: string): Promise<AccessAdmission> {
  return withoutTenant(async ({ sql }) => {
    const [account] = (await sql`SELECT id, email::text, name, photo_url AS "photoUrl", photo_key AS "photoKey"
      FROM accounts WHERE email = ${email}::citext`) as Array<NonNullable<AccessAdmission["account"]>>;
    const memberships = (account ? await sql`SELECT * FROM account_memberships(${account.id})` : []) as AccessAdmission["memberships"];
    const invitations = (await sql`SELECT invitations_for_email(${email}::citext) AS invitation`) as Array<{ invitation: unknown }>;
    const superAdmin = config.super_admins.includes(email);
    const gate = memberships.length ? "member" : invitations.length ? "invited" : superAdmin ? "super_admin" : "none";
    return { gate, email, superAdmin, account: account ?? null, memberships,
      invitations: invitations.map((r) => r.invitation), profile: null };
  });
}

// An account's name is a placeholder while it is empty or only the email.
const wantsProfile = (a: NonNullable<AccessAdmission["account"]>) =>
  a.name.trim() === "" || a.name.toLowerCase() === a.email.toLowerCase() || (!a.photoUrl && !a.photoKey);

/**
 * Who a request is, by its Access token, or null. Throws 403 for a
 * cross-site request before anything is verified or written.
 *
 * The identity endpoint can take seconds, so the profile is only ever
 * awaited between transactions, and only when something needs it: a
 * member's placeholder name or missing photo, or the pre-membership pages,
 * which show it. Admission is then read again, so a removal, revocation or
 * acceptance that committed meanwhile decides this request.
 */
export function accessAuthenticator(config: AccessConfig, _organizationId: string, deps: AccessDeps) {
  return async (request: Request): Promise<Principal | null> => {
    const token = accessToken(request);
    if (!token) return null;
    const url = new URL(request.url);
    // GitHub's redirect back is another site's navigation, naming no organisation: its proof is the
    // single-use state bound to the person who started it, and the route finds that organisation itself.
    const githubReturn = request.method === "GET" && url.pathname === GITHUB_CALLBACK_PATH;
    if (!githubReturn) checkBrowserOrigin(request, config.public_url);
    const identity = await deps.verify(token);
    if (!identity) return null;
    let admission = await admissionFor(config, identity.email);
    const frontDoor = url.pathname === "/v1/me" || url.pathname.startsWith("/v1/invitations/");
    const fill = admission.gate === "member" && admission.account !== null && wantsProfile(admission.account);
    if (fill || (admission.gate !== "member" && frontDoor)) {
      const profile = await deps.profile(token, identity.email);
      if (fill && profile) {
        // Only blanks are filled, rechecked in the statement, and only while
        // the account still has an active membership.
        await withoutTenant(({ sql }) => sql`
          UPDATE accounts SET
            name = CASE WHEN ${profile.name}::text IS NOT NULL AND (btrim(name) = '' OR lower(name) = lower(email::text))
                        THEN ${profile.name}::text ELSE name END,
            photo_url = CASE WHEN photo_url IS NULL AND photo_key IS NULL THEN ${profile.picture}::text ELSE photo_url END
          WHERE id = ${admission.account!.id} AND EXISTS (SELECT 1 FROM account_memberships(accounts.id))
            AND ((${profile.name}::text IS NOT NULL AND (btrim(name) = '' OR lower(name) = lower(email::text)))
              OR (${profile.picture}::text IS NOT NULL AND photo_url IS NULL AND photo_key IS NULL))`);
      }
      admission = await admissionFor(config, identity.email);
      admission.profile = profile;
    }
    const accountRoute = url.pathname === "/v1/me" || url.pathname.startsWith("/v1/me/keys") ||
      url.pathname.startsWith("/v1/invitations/") || url.pathname.startsWith("/v1/admin/") ||
      url.pathname.startsWith("/v1/locate/") || githubReturn;
    const requested = request.headers.get("x-dude-organization") ??
      (url.pathname === "/v1/events/stream" ? url.searchParams.get("org") : null);
    let selected: AccessAdmission["memberships"][number] | undefined;
    if (requested) {
      selected = admission.memberships.find((m) => m.organization_id === requested);
    } else if (admission.memberships.length === 1 || (accountRoute && url.pathname !== "/v1/me")) {
      selected = admission.memberships[0];
    }
    if (admission.gate === "member" && !selected && (requested ? !url.pathname.startsWith("/v1/admin/") : !accountRoute)) {
      throw requested ? new HttpError(403, "not_a_member", "not_a_member")
        : new HttpError(400, "organization_required", "organization_required");
    }
    if (admission.gate === "member") {
      // At most once an hour; a lost touch is harmless.
      await withoutTenant(({ sql }) => sql`UPDATE accounts SET signed_in_at = now()
        WHERE id = ${admission.account!.id} AND (signed_in_at IS NULL OR signed_in_at < now() - interval '1 hour')`)
        .catch(() => {});
    }
    return { credentialKind: "person", organizationId: selected?.organization_id ?? "",
      personId: selected?.person_id ?? "", kind: "user", name: selected?.name ?? identity.email,
      role: selected?.role ?? "member", expiresAt: identity.expiresAt, resolved: true, access: admission };
  };
}

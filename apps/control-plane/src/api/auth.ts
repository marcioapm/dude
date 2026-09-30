/**
 * API-key authentication and organization scoping.
 *
 * Every request resolves to an (organization, principal) pair before it can
 * touch tenant data. Keys are stored only as SHA-256 hashes; the plaintext
 * is shown once at creation and never persisted.
 */

import { createHash, randomBytes } from "node:crypto";
import { newId } from "@dude/domain";
import { type OrgScope, withOrg, withoutTenant } from "../db/client.ts";

export const KEY_PREFIX = "dude_sk_";

/** Only user keys remain: runner keys belonged to the retired Go runner. */
export type PrincipalKind = "user";

interface PersonIdentity {
  organizationId: string;
  personId: string;
  kind: PrincipalKind;
  name: string;
  role: "admin" | "member";
}

export type Principal = PersonIdentity & (
  | { credentialKind: "api_key"; apiKeyId: string }
  | { credentialKind: "person" }
);

export function auditActor(principal: Principal): { kind: "human" | "person"; id: string } {
  return principal.credentialKind === "api_key"
    ? { kind: "human", id: principal.apiKeyId }
    : { kind: "person", id: principal.personId };
}

export async function personPrincipal(organizationId: string, personId: string): Promise<Principal | null> {
  return withOrg(organizationId, async ({ sql }) => {
    const rows = await sql`SELECT name, role FROM people
      WHERE id = ${personId} AND organization_id = ${organizationId} AND removed_at IS NULL`;
    const row = rows[0];
    if (!row) return null;
    return { credentialKind: "person", organizationId, personId, kind: "user",
      name: row.name, role: row.role };
  });
}

export function hashKey(key: string): string {
  return createHash("sha256").update(key).digest("hex");
}

export function generateApiKey(): { key: string; keyHash: string; keyPrefix: string } {
  const key = KEY_PREFIX + randomBytes(24).toString("base64url");
  return { key, keyHash: hashKey(key), keyPrefix: key.slice(0, 16) };
}

/**
 * Create an API key and return the plaintext exactly once. For `personId`,
 * or — with none — for a new person named as the key is (migration 035).
 */
export async function createApiKey(params: {
  organizationId: string;
  name: string;
  kind?: PrincipalKind;
  personId?: string;
}): Promise<{ id: string; key: string; personId: string }> {
  // api_keys is tenant-scoped, so creation runs inside the owning org.
  return withOrg(params.organizationId, (scope) => insertApiKey(scope, params));
}

/** `createApiKey` inside a transaction already scoped to the organization. */
export async function insertApiKey(
  scope: OrgScope,
  params: { name: string; kind?: PrincipalKind; personId?: string },
): Promise<{ id: string; key: string; personId: string; keyPrefix: string }> {
  const { key, keyHash, keyPrefix } = generateApiKey();
  const id = newId("apiKey");
  const rows = (await scope.sql`
    INSERT INTO api_keys (id, organization_id, name, key_hash, key_prefix, kind, person_id)
    VALUES (${id}, ${scope.organizationId}, ${params.name}, ${keyHash}, ${keyPrefix},
            ${params.kind ?? "user"}, ${params.personId ?? null})
    RETURNING person_id AS "personId"`) as Array<{ personId: string }>;
  return { id, key, personId: rows[0]!.personId, keyPrefix };
}

/**
 * Add a person to the organization `scope` is in, with a first key to sign
 * in with (its plaintext returned this once). Null when someone there
 * already has that email.
 */
export async function insertPerson(
  scope: OrgScope,
  person: { name: string; email: string; role?: "admin" | "member" | undefined; lastSeenWhere?: string | null },
): Promise<{ personId: string; keyId: string; key: string } | null> {
  const taken = await scope.sql`SELECT 1 FROM people WHERE email = ${person.email} AND removed_at IS NULL`;
  if (taken.length > 0) return null;
  const personId = newId("person");
  const where = person.lastSeenWhere ?? null;
  await scope.sql`
    INSERT INTO people (id, organization_id, name, email, role, last_seen_at, last_seen_where)
    VALUES (${personId}, ${scope.organizationId}, ${person.name}, ${person.email}, ${person.role ?? "member"},
            ${where ? new Date() : null}, ${where})`;
  const { id, key } = await insertApiKey(scope, { name: person.name, personId });
  return { personId, keyId: id, key };
}

/**
 * Resolve a bearer token to a principal.
 *
 * Returns null for unknown, revoked or malformed keys — callers must not
 * distinguish these cases to the client.
 */
export async function authenticate(authorization: string | null): Promise<Principal | null> {
  if (!authorization) return null;

  const token = authorization.startsWith("Bearer ") ? authorization.slice(7).trim() : authorization.trim();
  if (!token.startsWith(KEY_PREFIX)) return null;

  const candidateHash = hashKey(token);

  // Tenant-blind lookup: the organization is unknown until the key resolves.
  // lookup_api_key is SECURITY DEFINER and matches on the full hash only.
  const rows = await withoutTenant(async ({ sql }) => {
    return (await sql`
      SELECT id, organization_id, name, kind, person_id, role
      FROM lookup_api_key(${candidateHash})`) as Array<{
      id: string;
      organization_id: string;
      name: string;
      kind: PrincipalKind;
      person_id: string | null;
      role: string | null;
    }>;
  });

  const row = rows[0];
  // A runner key left over from the retired runner protocol authenticates
  // nothing (migration 014 revokes them; this holds for any that remain).
  if (!row || row.kind !== "user" || !row.person_id) return null;

  // Best-effort usage tracking; never fail a request because it did not stick.
  void withoutTenant(async ({ sql }) => {
    await sql`SELECT touch_api_key(${row.id})`;
  }).catch(() => {});

  return {
    credentialKind: "api_key",
    organizationId: row.organization_id,
    apiKeyId: row.id,
    personId: row.person_id,
    kind: row.kind,
    name: row.name,
    role: row.role === "admin" ? "admin" : "member",
  };
}

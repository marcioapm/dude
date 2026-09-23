/**
 * API-key authentication and organization scoping.
 *
 * Every request resolves to an (organization, principal) pair before it can
 * touch tenant data. Keys are stored only as SHA-256 hashes; the plaintext
 * is shown once at creation and never persisted.
 */

import { createHash, randomBytes } from "node:crypto";
import { newId } from "@dude/domain";
import { withOrg, withoutTenant } from "../db/client.ts";

export const KEY_PREFIX = "dude_sk_";

/** Only user keys remain: runner keys belonged to the retired Go runner. */
export type PrincipalKind = "user";

export interface Principal {
  organizationId: string;
  apiKeyId: string;
  kind: PrincipalKind;
  name: string;
}

export function hashKey(key: string): string {
  return createHash("sha256").update(key).digest("hex");
}

export function generateApiKey(): { key: string; keyHash: string; keyPrefix: string } {
  const key = KEY_PREFIX + randomBytes(24).toString("base64url");
  return { key, keyHash: hashKey(key), keyPrefix: key.slice(0, 16) };
}

/** Create an API key and return the plaintext exactly once. */
export async function createApiKey(params: {
  organizationId: string;
  name: string;
  kind?: PrincipalKind;
}): Promise<{ id: string; key: string }> {
  const { key, keyHash, keyPrefix } = generateApiKey();
  const id = newId("apiKey");

  // api_keys is tenant-scoped, so creation runs inside the owning org.
  await withOrg(params.organizationId, async ({ sql }) => {
    await sql`
      INSERT INTO api_keys (id, organization_id, name, key_hash, key_prefix, kind)
      VALUES (${id}, ${params.organizationId}, ${params.name}, ${keyHash}, ${keyPrefix},
              ${params.kind ?? "user"})`;
  });

  return { id, key };
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
      SELECT id, organization_id, name, kind
      FROM lookup_api_key(${candidateHash})`) as Array<{
      id: string;
      organization_id: string;
      name: string;
      kind: PrincipalKind;
    }>;
  });

  const row = rows[0];
  // A runner key left over from the retired runner protocol authenticates
  // nothing (migration 014 revokes them; this holds for any that remain).
  if (!row || row.kind !== "user") return null;

  // Best-effort usage tracking; never fail a request because it did not stick.
  void withoutTenant(async ({ sql }) => {
    await sql`SELECT touch_api_key(${row.id})`;
  }).catch(() => {});

  return {
    organizationId: row.organization_id,
    apiKeyId: row.id,
    kind: row.kind,
    name: row.name,
  };
}

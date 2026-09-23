/**
 * Seed an organization and its keys for the local demo.
 *
 * Organizations and the first API key are provisioned, not self-served, so
 * this is a deliberate out-of-band step rather than an API call.
 *
 * Prints JSON on stdout: the organization and a user key to sign in with.
 */

import { SQL } from "bun";
import { newId } from "@dude/domain";
import { createApiKey } from "../apps/control-plane/src/api/auth.ts";
import { closePool, setPool } from "../apps/control-plane/src/db/client.ts";

const ownerDsn = process.env.OWNER_DSN;
const appDsn = process.env.DATABASE_URL;

if (!ownerDsn || !appDsn) {
  console.error("OWNER_DSN and DATABASE_URL are required");
  process.exit(1);
}

const owner = new SQL(ownerDsn);
const organizationId = newId("organization");
const slug = `demo-${Date.now().toString(36)}`;

try {
  await owner`
    INSERT INTO organizations (id, name, slug, default_agent_models)
    VALUES (${organizationId}, 'Demo', ${slug},
            ${{ reviewer: { model: "anthropic/claude-sonnet-5" } }}::jsonb)`;

  // Keys are created through the app role, so the same row-level security
  // that protects production applies to the demo's data too.
  setPool(new SQL(appDsn));

  const userKey = await createApiKey({ organizationId, name: "demo user" });

  console.log(JSON.stringify({ organizationId, userKey: userKey.key }));
} finally {
  await owner.end();
  await closePool();
}

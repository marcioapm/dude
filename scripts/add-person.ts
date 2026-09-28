/**
 * Add a person to an organization, with a key to sign in with.
 *
 * Admins invite people from Organization → Members; this is for the
 * operator, before anyone can: the first admin of a provisioned
 * organization, or someone to let back in.
 *
 *   DATABASE_URL=postgres://dude_app:…/dude \
 *     bun run scripts/add-person.ts <organizationId> "<name>" <email> [admin|member]
 *
 * Prints JSON on stdout: the person and their key (shown this once).
 */

import { SQL } from "bun";
import { newId } from "@dude/domain";
import { insertApiKey } from "../apps/control-plane/src/api/auth.ts";
import { closePool, setPool, withOrg } from "../apps/control-plane/src/db/client.ts";

const [organizationId, name, email, role = "member"] = process.argv.slice(2);
const appDsn = process.env.DATABASE_URL;

if (!appDsn || !organizationId || !name || !email || (role !== "admin" && role !== "member")) {
  console.error('usage: DATABASE_URL=… bun run scripts/add-person.ts <organizationId> "<name>" <email> [admin|member]');
  process.exit(1);
}

// Through the app role, so row-level security holds here as in the API.
setPool(new SQL(appDsn));
try {
  const out = await withOrg(organizationId, async (scope) => {
    const taken = await scope.sql`SELECT 1 FROM people WHERE email = ${email} AND removed_at IS NULL`;
    if (taken.length > 0) throw new Error(`${email} is already a member of ${organizationId}`);
    const personId = newId("person");
    await scope.sql`
      INSERT INTO people (id, organization_id, name, email, role)
      VALUES (${personId}, ${organizationId}, ${name}, ${email}, ${role})`;
    const { key } = await insertApiKey(scope, { name, personId });
    return { personId, name, email, role, key };
  });
  console.log(JSON.stringify(out));
} catch (err) {
  console.error(err instanceof Error ? err.message : err);
  process.exitCode = 1;
} finally {
  await closePool();
}

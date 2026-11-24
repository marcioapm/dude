// Makes PERSON the owner of TASK in ORG through the control plane's own
// PATCH /v1/tasks/:id (Inbox's Take over), as PERSON with a key of theirs,
// against DATABASE_URL (the app role). Prints the response's status.
//
//	bun run orchestrator/testdata/take-over.ts ORG PERSON TASK
import { createApiKey } from "../../apps/control-plane/src/api/auth.ts";
import { closePool } from "../../apps/control-plane/src/db/client.ts";
import { Config, useConfig } from "../../apps/control-plane/src/config.ts";
import { buildRouter } from "../../apps/control-plane/src/index.ts";

const [org, person, task] = process.argv.slice(2);
useConfig(Config.load({ env: { ...process.env } }));
const { key } = await createApiKey({ organizationId: org!, name: "take over", personId: person! });
const res = await buildRouter("").handle(new Request(`https://dude.test/v1/tasks/${task}`, {
  method: "PATCH", headers: { "content-type": "application/json", authorization: `Bearer ${key}` }, body: JSON.stringify({ ownerId: person }),
}));
process.stdout.write(String(res.status));
if (res.status !== 200) process.stderr.write(await res.text());
await closePool();

// Makes PERSON the owner of TASK in ORG through the control plane's own
// PATCH /v1/tasks/:id (Inbox's Take over), as PERSON, against DATABASE_URL
// (the app role). Prints the response's status.
//
//	bun run orchestrator/testdata/take-over.ts ORG PERSON TASK
import { closePool } from "../../apps/control-plane/src/db/client.ts";
import { Config, useConfig } from "../../apps/control-plane/src/config.ts";
import { buildRouter } from "../../apps/control-plane/src/index.ts";

const [org, person, task] = process.argv.slice(2);
useConfig(Config.load({ env: { ...process.env } }));
const router = buildRouter("", async () => ({ credentialKind: "person", organizationId: org!, personId: person!, kind: "user", name: person!, role: "member", resolved: true }));
const res = await router.handle(new Request(`https://dude.test/v1/tasks/${task}`, {
  method: "PATCH", headers: { "content-type": "application/json" }, body: JSON.stringify({ ownerId: person }),
}));
process.stdout.write(String(res.status));
if (res.status !== 200) process.stderr.write(await res.text());
await closePool();

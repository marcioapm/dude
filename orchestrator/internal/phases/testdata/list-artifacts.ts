// Prints GET /v1/artifacts?taskId=TASK as the control plane answers it, for
// ORG, against DATABASE_URL (the app role): the listing's real query over
// what the orchestrator recorded.
//
//	bun run orchestrator/internal/phases/testdata/list-artifacts.ts ORG TASK
import { createApiKey } from "../../../../apps/control-plane/src/api/auth.ts";
import { Config, useConfig } from "../../../../apps/control-plane/src/config.ts";
import { closePool } from "../../../../apps/control-plane/src/db/client.ts";
import { buildRouter } from "../../../../apps/control-plane/src/index.ts";

const [org, task] = process.argv.slice(2);
useConfig(Config.load({ env: process.env }));
const { key } = await createApiKey({ organizationId: org!, name: "lister" });
const res = await buildRouter("").handle(new Request(`http://dude.test/v1/artifacts?taskId=${encodeURIComponent(task!)}`, {
  headers: { authorization: `Bearer ${key}` },
}));
process.stdout.write(await res.text());
await closePool();
if (!res.ok) process.exit(1);

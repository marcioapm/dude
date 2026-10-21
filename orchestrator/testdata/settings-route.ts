// Prints what the control plane's settings route answers a member of ORG
// for PATH (/v1/settings/organization or /v1/projects/:id/settings),
// against DATABASE_URL (the app role) and the orchestrator at
// DUDE_ORCHESTRATOR_URL.
//
//	bun run orchestrator/testdata/settings-route.ts ORG PERSON PATH
import { Config, useConfig } from "../../apps/control-plane/src/config.ts";
import { closePool } from "../../apps/control-plane/src/db/client.ts";
import { Router } from "../../apps/control-plane/src/api/router.ts";
import { registerSettingsRoutes } from "../../apps/control-plane/src/api/routes/settings.ts";
import { registerModelRoutes } from "../../apps/control-plane/src/api/routes/models.ts";
import { registerJudgementRoutes } from "../../apps/control-plane/src/api/routes/judgements.ts";
import { modelTierInputSchema } from "../../packages/domain/src/tiers.ts";

const [org, person, path] = process.argv.slice(2);
useConfig(Config.load({ env: { ...process.env, DUDE_AUTH_PROVIDER: "cloudflare_access", DUDE_AUTH_PUBLIC_URL: "https://dude.test",
  DUDE_AUTH_DEFAULT_ORGANIZATION: "test", DUDE_AUTH_CLOUDFLARE_ACCESS_TEAM: "test", DUDE_AUTH_CLOUDFLARE_ACCESS_AUD: "aud" } }));
const router = new Router(async () => ({ credentialKind: "person", organizationId: org!, personId: person!, kind: "user", name: person!, role: "member", resolved: true }));
registerSettingsRoutes(router);
registerModelRoutes(router);
registerJudgementRoutes(router);
if (path === "/schemas/tier") {
  process.stdout.write(JSON.stringify(Object.fromEntries(Object.keys(modelTierInputSchema.shape).map(k => [k, true]))));
  await closePool();
  process.exit(0);
}
const res = await router.handle(new Request(`https://dude.test${path}`));
process.stdout.write(await res.text());
await closePool();

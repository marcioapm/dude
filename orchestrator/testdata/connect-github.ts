// Completes Connect GitHub for PERSON in ORG through the control plane's own
// /v1/me/github/connect and /v1/me/github/callback routes, against
// DATABASE_URL (the app role). GitHub's token exchange and GET /user are
// answered locally with TOKEN and GITHUB_ID; prints the callback's landing.
//
//	bun run orchestrator/testdata/connect-github.ts ORG PERSON TOKEN GITHUB_ID
import { Config, useConfig } from "../../apps/control-plane/src/config.ts";
import { closePool } from "../../apps/control-plane/src/db/client.ts";
import { Router } from "../../apps/control-plane/src/api/router.ts";
import { registerGithubOAuthRoutes } from "../../apps/control-plane/src/api/routes/githubOAuth.ts";

const [org, person, token, githubId] = process.argv.slice(2);
useConfig(Config.load({ env: { ...process.env, DUDE_AUTH_PROVIDER: "cloudflare_access", DUDE_AUTH_PUBLIC_URL: "https://dude.test",
  DUDE_AUTH_DEFAULT_ORGANIZATION: "test", DUDE_AUTH_CLOUDFLARE_ACCESS_TEAM: "test", DUDE_AUTH_CLOUDFLARE_ACCESS_AUD: "aud" } }));
const router = new Router(async () => ({ credentialKind: "person", organizationId: org!, personId: person!, kind: "user", name: person!, role: "member", resolved: true }));
registerGithubOAuthRoutes(router, (async (input: string | URL | Request) => String(input).endsWith("/login/oauth/access_token")
  ? Response.json({ access_token: token, refresh_token: `${token}-refresh`, expires_in: 28800, refresh_token_expires_in: 15552000 })
  : Response.json({ id: Number(githubId), login: person })) as typeof fetch);
const started = await router.handle(new Request("https://dude.test/v1/me/github/connect", { method: "POST" }));
const state = new URL(((await started.json()) as { url: string }).url).searchParams.get("state");
const landed = await router.handle(new Request(`https://dude.test/v1/me/github/callback?state=${state}&code=code`));
process.stdout.write(landed.headers.get("location") ?? "");
await closePool();

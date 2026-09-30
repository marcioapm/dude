/**
 * The backend: dude's public API.
 *
 * Display and management — projects, tasks, settings, the board, the
 * live event stream, search — and the front door for everything else. What
 * changes what runs (delivering, steering, pausing, resuming, aborting) is
 * forwarded to the orchestrator (orchestrator/, Go), which does all
 * background work. This process runs no background loops of its own.
 */

import { Router } from "./api/router.ts";
import { type RequestAuthenticator, authenticate, requestAuthenticator } from "./api/auth.ts";
import { type FetchLike, accessAuthenticator, accessProfiles, accessVerifier } from "./api/cloudflareAccess.ts";
import { type AuthConfig, Config, config, organizationBySlug, useConfig } from "./config.ts";
import { json } from "./api/http.ts";
import { registerEventRoutes } from "./api/routes/events.ts";
import { registerNavigationRoutes } from "./api/routes/navigation.ts";
import { registerPullRequestRoutes } from "./api/routes/pullRequests.ts";
import { registerFindingRoutes } from "./api/routes/findings.ts";
import { registerArtifactRoutes } from "./api/routes/artifacts.ts";
import { registerProjectRoutes } from "./api/routes/projects.ts";
import { registerWorkRoutes } from "./api/routes/work.ts";
import { registerStructureRoutes } from "./api/routes/structure.ts";
import { registerInterventionRoutes } from "./api/routes/intervention.ts";
import { registerPushRoutes } from "./api/routes/push.ts";
import { registerPeopleRoutes } from "./api/routes/people.ts";
import { registerMetricsRoutes } from "./api/routes/metrics.ts";
import { registerSettingsRoutes } from "./api/routes/settings.ts";
import { registerProjectPageRoutes } from "./api/routes/projectPage.ts";
import { registerLiveRoutes } from "./api/routes/live.ts";
import { registerServerRoutes } from "./api/routes/servers.ts";
import { registerMemoryRoutes } from "./api/routes/memory.ts";
import { webApp } from "./api/web.ts";
import { version } from "./build.ts";
import { closePool, getPool } from "./db/client.ts";
import { listenForEvents } from "./events/listen.ts";
import { s3RuntimeProblem } from "./storage.ts";

export function buildRouter(webDir = config().webDir, auth: RequestAuthenticator = authenticate): Router {
  const router = new Router(auth);

  router.publicRoute("GET", "/health", async () => {
    try {
      await getPool()`SELECT 1`;
      return json({ status: "ok" });
    } catch (err) {
      return json({ status: "degraded", error: String(err) }, 503);
    }
  });

  registerEventRoutes(router);
  registerNavigationRoutes(router);
  registerPullRequestRoutes(router);
  registerMemoryRoutes(router);
  registerFindingRoutes(router);
  registerArtifactRoutes(router);
  registerProjectRoutes(router);
  registerWorkRoutes(router);
  registerStructureRoutes(router);
  registerInterventionRoutes(router);
  registerPushRoutes(router);
  registerPeopleRoutes(router);
  registerMetricsRoutes(router);
  registerSettingsRoutes(router);
  registerProjectPageRoutes(router);
  registerLiveRoutes(router);
  registerServerRoutes(router);

  if (webDir) router.fallback(webApp(webDir));

  return router;
}

/**
 * How requests authenticate under `auth`: API keys alone, or API keys and
 * Cloudflare Access. Resolves the configured organization, so a name that
 * matches none stops startup rather than every sign-in.
 */
export async function authFor(auth: AuthConfig, fetchImpl: FetchLike = fetch): Promise<RequestAuthenticator> {
  if (auth.provider === "api_key") return authenticate;
  const organizationId = await organizationBySlug(auth.default_organization);
  const { team, aud } = auth.cloudflare_access;
  return requestAuthenticator(accessAuthenticator(auth, organizationId, {
    verify: accessVerifier(team, aud, fetchImpl),
    profile: accessProfiles(team, fetchImpl),
  }));
}

/** The router the backend serves under `settings`: its web app and its [auth]. */
export async function routerFor(settings: Config, fetchImpl: FetchLike = fetch): Promise<Router> {
  return buildRouter(settings.webDir, await authFor(settings.auth, fetchImpl));
}

export function startServer(port = config().port, router: Router = buildRouter()) {
  const server = Bun.serve({
    port,
    // SSE streams are long-lived; the default idle timeout would cut them off.
    idleTimeout: 0,
    fetch: (request) => router.handle(request),
  });

  return server;
}

if (import.meta.main) {
  if (process.argv.includes("--version")) {
    console.log(version);
    process.exit(0);
  }
  let settings: Config;
  try {
    settings = Config.load();
  } catch (err) {
    console.error(`configuration: ${(err as Error).message}`);
    process.exit(1);
  }
  if (settings.path) console.log(`configuration file read: ${settings.path}`);
  for (const warning of settings.warnings) console.warn(`configuration: ${warning}`);
  useConfig(settings);
  const databaseUrl = settings.databaseUrl;
  if (!databaseUrl) {
    console.error("database.url (DATABASE_URL) is required");
    process.exit(1);
  }
  // Refused here, not at the first upload: that fails a person days later.
  const runtimeProblem = settings.string("DUDE_S3_BUCKET") ? s3RuntimeProblem(Bun.version) : undefined;
  if (runtimeProblem) {
    console.error(`configuration: ${runtimeProblem}`);
    process.exit(1);
  }

  let router: Router;
  try {
    router = await routerFor(settings);
  } catch (err) {
    console.error(`configuration: ${(err as Error).message}`);
    process.exit(1);
  }
  const server = startServer(settings.port, router);
  console.log(`backend listening on http://localhost:${server.port}`);

  // Events are written by the orchestrator as well as here; the live stream
  // learns of them from the database.
  const stopListening = await listenForEvents(databaseUrl);

  const shutdown = async (signal: string) => {
    console.log(`\n${signal} received, shutting down`);
    await stopListening();
    await server.stop();
    await closePool();
    process.exit(0);
  };
  process.on("SIGINT", () => void shutdown("SIGINT"));
  process.on("SIGTERM", () => void shutdown("SIGTERM"));
}

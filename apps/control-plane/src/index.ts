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
import { webApp } from "./api/web.ts";
import { version } from "./build.ts";
import { closePool, getPool } from "./db/client.ts";
import { listenForEvents } from "./events/listen.ts";

export function buildRouter(webDir = process.env.DUDE_WEB_DIR): Router {
  const router = new Router();

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

  if (webDir) router.fallback(webApp(webDir));

  return router;
}

export function startServer(port = Number(process.env.PORT ?? 3000)) {
  const router = buildRouter();

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
  const databaseUrl = process.env.DATABASE_URL;
  if (!databaseUrl) {
    console.error("DATABASE_URL is required");
    process.exit(1);
  }

  const server = startServer();
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

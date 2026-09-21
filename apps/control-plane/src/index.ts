/**
 * Control plane entry point.
 *
 * A modular monolith on Bun (plan §21): API, webhook handlers, event
 * ingestion, live UI transport and background pollers in one process, with
 * PostgreSQL providing persistence, coordination and search.
 */

import { Router } from "./api/router.ts";
import { json } from "./api/http.ts";
import { registerEventRoutes } from "./api/routes/events.ts";
import { registerProjectRoutes } from "./api/routes/projects.ts";
import { registerWorkRoutes } from "./api/routes/work.ts";
import { closePool, getPool } from "./db/client.ts";

export function buildRouter(): Router {
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
  registerProjectRoutes(router);
  registerWorkRoutes(router);

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
  if (!process.env.DATABASE_URL) {
    console.error("DATABASE_URL is required");
    process.exit(1);
  }

  const server = startServer();
  console.log(`control plane listening on http://localhost:${server.port}`);

  const shutdown = async (signal: string) => {
    console.log(`\n${signal} received, shutting down`);
    await server.stop();
    await closePool();
    process.exit(0);
  };
  process.on("SIGINT", () => void shutdown("SIGINT"));
  process.on("SIGTERM", () => void shutdown("SIGTERM"));
}

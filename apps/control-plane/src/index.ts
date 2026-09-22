/**
 * Control plane entry point.
 *
 * A modular monolith on Bun (plan §21): API, webhook handlers, event
 * ingestion, live UI transport and background sweepers in one process, with
 * PostgreSQL providing persistence, coordination and search.
 */

import { Router } from "./api/router.ts";
import { json } from "./api/http.ts";
import { registerEventRoutes } from "./api/routes/events.ts";
import { registerNavigationRoutes } from "./api/routes/navigation.ts";
import { registerProjectRoutes } from "./api/routes/projects.ts";
import { registerWorkRoutes } from "./api/routes/work.ts";
import { registerRunnerRoutes } from "./api/routes/runner.ts";
import { registerInterventionRoutes } from "./api/routes/intervention.ts";
import { closePool, getPool } from "./db/client.ts";
import { PostgresWorkflowRuntime } from "./workflow/runtime.ts";
import { Sweeper, dispatchOutbox, reapExpiredRunLeases, reapLostWorkers } from "./workflow/sweepers.ts";

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
  registerNavigationRoutes(router);
  registerProjectRoutes(router);
  registerWorkRoutes(router);
  registerRunnerRoutes(router);
  registerInterventionRoutes(router);

  return router;
}

/**
 * The background work the control plane owns.
 *
 * Without these, the durable workflow runtime is inert: nothing wakes a parked
 * workflow, nothing dispatches the outbox, and work abandoned by a dead worker
 * stays abandoned. Plan §21's "waiting is free" depends on something doing the
 * waking.
 */
export function buildSweepers(options: { log?: typeof console.log } = {}) {
  const log = options.log ?? console.log;
  const workflow = new PostgresWorkflowRuntime();

  const sweepers = [
    new Sweeper("workflow-poller", async () => ({ handled: await workflow.tick() }), {
      // Short: this is what makes a signalled workflow feel responsive.
      intervalMs: 500,
      log,
    }),
    new Sweeper(
      "outbox-dispatcher",
      // Handlers are registered as integrations land; an unknown kind
      // retries with backoff rather than being silently dropped.
      () => dispatchOutbox({}),
      { intervalMs: 1_000, log },
    ),
    new Sweeper("run-lease-reaper", () => reapExpiredRunLeases(), {
      // Leases are 90s, so checking every few seconds is ample.
      intervalMs: 5_000,
      log,
    }),
    new Sweeper("worker-liveness-reaper", () => reapLostWorkers(), {
      intervalMs: 10_000,
      log,
    }),
  ];

  return {
    workflow,
    start: () => sweepers.forEach((s) => s.start()),
    stop: () => Promise.all(sweepers.map((s) => s.stop())),
  };
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

  const sweepers = buildSweepers({
    log: (message: string, detail?: Record<string, unknown>) =>
      console.log(message, detail ?? ""),
  });
  sweepers.start();
  console.log("background sweepers started");

  const shutdown = async (signal: string) => {
    console.log(`\n${signal} received, shutting down`);
    // Stop taking new work before closing the pool, or an in-flight sweep
    // fails on a dead connection during every shutdown.
    await sweepers.stop();
    await server.stop();
    await closePool();
    process.exit(0);
  };
  process.on("SIGINT", () => void shutdown("SIGINT"));
  process.on("SIGTERM", () => void shutdown("SIGTERM"));
}

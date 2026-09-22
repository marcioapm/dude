/**
 * The process-wide workflow runtime.
 *
 * A route that starts a workflow needs the same runtime instance the sweeper
 * is ticking — a second one would have its own registered definitions and
 * would start workflows nothing advances.
 *
 * A module-level holder rather than a parameter threaded through every route,
 * because the router's handler signature is `(ctx) => Response` and widening
 * it so one endpoint can reach the runtime would cost every other route
 * clarity. Set once at startup; tests set their own.
 */

import type { PostgresWorkflowRuntime } from "./runtime.ts";

let runtime: PostgresWorkflowRuntime | null = null;

export function setWorkflowRuntime(next: PostgresWorkflowRuntime | null): void {
  runtime = next;
}

/**
 * The runtime, or a clear failure.
 *
 * Throwing beats returning null: a route that could start a workflow but
 * silently did not would look like it worked, and the work item would sit
 * there forever.
 */
export function getWorkflowRuntime(): PostgresWorkflowRuntime {
  if (!runtime) {
    throw new Error("workflow runtime is not configured; sweepers must be started first");
  }
  return runtime;
}

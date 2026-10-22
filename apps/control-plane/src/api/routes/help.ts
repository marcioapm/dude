/**
 * Help: dude's own guide, as the orchestrator's build embeds it
 * (orchestrator/internal/guide). Any member reads it; it is the same text
 * agents find with search_memory's type doc, so there is no copy here.
 */

import { orchestrator } from "../../orchestrator/client.ts";
import type { Router } from "../router.ts";

export function registerHelpRoutes(router: Router): void {
  router.get("/v1/help", (ctx) => orchestrator(ctx.principal.organizationId, "GET", "/internal/help", "{}", ctx.principal));
  router.get("/v1/help/:page", (ctx) =>
    orchestrator(ctx.principal.organizationId, "GET", `/internal/help/${encodeURIComponent(ctx.params.page!)}`, "{}", ctx.principal));
}

/**
 * Memory: what people, dude and agents remember, and one search over it and
 * the work (docs/design/memory.md). The orchestrator owns it — the index,
 * the embedder, the search — so these pass through, adding who is asking.
 *
 * Anyone in the organization searches and adds; only an admin reindexes
 * (checked here, as every admin-only route is). A person changes and
 * archives their own, an admin anyone's: whose a memory is, only the
 * orchestrator knows, so it checks that against the principal sent with
 * every call (in headers, never the body).
 */

import { orchestrator } from "../../orchestrator/client.ts";
import { requireOrgAdmin } from "../access.ts";
import type { RequestContext, Router } from "../router.ts";

function forward(method: string, path: (ctx: RequestContext) => string) {
  return async (ctx: RequestContext): Promise<Response> => {
    const target = path(ctx) + (method === "GET" ? ctx.url.search : "");
    return orchestrator(ctx.principal.organizationId, method, target,
      method === "GET" ? "{}" : await ctx.request.text(), ctx.principal);
  };
}

const memory = (ctx: RequestContext) => `/internal/memory/memories/${encodeURIComponent(ctx.params.id!)}`;

export function registerMemoryRoutes(router: Router): void {
  router.get("/v1/memory/search", forward("GET", () => "/internal/memory/search"));
  router.get("/v1/memory/memories", forward("GET", () => "/internal/memory/memories"));
  router.post("/v1/memory/memories", forward("POST", () => "/internal/memory/memories"));
  router.get("/v1/memory/memories/:id", forward("GET", memory));
  router.patch("/v1/memory/memories/:id", forward("PATCH", memory));
  router.post("/v1/memory/memories/:id/archive", forward("POST", (ctx) => `${memory(ctx)}/archive`));
  router.post("/v1/memory/memories/:id/restore", forward("POST", (ctx) => `${memory(ctx)}/restore`));
  router.get("/v1/memory/index", forward("GET", () => "/internal/memory/index"));
  router.post("/v1/memory/index/retry", forward("POST", () => "/internal/memory/index/retry"));
  const reindex = forward("POST", () => "/internal/memory/index/reindex");
  router.post("/v1/memory/index/reindex", async (ctx) => {
    await requireOrgAdmin(ctx);
    return reindex(ctx);
  });
}

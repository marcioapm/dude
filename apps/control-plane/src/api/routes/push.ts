/**
 * Browser notifications (Web Push): a browser subscribes with dude's public
 * key and registers the subscription here; the orchestrator sends to it
 * when something waits on a person (docs/design/notifications.md).
 *
 * A registration is the organisation's the request names: one browser's
 * subscription may be registered in several, each turned on and off alone.
 */

import { z } from "zod";
import { withOrg } from "../../db/client.ts";
import { orchestrator } from "../../orchestrator/client.ts";
import { json, noContent, parseBody } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";

// PushSubscription.toJSON(), as a browser gives it.
const subscriptionSchema = z.object({
  endpoint: z.string().url().startsWith("https://"),
  keys: z.object({ p256dh: z.string().min(1).max(200), auth: z.string().min(1).max(100) }),
});
const endpointSchema = z.object({ endpoint: z.string().min(1) });

/** The key a browser subscribes with: the orchestrator holds the pair. */
async function publicKey(ctx: RequestContext): Promise<Response> {
  return orchestrator(ctx.principal.organizationId, "GET", "/internal/push/key", undefined);
}

/** Register this browser in the organisation the request names; again only refreshes its keys and owner. */
async function subscribe(ctx: RequestContext): Promise<Response> {
  const sub = await parseBody(ctx.request, subscriptionSchema);
  await withOrg(ctx.principal.organizationId, async ({ sql }) => {
    await sql`SELECT claim_push_subscription(${sub.endpoint}, ${ctx.principal.organizationId},
      ${ctx.principal.personId}, ${ctx.principal.credentialKind === "api_key" ? ctx.principal.apiKeyId : null},
      ${sub.keys.p256dh}, ${sub.keys.auth})`;
  });
  return json({ subscribed: true }, 201);
}

/**
 * Whether this browser is registered in the organisation the request names.
 * A POST so the endpoint, which a push service accepts messages at, stays
 * out of URLs and their logs.
 */
async function lookup(ctx: RequestContext): Promise<Response> {
  const { endpoint } = await parseBody(ctx.request, endpointSchema);
  const rows = await withOrg(ctx.principal.organizationId, ({ sql }) =>
    sql`SELECT 1 FROM push_subscriptions WHERE endpoint = ${endpoint} AND organization_id = ${ctx.principal.organizationId}`);
  return json({ subscribed: rows.length > 0 });
}

/** Forget this browser in the organisation the request names; its other organisations keep it. */
async function unsubscribe(ctx: RequestContext): Promise<Response> {
  const { endpoint } = await parseBody(ctx.request, endpointSchema);
  await withOrg(ctx.principal.organizationId, async ({ sql }) => {
    await sql`DELETE FROM push_subscriptions WHERE endpoint = ${endpoint} AND organization_id = ${ctx.principal.organizationId}`;
  });
  return noContent();
}

export function registerPushRoutes(router: Router): void {
  router.get("/v1/push/key", publicKey);
  router.post("/v1/push/subscriptions", subscribe);
  router.post("/v1/push/subscriptions/lookup", lookup);
  router.post("/v1/push/subscriptions/remove", unsubscribe);
}

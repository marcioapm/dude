/**
 * The backend's client for the orchestrator.
 *
 * Everything that changes what runs — delivering a work item, steering,
 * pausing, resuming, aborting — is the orchestrator's decision to carry out.
 * The backend authenticates the user, resolves their organization, and
 * forwards the request with a service token. The orchestrator's refusals use
 * the same error shape as this API's, so they pass straight through.
 */

import { HttpError } from "../api/http.ts";

const TIMEOUT_MS = 15_000;

function config(): { url: string; token: string } {
  const url = process.env.DUDE_ORCHESTRATOR_URL;
  const token = process.env.DUDE_ORCHESTRATOR_TOKEN;
  if (!url || !token) {
    throw new HttpError(503, "the orchestrator is not configured", "unavailable");
  }
  return { url: url.replace(/\/+$/, ""), token };
}

/**
 * Call the orchestrator on behalf of an organization and return its response
 * unchanged — status and body — so the user sees what it decided.
 */
export async function orchestrator(
  organizationId: string,
  method: string,
  path: string,
  body: string = "{}",
  actorId?: string,
): Promise<Response> {
  const { url, token } = config();
  let res: Response;
  try {
    res = await fetch(`${url}${path}`, {
      method,
      signal: AbortSignal.timeout(TIMEOUT_MS),
      headers: {
        authorization: `Bearer ${token}`,
        // Who is asking travels in headers, never in the body, so a
        // request cannot claim to be someone else.
        "x-dude-organization": organizationId,
        ...(actorId ? { "x-dude-actor": actorId } : {}),
        "content-type": "application/json",
      },
      ...(method === "GET" ? {} : { body: body || "{}" }),
    });
  } catch (err) {
    throw new HttpError(503, `the orchestrator is unreachable: ${String(err)}`, "unavailable");
  }
  return new Response(await res.text(), {
    status: res.status,
    headers: { "content-type": "application/json" },
  });
}

/** Tell the orchestrator something changed that it should act on now. */
export async function kickOrchestrator(organizationId: string): Promise<void> {
  try {
    await orchestrator(organizationId, "POST", "/internal/kick");
  } catch {
    // Its loops pick the change up on their next tick anyway.
  }
}

/**
 * The backend's client for the orchestrator.
 *
 * Everything that changes what runs — delivering a task, steering,
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
  const res = await call(organizationId, method, path, {
    signal: AbortSignal.timeout(TIMEOUT_MS),
    headers: { ...(actorId ? { "x-dude-actor": actorId } : {}), "content-type": "application/json" },
    ...(method === "GET" ? {} : { body: body || "{}" }),
  });
  const text = await res.text();
  // A 204 carries no body, and a Response refuses one.
  return new Response(res.status === 204 ? null : text, {
    status: res.status,
    headers: { "content-type": "application/json" },
  });
}

/**
 * Read a file from the orchestrator (an artifact's bytes) as a stream: the
 * response as it came, for the caller to pass on. Only the time to its
 * first byte is limited; a large file may take as long as it takes.
 */
export async function orchestratorStream(organizationId: string, path: string): Promise<Response> {
  const abort = new AbortController();
  const timer = setTimeout(() => abort.abort(), TIMEOUT_MS);
  try {
    return await call(organizationId, "GET", path, { signal: abort.signal });
  } finally {
    clearTimeout(timer);
  }
}

async function call(organizationId: string, method: string, path: string, init: RequestInit): Promise<Response> {
  const { url, token } = config();
  try {
    return await fetch(`${url}${path}`, {
      ...init,
      method,
      headers: {
        ...(init.headers as Record<string, string>),
        authorization: `Bearer ${token}`,
        // Who is asking travels in headers, never in the body, so a
        // request cannot claim to be someone else.
        "x-dude-organization": organizationId,
      },
    });
  } catch (err) {
    throw new HttpError(503, `the orchestrator is unreachable: ${String(err)}`, "unavailable");
  }
}

/** Tell the orchestrator something changed that it should act on now. */
export async function kickOrchestrator(organizationId: string): Promise<void> {
  try {
    await orchestrator(organizationId, "POST", "/internal/kick");
  } catch {
    // Its loops pick the change up on their next tick anyway.
  }
}

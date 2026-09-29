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
import type { Principal } from "../api/auth.ts";

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
  /** Who is asking: a principal (its key, person and role all travel), or a key's id alone. */
  actor?: string | Principal,
): Promise<Response> {
  const res = await call(organizationId, method, path, {
    signal: AbortSignal.timeout(TIMEOUT_MS),
    headers: { ...identity(actor), "content-type": "application/json" },
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

/**
 * Who is asking, as the orchestrator reads it (api.principalOf): the key
 * acting, and for a principal its person and role, so what only the
 * orchestrator can check (whose a memory is) is checked against them.
 */
function identity(actor: string | Principal | undefined): Record<string, string> {
  if (!actor) return {};
  if (typeof actor === "string") return { "x-dude-actor": actor };
  return { "x-dude-actor": actor.apiKeyId, "x-dude-person": actor.personId, "x-dude-role": actor.role };
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

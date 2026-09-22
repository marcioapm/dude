/**
 * Typed client for the control plane's product API.
 *
 * The frontend depends on the product API, never on its host (plan §117).
 * Everything here is plain `fetch` against documented endpoints, so the same
 * code runs in a browser and inside the Tauri shell the plan anticipates —
 * no Electron/Node assumptions, no direct database access.
 */

import type {
  Directive,
  DirectiveScope,
  PauseMode,
  Project,
  Run,
  Session,
  WorkItem,
} from "@dude/domain";

// ---------------------------------------------------------------------------
// Wire types
//
// The API returns domain records, sometimes with their children embedded.
// These aliases add only that nesting: anything else about a Project or a Run
// is the domain package's to say, so the two cannot drift.
// ---------------------------------------------------------------------------

export type { Project, Repository, Run, Session, WorkItem } from "@dude/domain";

/** A work item with its attempts, newest first. `GET /v1/work-items/:id`. */
export interface WorkItemDetail extends WorkItem {
  runs: Run[];
}

/** A Run with the sessions it spawned. `GET /v1/runs/:id`. */
export interface RunDetail extends Run {
  sessions: Session[];
}

/** What the API returns when something goes wrong. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    override readonly message: string,
    readonly details?: unknown,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

/** Query string for the defined params, or "" when there are none. */
function qs(params: Record<string, unknown>): string {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== null) query.set(key, String(value));
  }
  const encoded = query.toString();
  return encoded ? `?${encoded}` : "";
}

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

export interface ApiClientOptions {
  /** Base URL; empty means same-origin, which is the browser and Tauri case. */
  baseUrl?: string;
  apiKey: string;
}

export class ApiClient {
  readonly #baseUrl: string;
  readonly #apiKey: string;

  constructor(options: ApiClientOptions) {
    this.#baseUrl = (options.baseUrl ?? "").replace(/\/$/, "");
    this.#apiKey = options.apiKey;
  }

  async #request<T>(method: string, path: string, body?: unknown): Promise<T> {
    const res = await fetch(`${this.#baseUrl}${path}`, {
      method,
      headers: {
        authorization: `Bearer ${this.#apiKey}`,
        "content-type": "application/json",
      },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });

    const text = await res.text();
    const payload = text ? JSON.parse(text) : null;

    if (!res.ok) {
      const error = payload?.error ?? {};
      throw new ApiError(
        res.status,
        error.code ?? "error",
        error.message ?? `${method} ${path} failed`,
        error.details,
      );
    }
    return payload as T;
  }

  // -- reads --------------------------------------------------------------

  listProjects(): Promise<{ projects: Project[] }> {
    return this.#request("GET", "/v1/projects");
  }

  listWorkItems(params: { projectId?: string; status?: string } = {}): Promise<{
    workItems: WorkItem[];
  }> {
    return this.#request("GET", `/v1/work-items${qs(params)}`);
  }

  getWorkItem(id: string): Promise<WorkItemDetail> {
    return this.#request("GET", `/v1/work-items/${id}`);
  }

  getRun(id: string): Promise<RunDetail> {
    return this.#request("GET", `/v1/runs/${id}`);
  }

  // -- writes -------------------------------------------------------------

  createRun(workItemId: string): Promise<Run> {
    return this.#request("POST", `/v1/work-items/${workItemId}/runs`, {});
  }

  // -- intervention (plan §24) --------------------------------------------

  /** Redirect a running agent. Produces a durable, auditable directive. */
  steer(runId: string, text: string, scope: DirectiveScope = "run"): Promise<Directive> {
    return this.#request("POST", `/v1/runs/${runId}/steer`, { text, scope });
  }

  /**
   * Request a pause. Returns once recorded — the runner confirms separately,
   * so the Run stays `running` until it does.
   */
  pause(runId: string, mode: PauseMode = "graceful", reason?: string): Promise<{ ok: true }> {
    return this.#request("POST", `/v1/runs/${runId}/pause`, { mode, reason });
  }

  resume(runId: string, reason?: string): Promise<{ ok: true }> {
    return this.#request("POST", `/v1/runs/${runId}/resume`, { reason });
  }

  abort(runId: string, reason?: string): Promise<{ ok: true }> {
    return this.#request("POST", `/v1/runs/${runId}/abort`, { reason });
  }

  /**
   * Live event stream for a scope, resuming from `after`.
   *
   * `EventSource` cannot send an Authorization header, so the key travels as
   * a query parameter here. That is acceptable only because the control plane
   * is same-origin in the browser and loopback in the desktop shell; a
   * cross-origin deployment would need a short-lived stream token instead.
   */
  streamUrl(params: {
    after?: number | undefined;
    runId?: string | undefined;
    sessionId?: string | undefined;
  }): string {
    return `${this.#baseUrl}/v1/events/stream${qs({ ...params, key: this.#apiKey })}`;
  }
}

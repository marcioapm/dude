/**
 * Typed client for the control plane's product API.
 *
 * The frontend depends on the product API, never on its host (plan §117).
 * Everything here is plain `fetch` against documented endpoints, so the same
 * code runs in a browser and inside the Tauri shell the plan anticipates —
 * no Electron/Node assumptions, no direct database access.
 */

import type { NavProject } from "@dude/design-system";
import type {
  DeliveryPolicy,
  Directive,
  DirectiveScope,
  Epic,
  Finding,
  Repository,
  PullRequest,
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

export type {
  Epic,
  Finding,
  Project,
  PullRequest,
  Repository,
  Run,
  Session,
  WorkItem,
} from "@dude/domain";

/** A project with its repositories. `GET /v1/projects/:id`. */
export interface ProjectDetail extends Project {
  repositories: Repository[];
  deliveryPolicy: DeliveryPolicy;
}

/** What a work item asks for, and where it sits. */
export interface WorkItemFields {
  title: string;
  goal: string;
  acceptanceCriteria: string[];
  epicId: string | null;
  repositoryId: string | null;
}

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

  /** Every project, epic, work item and agent the sidebar and board draw. */
  navigation(): Promise<{ projects: NavProject[] }> {
    return this.#request("GET", "/v1/navigation");
  }

  listPullRequests(workItemId: string): Promise<{ pullRequests: PullRequest[] }> {
    return this.#request("GET", `/v1/pull-requests${qs({ workItemId })}`);
  }

  listFindings(workItemId: string): Promise<{ findings: Finding[] }> {
    return this.#request("GET", `/v1/findings${qs({ workItemId })}`);
  }

  getProject(id: string): Promise<ProjectDetail> {
    return this.#request("GET", `/v1/projects/${id}`);
  }

  listEpics(projectId: string): Promise<{ epics: Epic[] }> {
    return this.#request("GET", `/v1/projects/${projectId}/epics`);
  }

  // -- writes -------------------------------------------------------------

  createRun(workItemId: string): Promise<Run> {
    return this.#request("POST", `/v1/work-items/${workItemId}/runs`, {});
  }

  createWorkItem(input: { projectId: string } & Partial<WorkItemFields> & { title: string }): Promise<WorkItem> {
    return this.#request("POST", "/v1/work-items", input);
  }

  /** Edit a work item. What it asks for is fixed once delivery starts; where it sits is not. */
  updateWorkItem(id: string, changes: Partial<WorkItemFields>): Promise<WorkItem> {
    return this.#request("PATCH", `/v1/work-items/${id}`, changes);
  }

  createProject(input: {
    name: string;
    slug: string;
    repositories?: Array<{ name: string; url: string; defaultBranch?: string }>;
  }): Promise<ProjectDetail> {
    return this.#request("POST", "/v1/projects", input);
  }

  updateProject(
    id: string,
    changes: Partial<{ name: string; description: string; runtimeImage: string | null;
      agentModels: Project["agentModels"]; deliveryPolicy: DeliveryPolicy }>,
  ): Promise<ProjectDetail> {
    return this.#request("PATCH", `/v1/projects/${id}`, changes);
  }

  addRepository(projectId: string, repo: { name: string; url: string; defaultBranch?: string;
    trust?: Repository["trust"] }): Promise<Repository> {
    return this.#request("POST", `/v1/projects/${projectId}/repositories`, repo);
  }

  updateRepository(id: string, changes: Partial<Omit<Repository, "id" | "projectId">>): Promise<Repository> {
    return this.#request("PATCH", `/v1/repositories/${id}`, changes);
  }

  removeRepository(id: string): Promise<void> {
    return this.#request("DELETE", `/v1/repositories/${id}`);
  }

  createEpic(projectId: string, epic: { title: string; description?: string }): Promise<Epic> {
    return this.#request("POST", `/v1/projects/${projectId}/epics`, epic);
  }

  updateEpic(id: string, changes: Partial<{ title: string; description: string; position: number }>): Promise<Epic> {
    return this.#request("PATCH", `/v1/epics/${id}`, changes);
  }

  deleteEpic(id: string): Promise<void> {
    return this.#request("DELETE", `/v1/epics/${id}`);
  }

  /**
   * Start the delivery workflow: implement, review, fix, simplify, then a
   * pull request. Idempotent — a second call joins the delivery in flight.
   */
  deliver(workItemId: string): Promise<{ workflowRunId: string; alreadyRunning: boolean }> {
    return this.#request("POST", `/v1/work-items/${workItemId}/deliver`, {});
  }

  // -- intervention (plan §24) --------------------------------------------

  /** Answer the question an agent stopped on; the answer starts its next turn. */
  answer(questionId: string, text: string): Promise<{ id: string; status: "answered" }> {
    return this.#request("POST", `/v1/questions/${questionId}/answer`, { text });
  }

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
    workItemId?: string | undefined;
    projectId?: string | undefined;
    /** Skip the backfill and deliver only what happens from now on. */
    live?: boolean | undefined;
  }): string {
    const { live, ...rest } = params;
    return `${this.#baseUrl}/v1/events/stream${qs({ ...rest, ...(live ? { live: 1 } : {}), key: this.#apiKey })}`;
  }
}

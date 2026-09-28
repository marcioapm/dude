/**
 * Typed client for the control plane's product API.
 *
 * The frontend depends on the product API, never on its host (plan §117).
 * Everything here is plain `fetch` against documented endpoints, so the same
 * code runs in a browser and inside the Tauri shell the plan anticipates —
 * no Electron/Node assumptions, no direct database access.
 */

import type { NavProject, NavTask } from "@dude/design-system";
import { escalationWords } from "../escalation.ts";
import type {
  AgentRole,
  TaskRepository,
  DeliveryPolicy,
  Directive,
  DirectiveScope,
  Epic,
  Escalation,
  EscalationAction,
  Finding,
  Repository,
  PullRequest,
  PauseMode,
  Person,
  PersonDetail,
  PersonRef,
  PersonRole,
  Project,
  PersistedEvent,
  Run,
  Session,
  Task,
  EpicState,
  ProjectPromptMode,
  PromptHistory,
  PromptRole,
  SettingsPatch,
  SettingsResponse,
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
  Escalation,
  EscalationAction,
  Finding,
  Person,
  PersonDetail,
  PersonRef,
  PersonRole,
  Project,
  PullRequest,
  Repository,
  Run,
  Session,
  Task,
} from "@dude/domain";

/** A person as `/v1/people` lists them: where they were last seen, too. */
export type Member = PersonDetail & { lastSeenWhere: string | null };

/** One of your keys: its prefix, never the key. */
export interface ApiKeyInfo {
  id: string;
  name: string;
  prefix: string;
  createdAt: string;
  lastUsedAt: string | null;
  /** The key this browser signed in with. */
  current: boolean;
}

/** A project with its repositories. `GET /v1/projects/:id`. */
export interface ProjectDetail extends Project {
  repositories: Repository[];
  deliveryPolicy: DeliveryPolicy;
}

/** The organization's GitHub connection, as settings may show it: never the secret. */
export type ForgeConnection =
  | { connected: false }
  | {
      connected: true;
      auth: "pat" | "github_app";
      secretHint: string;
      apiBaseUrl: string | null;
      webhookPath: string;
      updatedAt: string;
    };

/** What a task asks for, and where it sits. */
export interface TaskFields {
  title: string;
  goal: string;
  acceptanceCriteria: string[];
  epicId: string | null;
  /** The repositories it works on; none is work that changes no code. */
  repositories: TaskRepository[];
}

export type { TaskRepository } from "@dude/domain";

/** A task with its attempts, newest first, and why it waits if delivery stopped for a person. `GET /v1/tasks/:id`. */
export interface TaskDetail extends Task {
  runs: Run[];
  escalation: Escalation | null;
}

/**
 * The navigation tree gives each task's escalation, when delivery stopped
 * for a person; the design system's NavTask shows it as what the task waits
 * for, in words.
 */
function withWaitingFor(projects: NavProject[]): NavProject[] {
  const task = (t: NavTask & { escalation?: Escalation | null }): NavTask =>
    t.escalation ? { ...t, waitingFor: escalationWords(t.escalation).short } : t;
  return projects.map((p) => ({
    ...p,
    ...(p.epics ? { epics: p.epics.map((e) => ({ ...e, tasks: e.tasks.map(task) })) } : {}),
    ...(p.tasks ? { tasks: p.tasks.map(task) } : {}),
  }));
}

/** One epic on its project's page. `GET /v1/projects/:id/overview`. */
export interface EpicOverview {
  id: string;
  title: string;
  description: string;
  position: number;
  state: EpicState;
  /** A person chose the state; otherwise it is what the tasks say. */
  stateSet: boolean;
  tasks: number;
  lanes: { done: number; review: number; progress: number; backlog: number };
  prs: Record<string, number>;
  owners: Array<{ id: string; name: string }>;
  costUsd: number;
  lastActivity: string | null;
  needsYou: number;
  createdAt: string;
  updatedAt: string;
}

export interface ProjectOverview {
  project: { id: string; name: string };
  epics: EpicOverview[];
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

/**
 * What a validation failure is about, in the words the server used for the
 * field — "url must be an https, ssh or git:// URL" says what to change;
 * "request body failed validation" does not.
 */
function fieldMessage(details: unknown): string | undefined {
  const fields = (details as { fieldErrors?: Record<string, string[]> } | undefined)?.fieldErrors;
  const [field, messages] = fields ? Object.entries(fields).find(([, m]) => m.length > 0) ?? [] : [];
  const first = messages?.[0];
  if (typeof first !== "string") return undefined;
  // zod's own messages ("Required") need the field to mean anything.
  return first.toLowerCase().startsWith(field!.toLowerCase()) ? first : `${field}: ${first}`;
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

/** A file an agent published, as the task lists it. `GET /v1/artifacts`. */
export interface Artifact {
  id: string;
  taskId: string;
  runId: string | null;
  name: string;
  contentType: string;
  sizeBytes: number;
  sha256: string;
  epoch: number;
  createdAt: string;
  phase: string | null;
  role: AgentRole | null;
}

export class ApiClient {
  readonly #baseUrl: string;
  readonly #apiKey: string;
  /** What this browser has open, for presence ("TEXT-14"): see `setWhere`. */
  #where = "";

  constructor(options: ApiClientOptions) {
    this.#baseUrl = (options.baseUrl ?? "").replace(/\/$/, "");
    this.#apiKey = options.apiKey;
  }

  /** A request that throws ApiError for a refusal; the caller reads the body. */
  async #fetch(method: string, path: string, body?: unknown): Promise<Response> {
    const res = await fetch(`${this.#baseUrl}${path}`, {
      method,
      headers: {
        authorization: `Bearer ${this.#apiKey}`,
        "content-type": "application/json",
        ...(this.#where ? { "x-dude-where": this.#where } : {}),
      },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
    if (!res.ok) {
      const text = await res.text();
      const error = (text ? JSON.parse(text) : null)?.error ?? {};
      throw new ApiError(
        res.status,
        error.code ?? "error",
        fieldMessage(error.details) ?? error.message ?? `${method} ${path} failed`,
        error.details,
      );
    }
    return res;
  }

  async #request<T>(method: string, path: string, body?: unknown): Promise<T> {
    const text = await (await this.#fetch(method, path, body)).text();
    return (text ? JSON.parse(text) : null) as T;
  }

  /** Say what this browser has open; teammates see it beside your face. */
  setWhere(where: string): void {
    // Header values are Latin-1: anything else would make fetch throw.
    this.#where = where.replace(/[^\x20-\x7e]/g, "").slice(0, 80);
  }

  // -- reads --------------------------------------------------------------

  listProjects(): Promise<{ projects: Project[] }> {
    return this.#request("GET", "/v1/projects");
  }

  listTasks(params: { projectId?: string; status?: string } = {}): Promise<{
    tasks: Task[];
  }> {
    return this.#request("GET", `/v1/tasks${qs(params)}`);
  }

  getTask(id: string): Promise<TaskDetail> {
    return this.#request("GET", `/v1/tasks/${id}`);
  }

  getRun(id: string): Promise<RunDetail> {
    return this.#request("GET", `/v1/runs/${id}`);
  }

  /** Every project, epic, task and agent the sidebar and board draw. */
  async navigation(): Promise<{ projects: NavProject[] }> {
    const { projects } = await this.#request<{ projects: NavProject[] }>("GET", "/v1/navigation");
    return { projects: withWaitingFor(projects) };
  }

  listPullRequests(taskId: string): Promise<{ pullRequests: PullRequest[] }> {
    return this.#request("GET", `/v1/pull-requests${qs({ taskId })}`);
  }

  /** The organisation's latest pull requests, for the chips on cards and tree rows. */
  recentPullRequests(): Promise<{ pullRequests: PullRequest[] }> {
    return this.#request("GET", "/v1/pull-requests");
  }

  /** A scope's events, oldest first, from a cursor: for who did what, and a task's activity. */
  events(params: { runId?: string; taskId?: string; after?: number; limit?: number }): Promise<{ events: PersistedEvent[]; nextCursor: number }> {
    return this.#request("GET", `/v1/events${qs(params)}`);
  }

  listArtifacts(taskId: string): Promise<{ artifacts: Artifact[] }> {
    return this.#request("GET", `/v1/artifacts${qs({ taskId })}`);
  }

  /**
   * An artifact's bytes. Fetched with the key in a header, never in a URL,
   * so the page makes blob URLs from it for images and downloads.
   */
  async artifactContent(id: string): Promise<Blob> {
    return (await this.#fetch("GET", `/v1/artifacts/${encodeURIComponent(id)}/content`)).blob();
  }

  listFindings(taskId: string): Promise<{ findings: Finding[] }> {
    return this.#request("GET", `/v1/findings${qs({ taskId })}`);
  }

  forgeConnection(): Promise<ForgeConnection> {
    return this.#request("GET", "/v1/forge/credential");
  }

  /** Ask GitHub who the stored token is. */
  verifyForge(): Promise<{ ok: true; login: string | null; scopes: string | null } | { ok: false; reason: string }> {
    return this.#request("POST", "/v1/forge/credential/verify", {});
  }

  connectForge(token: string, apiBaseUrl?: string): Promise<unknown> {
    return this.#request("POST", "/v1/forge/credential", { auth: "pat", secret: token, ...(apiBaseUrl ? { apiBaseUrl } : {}) });
  }

  getProject(id: string): Promise<ProjectDetail> {
    return this.#request("GET", `/v1/projects/${id}`);
  }

  listEpics(projectId: string): Promise<{ epics: Epic[] }> {
    return this.#request("GET", `/v1/projects/${projectId}/epics`);
  }

  /** The organization's people, who a task can be handed to; `you` is the signed-in one's person id. */
  listPeople(): Promise<{ people: Member[]; you: string }> {
    return this.#request("GET", "/v1/people");
  }

  /** Who you are, and your organization. */
  me(): Promise<{ person: Member; organization: { id: string; name: string } }> {
    return this.#request("GET", "/v1/me");
  }

  updateMe(changes: { name?: string; photoUrl?: string | null }): Promise<{ person: Member }> {
    return this.#request("PATCH", "/v1/me", changes);
  }

  listMyKeys(): Promise<{ keys: ApiKeyInfo[] }> {
    return this.#request("GET", "/v1/me/keys");
  }

  /** A new key for you; `key` is shown this once. */
  createMyKey(name: string): Promise<{ id: string; name: string; prefix: string; key: string }> {
    return this.#request("POST", "/v1/me/keys", { name });
  }

  revokeMyKey(id: string): Promise<void> {
    return this.#request("DELETE", `/v1/me/keys/${id}`);
  }

  /** Admins: add someone, with a key shown this once. */
  invitePerson(input: { name: string; email: string; role: PersonRole }): Promise<{ person: Member; key: string }> {
    return this.#request("POST", "/v1/people", input);
  }

  updatePerson(id: string, changes: { name?: string; role?: PersonRole }): Promise<{ person: Member }> {
    return this.#request("PATCH", `/v1/people/${id}`, changes);
  }

  /** Admins: remove someone; every key of theirs stops working. */
  removePerson(id: string): Promise<void> {
    return this.#request("DELETE", `/v1/people/${id}`);
  }

  // -- writes -------------------------------------------------------------

  createRun(taskId: string): Promise<Run> {
    return this.#request("POST", `/v1/tasks/${taskId}/runs`, {});
  }

  createTask(input: { projectId: string } & Partial<TaskFields> & { title: string }): Promise<Task> {
    return this.#request("POST", "/v1/tasks", input);
  }

  /** Edit a task. What it asks for is fixed once delivery starts; where it sits is not. */
  updateTask(id: string, changes: Partial<TaskFields>): Promise<Task> {
    return this.#request("PATCH", `/v1/tasks/${id}`, changes);
  }

  /** Hand a task to someone else to drive: they hear of it, and answer for it. */
  reassignTask(id: string, ownerId: string): Promise<Task> {
    return this.#request("PATCH", `/v1/tasks/${id}`, { ownerId });
  }

  /** Everyone on a task, in order; the first owns it. */
  setTaskPeople(id: string, people: string[]): Promise<{ owner: PersonRef | null; people: PersonRef[] }> {
    return this.#request("PUT", `/v1/tasks/${id}/people`, { people });
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

  updateEpic(id: string, changes: Partial<{ title: string; description: string; position: number; state: EpicState | null }>): Promise<Epic> {
    return this.#request("PATCH", `/v1/epics/${id}`, changes);
  }

  // -- settings: the organization's defaults, a project's overrides ----------

  organizationSettings(): Promise<SettingsResponse> {
    return this.#request("GET", "/v1/settings/organization");
  }

  updateOrganizationSettings(patch: SettingsPatch): Promise<SettingsResponse> {
    return this.#request("PATCH", "/v1/settings/organization", patch);
  }

  projectSettings(projectId: string): Promise<SettingsResponse> {
    return this.#request("GET", `/v1/projects/${projectId}/settings`);
  }

  updateProjectSettings(projectId: string, patch: SettingsPatch): Promise<SettingsResponse> {
    return this.#request("PATCH", `/v1/projects/${projectId}/settings`, patch);
  }

  /** Save a role's prompt: the organization's, or (with a project) the project's and how it goes with the organization's. */
  savePrompt(role: PromptRole, prompt: { projectId?: string; mode?: ProjectPromptMode; body?: string; note?: string }): Promise<SettingsResponse> {
    return this.#request("POST", `/v1/prompts/${role}`, prompt);
  }

  promptHistory(role: PromptRole, projectId?: string): Promise<PromptHistory> {
    return this.#request("GET", `/v1/prompts/${role}/history${qs(projectId ? { projectId } : {})}`);
  }

  restorePrompt(versionId: string): Promise<SettingsResponse> {
    return this.#request("POST", `/v1/prompts/versions/${versionId}/restore`);
  }

  /** A project's page: its epics by state, with lanes, pull requests, people and cost. */
  projectOverview(projectId: string): Promise<ProjectOverview> {
    return this.#request("GET", `/v1/projects/${projectId}/overview`);
  }

  deleteEpic(id: string): Promise<void> {
    return this.#request("DELETE", `/v1/epics/${id}`);
  }

  /**
   * Start the delivery workflow: implement, review, fix, simplify, then a
   * pull request. Idempotent — a second call joins the delivery in flight.
   */
  deliver(taskId: string): Promise<{ workflowRunId: string; alreadyRunning: boolean }> {
    return this.#request("POST", `/v1/tasks/${taskId}/deliver`, {});
  }

  /** Approve or decline an agent's request for a repository. */
  decideRepositoryRequest(id: string, approve: boolean, note = ""): Promise<{ status: string }> {
    return this.#request("POST", `/v1/repository-requests/${encodeURIComponent(id)}/decide`, { approve, note });
  }

  /** How long a task's agents worked and waited, how long it sat in review, what it cost. */
  taskMetrics(taskId: string): Promise<TaskMetrics> {
    return this.#request("GET", `/v1/tasks/${encodeURIComponent(taskId)}/metrics`);
  }

  /** An epic's tasks, totalled. */
  epicMetrics(epicId: string): Promise<EpicMetrics> {
    return this.#request("GET", `/v1/epics/${encodeURIComponent(epicId)}/metrics`);
  }

  /** The key this browser subscribes to notifications with. */
  pushKey(): Promise<{ publicKey: string }> {
    return this.#request("GET", "/v1/push/key");
  }

  /** Register this browser for notifications (its PushSubscription, as JSON). */
  subscribePush(subscription: PushSubscriptionJSON): Promise<{ subscribed: boolean }> {
    return this.#request("POST", "/v1/push/subscriptions", subscription);
  }

  unsubscribePush(endpoint: string): Promise<void> {
    return this.#request("POST", "/v1/push/subscriptions/remove", { endpoint });
  }

  /** Finished work with nothing to merge — a write-up, a design — is done once a person has read it. */
  markDone(taskId: string): Promise<{ status: string }> {
    return this.#request("POST", `/v1/tasks/${taskId}/done`, {});
  }

  /** How delivery goes on after it stopped for a person: the task's owner decides. */
  decide(taskId: string, action: EscalationAction, note: string): Promise<{ action: EscalationAction }> {
    return this.#request("POST", `/v1/tasks/${taskId}/decide`, { action, note });
  }

  // -- intervention (plan §24) --------------------------------------------

  /** Answer the question an agent stopped on; the answer starts its next turn. */
  answer(questionId: string, text: string): Promise<{ id: string; status: "answered" }> {
    return this.#request("POST", `/v1/questions/${questionId}/answer`, { text });
  }

  /**
   * Redirect a running agent. Produces a durable, auditable directive. An
   * agent mid-turn hears it when the turn ends, unless `interrupt` stops
   * the turn so it hears it now.
   */
  steer(runId: string, text: string, options: { scope?: DirectiveScope; interrupt?: boolean } = {}): Promise<Directive> {
    return this.#request("POST", `/v1/runs/${runId}/steer`, { text, scope: options.scope ?? "run", ...(options.interrupt ? { interrupt: true } : {}) });
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
    taskId?: string | undefined;
    projectId?: string | undefined;
    /** Skip the backfill and deliver only what happens from now on. */
    live?: boolean | undefined;
  }): string {
    const { live, ...rest } = params;
    return `${this.#baseUrl}/v1/events/stream${qs({ ...rest, ...(live ? { live: 1 } : {}), key: this.#apiKey })}`;
  }
}

/**
 * A cost as the API reports it, or null when it is not known. Zero means
 * the agent reported none (a model behind a proxy with no prices), not that
 * the work was free — so it is shown as "not reported", never as $0.00.
 */
export function reportedCost(usd: number): number | null {
  return usd > 0 ? usd : null;
}

export interface Tokens {
  input: number;
  output: number;
}

/** Times in milliseconds; a task's lead time runs until it ends, or now. */
export interface TaskMetrics {
  leadMs: number;
  activeMs: number;
  humanWaitMs: number;
  reviewMs: number;
  costUsd: number;
  tokens: Tokens;
  runs: Array<{
    id: string;
    phase: string | null;
    role: string | null;
    category: string | null;
    status: string;
    activeMs: number;
    parkedMs: number;
    costUsd: number;
    tokens: Tokens;
  }>;
}

export interface EpicMetrics {
  tasks: number;
  done: number;
  /** The middle lead time of its finished tasks; null until one is. */
  leadMsMedian: number | null;
  activeMs: number;
  humanWaitMs: number;
  reviewMs: number;
  costUsd: number;
  tokens: Tokens;
}

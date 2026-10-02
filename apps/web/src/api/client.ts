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
  RecoverAction,
  RecoveryOptions,
  Repository,
  PullRequest,
  PauseMode,
  Person,
  PersonDetail,
  PersonRef,
  PersonRole,
  Project,
  CostSplit,
  CostOrigin,
  PersistedEvent,
  Run,
  RunDiff,
  Session,
  Task,
  EpicState,
  ProjectPromptMode,
  PromptHistory,
  PromptRole,
  MachineSizeInput,
  MachineSizeWithUse,
  MachinePools,
  ModelTierInput,
  ModelTiersResponse,
  ModelTestResult,
  ProxyModels,
  ImageBuildWithLog,
  ImageChoice,
  ImageDetail,
  ImagesResponse,
  AddServer,
  PreviewSettings,
  Recipe,
  RecipeInput,
  RunServer,
  TaskServers,
  SettingsPatch,
  SettingsResponse,
  IndexStatus,
  Memory,
  MemoryInput,
  SearchOutcome,
  AttachmentInfo,
} from "@dude/domain";
import type { ServerLogLine } from "@dude/design-system";

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
  RecoverAction,
  RecoveryOptions,
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
      webhook: WebhookHealth;
    };

/** One permission on one repository, as Verify found it. */
export interface ForgePermission {
  permission: string;
  level: "required" | "optional";
  outcome: "ok" | "missing" | "untested";
  reason: string;
}

export interface ForgeRepositoryPermissions {
  id: string;
  name: string;
  projectName: string;
  slug: string | null;
  error?: string;
  permissions: ForgePermission[];
}

/**
 * `POST /v1/forge/credential/verify`: who the token is and what it may do on
 * each repository; `ok` is false when a required permission is missing. A
 * token GitHub would not even identify has a `reason` instead.
 */
export type ForgeVerification =
  | {
      ok: boolean;
      login: string | null;
      scopes: string | null;
      tokenKind: "classic" | "fine_grained" | "unknown";
      repositories: ForgeRepositoryPermissions[];
    }
  | { ok: false; reason: string };

/** Whether GitHub's webhooks reach dude, and each repository's hook. */
export interface WebhookHealth {
  lastDeliveryAt: string | null;
  lastFailureAt: string | null;
  lastFailure: string | null;
  failedToday: number;
  rotatedAt: string | null;
  publicUrl: string | null;
  /** Deliveries dude has yet to act on, retrying. */
  retrying: number;
  lastError: string | null;
  repositories: Array<{ id: string; name: string; url: string; projectName: string; hookId: string | null;
    registeredAt: string | null; error: string | null }>;
}

/** Someone (or a team, as "org/slug") a review can be asked of, as GitHub offers them. */
export interface ReviewerCandidate {
  kind: "user" | "team";
  login: string;
  name?: string;
  avatarUrl?: string;
  /** Why GitHub suggests them; absent for one found by words. */
  reason?: "changed" | "commented";
  members?: number;
}

/** How dude behaves on GitHub for the organization. */
export interface GithubSettings {
  whoCanWake: "collaborators" | "members" | "anyone";
  openAs: "ready" | "draft";
  requestReviewFrom: "nobody" | "codeowners" | "logins";
  reviewLogins: string[];
  mergeMethod: "squash" | "merge" | "rebase";
  whenBehind: "update" | "tell";
  /** 0: no limit but each round's. */
  fixRoundsPerPr: number;
  ciStuckMinutes: number;
}

export type MergeMethod = GithubSettings["mergeMethod"];

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
  owners: Array<{ id: string; name: string; photoUrl: string | null; online: boolean }>;
  /** Tokens and machine time, as the epic's metrics count them. */
  costUsd: number;
  machineUsd: number;
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

export type { CostSplit, RunDiff, RunDiffFile, RunDiffSummary } from "@dude/domain";

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

export interface MeResponse {
  person: Member;
  organization: { id: string; name: string };
  authMethod?: "api_key" | "cloudflare_access";
  logoutUrl?: "/cdn-cgi/access/logout";
}

export interface ApiClientOptions {
  /** Base URL; empty means same-origin, which is the browser and Tauri case. */
  baseUrl?: string;
  apiKey?: string | undefined;
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
  /** 1 for the first of its name; a later Run saving the same name adds one. */
  version: number;
  /** How many of its name there are. */
  versions: number;
}

/** Whether images can be attached, and the limits the browser keeps to (packages/domain attachments.ts). */
export interface AttachmentLimits {
  enabled: boolean;
  types: string[];
  perMessage: number;
  originalBytes: number;
  deliveredBytes: number;
  messageBytes: number;
  maxSide: number;
}

export class ApiClient {
  readonly #baseUrl: string;
  readonly #apiKey: string | undefined;
  /** What this browser has open, for presence ("TEXT-14"): see `setWhere`. */
  #where = "";

  constructor(options: ApiClientOptions) {
    this.#baseUrl = (options.baseUrl ?? "").replace(/\/$/, "");
    this.#apiKey = options.apiKey;
  }

  /** A request that throws ApiError for a refusal; the caller reads the body. */
  async #fetch(method: string, path: string, body?: unknown): Promise<Response> {
    // A Blob goes as itself (an image upload); anything else as JSON.
    const raw = body instanceof Blob;
    const res = await fetch(`${this.#baseUrl}${path}`, {
      method,
      credentials: "same-origin",
      headers: {
        ...(this.#apiKey ? { authorization: `Bearer ${this.#apiKey}` } : {}),
        "content-type": raw ? body.type : "application/json",
        ...(this.#where ? { "x-dude-where": this.#where } : {}),
      },
      ...(body === undefined ? {} : { body: raw ? body : JSON.stringify(body) }),
    });
    if (!res.ok) {
      const text = await res.text();
      let error: { code?: string; message?: string; details?: unknown } = {};
      try {
        error = (text ? JSON.parse(text) : null)?.error ?? {};
      } catch {
        // Proxy and Access refusals may be HTML; the HTTP status still applies.
      }
      throw new ApiError(
        res.status,
        error.code ?? "error",
        fieldMessage(error.details) ?? error.message ?? `${method} ${path} failed`,
        error.details,
      );
    }
    return res;
  }

  #upload<T>(method: string, path: string, body: Blob): Promise<T> {
    return this.#request(method, path, body);
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
  /**
   * An artifact's bytes. `inline` asks for a page (HTML, SVG) to be sent
   * for a sandboxed frame rather than as an attachment.
   */
  async artifactContent(id: string, inline = false): Promise<Blob> {
    return (await this.#fetch("GET", `/v1/artifacts/${encodeURIComponent(id)}/content${inline ? "?inline=1" : ""}`)).blob();
  }

  /** The latest version of each of a task's files, as a zip. */
  async artifactsZip(taskId: string): Promise<Blob> {
    return (await this.#fetch("GET", `/v1/tasks/${encodeURIComponent(taskId)}/artifacts.zip`)).blob();
  }

  /** A Run's checkout against where it started, uncommitted work included. */
  runDiff(runId: string): Promise<RunDiff> {
    return this.#request("GET", `/v1/runs/${encodeURIComponent(runId)}/diff`);
  }

  listFindings(taskId: string): Promise<{ findings: Finding[] }> {
    return this.#request("GET", `/v1/findings${qs({ taskId })}`);
  }

  forgeConnection(): Promise<ForgeConnection> {
    return this.#request("GET", "/v1/forge/credential");
  }

  /** Ask GitHub who the stored token is, and what it may do on each repository. */
  verifyForge(): Promise<ForgeVerification> {
    return this.#request("POST", "/v1/forge/credential/verify", {});
  }

  connectForge(token: string, apiBaseUrl?: string): Promise<unknown> {
    return this.#request("POST", "/v1/forge/credential", { auth: "pat", secret: token, ...(apiBaseUrl ? { apiBaseUrl } : {}) });
  }

  githubSettings(): Promise<GithubSettings> {
    return this.#request("GET", "/v1/forge/settings");
  }

  updateGithubSettings(changes: Partial<GithubSettings>): Promise<GithubSettings> {
    return this.#request("PATCH", "/v1/forge/settings", changes);
  }

  /** Register dude's webhook on every repository (or one), delivering to `url`. */
  registerWebhooks(url: string): Promise<{ repositories: Array<{ name: string; slug: string; hookId?: string; error?: string }> }> {
    return this.#request("POST", "/v1/forge/webhooks/register", { url });
  }

  revealWebhookSecret(): Promise<{ secret: string }> {
    return this.#request("GET", "/v1/forge/webhook-secret");
  }

  rotateWebhookSecret(url?: string): Promise<{ secret: string }> {
    return this.#request("POST", "/v1/forge/webhook-secret/rotate", url ? { url } : {});
  }

  /** Merge a pull request on GitHub, when dude would call it ready. */
  mergePullRequest(id: string, method?: MergeMethod): Promise<unknown> {
    return this.#request("POST", `/v1/pull-requests/${id}/merge`, method ? { method } : {});
  }

  /** Merge its base into its branch on GitHub, as "Update branch" does. */
  updatePullRequestBranch(id: string): Promise<unknown> {
    return this.#request("POST", `/v1/pull-requests/${id}/update-branch`, {});
  }

  rerunFailedChecks(id: string): Promise<{ rerun: number }> {
    return this.#request("POST", `/v1/pull-requests/${id}/rerun-failed`, {});
  }

  requestReview(id: string, logins: string[]): Promise<unknown> {
    return this.#request("POST", `/v1/pull-requests/${id}/reviewers`, { logins });
  }

  /**
   * Who could review: GitHub's suggestions for pull request `id` with no
   * words, else who matches them — in its repository, or with no `id`, in
   * the organization's.
   */
  async reviewerCandidates(id: string | null, q: string): Promise<ReviewerCandidate[]> {
    const query = q ? `?q=${encodeURIComponent(q)}` : "";
    const path = id ? `/v1/pull-requests/${id}/reviewer-candidates${query}` : `/v1/forge/reviewer-candidates${query}`;
    return (await this.#request<{ candidates: ReviewerCandidate[] }>("GET", path)).candidates;
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
  me(): Promise<MeResponse> {
    return this.#request("GET", "/v1/me");
  }

  updateMe(changes: { name?: string; photoUrl?: string | null }): Promise<{ person: Member }> {
    return this.#request("PATCH", "/v1/me", changes);
  }

  /** Your photo: the image itself, stored and served back by the backend. */
  uploadMyPhoto(image: Blob): Promise<{ person: Member }> {
    return this.#upload("PUT", "/v1/me/photo", image);
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
    changes: Partial<{ name: string; description: string; runtimeImage: null; runtimeImageId: string | null;
      agentModels: Project["agentModels"]; deliveryPolicy: DeliveryPolicy }>,
  ): Promise<ProjectDetail> {
    return this.#request("PATCH", `/v1/projects/${id}`, changes);
  }

  /** A project's face: an image, or null for its initials again. */
  setProjectImage(id: string, image: Blob | null): Promise<ProjectDetail> {
    return image ? this.#upload("PUT", `/v1/projects/${id}/image`, image) : this.#request("DELETE", `/v1/projects/${id}/image`);
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

  // -- memory: what dude and its agents remember, and the index they search --

  /** `mode: "words"` skips meaning: a lookup by name (the About picker) needs no embedding. */
  searchMemory(params: { q: string; project?: string; types?: string; limit?: number; mode?: "words" }): Promise<SearchOutcome> {
    return this.#request("GET", `/v1/memory/search${qs(params)}`);
  }

  listMemories(params: { project?: string; scope?: string; author?: string; q?: string; archived?: boolean } = {}): Promise<{ memories: Memory[] }> {
    return this.#request("GET", `/v1/memory/memories${qs(params)}`);
  }

  getMemory(id: string): Promise<Memory> {
    return this.#request("GET", `/v1/memory/memories/${encodeURIComponent(id)}`);
  }

  createMemory(input: MemoryInput): Promise<Memory> {
    return this.#request("POST", "/v1/memory/memories", input);
  }

  updateMemory(id: string, input: MemoryInput): Promise<Memory> {
    return this.#request("PATCH", `/v1/memory/memories/${encodeURIComponent(id)}`, input);
  }

  archiveMemory(id: string, archived: boolean): Promise<Memory> {
    return this.#request("POST", `/v1/memory/memories/${encodeURIComponent(id)}/${archived ? "archive" : "restore"}`);
  }

  memoryIndex(project?: string): Promise<IndexStatus> {
    return this.#request("GET", `/v1/memory/index${qs({ project })}`);
  }

  retryIndex(target: { type?: string; id?: string } = {}): Promise<{ due: number }> {
    return this.#request("POST", "/v1/memory/index/retry", target);
  }

  reindexMemory(): Promise<{ due: number }> {
    return this.#request("POST", "/v1/memory/index/reindex");
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

  // -- machines: the organization's sizes, lux's pools -----------------------

  machineSizes(): Promise<{ sizes: MachineSizeWithUse[]; canEdit: boolean }> {
    return this.#request("GET", "/v1/machines/sizes");
  }

  addMachineSize(size: MachineSizeInput): Promise<{ sizes: MachineSizeWithUse[]; canEdit: boolean }> {
    return this.#request("POST", "/v1/machines/sizes", size);
  }

  updateMachineSize(id: string, size: MachineSizeInput): Promise<{ sizes: MachineSizeWithUse[]; canEdit: boolean }> {
    return this.#request("PUT", `/v1/machines/sizes/${encodeURIComponent(id)}`, size);
  }

  makeDefaultMachineSize(id: string): Promise<{ sizes: MachineSizeWithUse[]; canEdit: boolean }> {
    return this.#request("POST", `/v1/machines/sizes/${encodeURIComponent(id)}/default`);
  }

  /** Remove a size, moving what named it to `replacement` (null: the default). */
  removeMachineSize(id: string, replacement: string | null): Promise<{ sizes: MachineSizeWithUse[]; canEdit: boolean }> {
    return this.#request("DELETE", `/v1/machines/sizes/${encodeURIComponent(id)}`, { replacement });
  }

  machinePools(): Promise<MachinePools> {
    return this.#request("GET", "/v1/machines/pools");
  }

  // -- models: the organization's tiers, the proxy's models -----------------

  modelTiers(): Promise<ModelTiersResponse> {
    return this.#request("GET", "/v1/models/tiers");
  }

  addModelTier(tier: ModelTierInput): Promise<ModelTiersResponse> {
    return this.#request("POST", "/v1/models/tiers", tier);
  }

  updateModelTier(id: string, tier: ModelTierInput): Promise<ModelTiersResponse> {
    return this.#request("PUT", `/v1/models/tiers/${encodeURIComponent(id)}`, tier);
  }

  /** Removes a tier; what names it moves to `replacement` (required while it is in use). */
  removeModelTier(id: string, replacement: string | null): Promise<ModelTiersResponse> {
    return this.#request("DELETE", `/v1/models/tiers/${encodeURIComponent(id)}`, { replacement });
  }

  dismissTierUpgrade(): Promise<ModelTiersResponse> {
    return this.#request("POST", "/v1/models/upgrade/dismiss");
  }

  proxyModels(): Promise<ProxyModels> {
    return this.#request("GET", "/v1/models/proxy");
  }

  /** One small request for `model` at each effort `tierId`'s agents use (none: a new tier). */
  testModel(model: string, tierId: string | null): Promise<{ model: string; results: ModelTestResult[] }> {
    return this.#request("POST", "/v1/models/test", { model, tierId });
  }

  // -- images: the organization's library ------------------------------------

  images(): Promise<ImagesResponse> {
    return this.#request("GET", "/v1/images");
  }

  /** Every image a picker offers, the default base first. */
  imageChoices(): Promise<{ images: ImageChoice[]; defaultImageId: string | null }> {
    return this.#request("GET", "/v1/images/picker");
  }

  image(id: string): Promise<ImageDetail> {
    return this.#request("GET", `/v1/images/${encodeURIComponent(id)}`);
  }

  createImage(input: { name: string; description?: string; containerfile?: string; note?: string }): Promise<ImageDetail> {
    return this.#request("POST", "/v1/images", input);
  }

  updateImage(id: string, patch: { description?: string; archived?: boolean }): Promise<ImageDetail> {
    return this.#request("PATCH", `/v1/images/${encodeURIComponent(id)}`, patch);
  }

  /** Save the image's draft; 422 invalid_containerfile names each line that won't build. */
  saveImageDraft(id: string, draft: { containerfile: string; buildArgs?: Record<string, string>; note?: string }): Promise<ImageDetail> {
    return this.#request("PUT", `/v1/images/${encodeURIComponent(id)}/draft`, draft);
  }

  discardImageDraft(id: string): Promise<ImageDetail> {
    return this.#request("DELETE", `/v1/images/${encodeURIComponent(id)}/draft`);
  }

  /** Build & publish: the draft (saved first, when given) numbered and queued. */
  buildImage(
    id: string,
    draft?: { containerfile: string; buildArgs?: Record<string, string>; note?: string },
  ): Promise<{ buildId: string; versionId: string; version: number; image: ImageDetail }> {
    return this.#request("POST", `/v1/images/${encodeURIComponent(id)}/build`, draft);
  }

  /** Publish a built version again: at once, no build. */
  republishImage(id: string, versionId: string): Promise<ImageDetail> {
    return this.#request("POST", `/v1/images/${encodeURIComponent(id)}/versions/${encodeURIComponent(versionId)}/publish`);
  }

  setDefaultImage(id: string | null): Promise<ImagesResponse> {
    return id ? this.#request("POST", `/v1/images/default/${encodeURIComponent(id)}`) : this.#request("DELETE", "/v1/images/default");
  }

  /** A build with its log; `after`, the logTotal already read: only the log since, when the build still holds it. */
  imageBuild(buildId: string, after?: number): Promise<ImageBuildWithLog> {
    return this.#request("GET", `/v1/images/builds/${encodeURIComponent(buildId)}${after === undefined ? "" : `?after=${after}`}`);
  }

  cancelImageBuild(buildId: string): Promise<{ cancelled: string }> {
    return this.#request("POST", `/v1/images/builds/${encodeURIComponent(buildId)}/cancel`);
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
  deliver(taskId: string, attachmentIds: ReadonlyArray<string> = []): Promise<{ workflowRunId: string; alreadyRunning: boolean }> {
    return this.#request("POST", `/v1/tasks/${taskId}/deliver`, attachmentIds.length > 0 ? { attachmentIds } : {});
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

  /** How a stopped task can be picked back up now, and until when a resume can. */
  recoveryOptions(taskId: string): Promise<RecoveryOptions> {
    return this.#request("GET", `/v1/tasks/${taskId}/recover`);
  }

  /** Pick a stopped task back up — the task's owner does — with a note for the agents. */
  recover(taskId: string, action: RecoverAction, note: string): Promise<{ action: RecoverAction }> {
    return this.#request("POST", `/v1/tasks/${taskId}/recover`, { action, note });
  }

  /** How delivery goes on after it stopped for a person: the task's owner decides. */
  decide(taskId: string, action: EscalationAction, note: string): Promise<{ action: EscalationAction }> {
    return this.#request("POST", `/v1/tasks/${taskId}/decide`, { action, note });
  }

  // -- intervention (plan §24) --------------------------------------------

  /** Answer the question an agent stopped on; the answer starts its next turn. */
  answer(questionId: string, text: string, attachmentIds: ReadonlyArray<string> = []): Promise<{ id: string; status: "answered" }> {
    return this.#request("POST", `/v1/questions/${questionId}/answer`, { text, ...(attachmentIds.length > 0 ? { attachmentIds } : {}) });
  }

  // -- images a person sends an agent -------------------------------------

  /** Whether images can be attached here, and the limits the browser keeps to. */
  attachmentLimits(): Promise<AttachmentLimits> {
    return this.#request("GET", "/v1/attachment-limits");
  }

  /**
   * Upload one image to a task: the original as picked and the variant the
   * agent is sent. `onProgress` follows the upload (0..1). XHR, because
   * fetch reports no upload progress.
   */
  uploadAttachment(taskId: string, image: { name: string; original: Blob; delivered: Blob },
    onProgress?: (fraction: number) => void): Promise<AttachmentInfo> {
    const form = new FormData();
    form.set("name", image.name);
    form.set("original", image.original, image.name);
    form.set("originalType", image.original.type);
    form.set("delivered", image.delivered, image.name);
    form.set("deliveredType", image.delivered.type);
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      xhr.open("POST", `${this.#baseUrl}/v1/tasks/${encodeURIComponent(taskId)}/attachments`);
      xhr.withCredentials = true;
      if (this.#apiKey) xhr.setRequestHeader("authorization", `Bearer ${this.#apiKey}`);
      xhr.upload.onprogress = (e) => {
        if (e.lengthComputable) onProgress?.(e.loaded / e.total);
      };
      xhr.onerror = () => reject(new ApiError(0, "network", "the upload did not reach dude"));
      xhr.onload = () => {
        let body: { error?: { code?: string; message?: string } } & Partial<AttachmentInfo> = {};
        try {
          body = JSON.parse(xhr.responseText || "{}");
        } catch {
          // A proxy's HTML refusal: the status still applies.
        }
        if (xhr.status >= 200 && xhr.status < 300) resolve(body as AttachmentInfo);
        else reject(new ApiError(xhr.status, body.error?.code ?? "error", body.error?.message ?? `upload failed (${xhr.status})`));
      };
      xhr.send(form);
    });
  }

  /** An image's bytes, as the agent got it or as it was picked. */
  async attachment(id: string, variant: "delivered" | "original" = "delivered"): Promise<Blob> {
    return (await this.#fetch("GET", `/v1/attachments/${encodeURIComponent(id)}?variant=${variant}`)).blob();
  }

  /** Remove an image not sent yet (its chip's ✕). */
  removeAttachment(id: string): Promise<void> {
    return this.#request("DELETE", `/v1/attachments/${encodeURIComponent(id)}`);
  }

  // -- servers: a project's recipes, and what a run serves ---------------

  /** The project's server recipes, and how its branch previews run. */
  projectServers(projectId: string): Promise<{ servers: Recipe[]; previews: PreviewSettings }> {
    return this.#request("GET", `/v1/projects/${encodeURIComponent(projectId)}/servers`);
  }

  /** Create or replace a recipe; `name` in the path is the one being replaced, the body's may differ (a rename). */
  putProjectServer(projectId: string, name: string, recipe: RecipeInput): Promise<Recipe> {
    return this.#request("PUT", `/v1/projects/${encodeURIComponent(projectId)}/servers/${encodeURIComponent(name)}`, recipe);
  }

  removeProjectServer(projectId: string, name: string): Promise<void> {
    return this.#request("DELETE", `/v1/projects/${encodeURIComponent(projectId)}/servers/${encodeURIComponent(name)}`);
  }

  updatePreviewSettings(projectId: string, settings: PreviewSettings): Promise<PreviewSettings> {
    return this.#request("PUT", `/v1/projects/${encodeURIComponent(projectId)}/preview-settings`, settings);
  }

  /** The run serving a task — its agent's, else its branch preview — and that run's servers. */
  taskServers(taskId: string): Promise<TaskServers> {
    return this.#request("GET", `/v1/tasks/${encodeURIComponent(taskId)}/servers`);
  }

  runServers(runId: string): Promise<TaskServers> {
    return this.#request("GET", `/v1/runs/${encodeURIComponent(runId)}/servers`);
  }

  /** Add a server to a run: one of the project's recipes by name, or a port and command just for this run. */
  addRunServer(runId: string, input: AddServer): Promise<RunServer> {
    return this.#request("POST", `/v1/runs/${encodeURIComponent(runId)}/servers`, input);
  }

  serverAction(runId: string, name: string, action: "start" | "stop" | "restart"): Promise<RunServer> {
    return this.#request("POST", `/v1/runs/${encodeURIComponent(runId)}/servers/${encodeURIComponent(name)}/${action}`, {});
  }

  removeRunServer(runId: string, name: string): Promise<void> {
    return this.#request("DELETE", `/v1/runs/${encodeURIComponent(runId)}/servers/${encodeURIComponent(name)}`);
  }

  serversAll(runId: string, action: "start-all" | "stop-all"): Promise<TaskServers> {
    return this.#request("POST", `/v1/runs/${encodeURIComponent(runId)}/servers/${action}`, {});
  }

  /** A server's output, current and earlier placements, newest last; `tail` up to 10 000 lines. */
  serverLog(runId: string, name: string, tail = 200): Promise<{ lines: ServerLogLine[] }> {
    return this.#request("GET", `/v1/runs/${encodeURIComponent(runId)}/servers/${encodeURIComponent(name)}/log${qs({ tail })}`);
  }

  /** Bring the project's servers up on a run of their own, at the task's branch: no agent, just the checkout. 409 `preview_running` while one is live. */
  startPreview(taskId: string): Promise<TaskServers> {
    return this.#request("POST", `/v1/tasks/${encodeURIComponent(taskId)}/preview`, {});
  }

  /** Stop the task's branch preview; 404 when none is live. */
  stopPreview(taskId: string): Promise<TaskServers> {
    return this.#request("DELETE", `/v1/tasks/${encodeURIComponent(taskId)}/preview`);
  }

  /**
   * Redirect a running agent. Produces a durable, auditable directive. The
   * agent reads it at its next step (or its next turn, for a harness that
   * reads only between turns), unless `interrupt` stops the turn so it
   * hears it now. `supersedes` sends a queued or failed directive again.
   */
  steer(runId: string, text: string,
    options: { scope?: DirectiveScope; interrupt?: boolean; supersedes?: string; attachmentIds?: ReadonlyArray<string> } = {}): Promise<Directive> {
    return this.#request("POST", `/v1/runs/${runId}/steer`, {
      text, scope: options.scope ?? "run",
      ...(options.interrupt ? { interrupt: true } : {}),
      ...(options.supersedes ? { supersedes: options.supersedes } : {}),
      ...(options.attachmentIds && options.attachmentIds.length > 0 ? { attachmentIds: options.attachmentIds } : {}),
    });
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
    return `${this.#baseUrl}/v1/events/stream${qs({ ...rest, ...(live ? { live: 1 } : {}), ...(this.#apiKey ? { key: this.#apiKey } : {}) })}`;
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

/**
 * A model cost as shown, given who priced it: lux's figure is a price even
 * at zero; the agent's zero is "not reported" (reportedCost).
 */
export function modelCostShown(costUsd: number, from: CostOrigin): number | null {
  return from === "lux" ? costUsd : reportedCost(costUsd);
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
  /** The model's tokens; `cost` has the whole. */
  costUsd: number;
  cost: CostSplit;
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
    cost: CostSplit;
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
  cost: CostSplit;
  tokens: Tokens;
}

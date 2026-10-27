/**
 * Typed client for the control plane's product API.
 *
 * The frontend depends on the product API, never on its host (plan §117).
 * Everything here is plain `fetch` against documented endpoints, so the same
 * code runs in a browser and inside the Tauri shell the plan anticipates —
 * no Electron/Node assumptions, no direct database access.
 */

import type { NavProject, NavTask } from "@dude/design-system";
import { driverMode } from "@dude/design-system/components";
import type { CloseDecision } from "@dude/design-system/components";
import { escalationWords } from "../escalation.ts";
import { isActive } from "../activity.ts";
// One question's answer as the form sends it: the choices picked, by index, and the person's own words.
export type { QuestionAnswer as SentAnswer } from "@dude/design-system/components";
import type { QuestionAnswer as SentAnswer } from "@dude/design-system/components";
import type {
  AgentMachine,
  AgentRole,
  Automation,
  AutomationInput,
  AutomationRunDetail,
  AutomationPromptVersion,
  AutomationRunsResponse,
  EventSubscriptions,
  EventVerdict,
  NewAutomation,
  PastDelivery,
  RunNow,
  FeedbackDraftInput,
  FeedbackItem,
  FeedbackList,
  FileResult,
  SessionDetail,
  SessionModel,
  Harness,
  SessionLink,
  SessionsList,
  OpenSessionsList,
  TaskRepository,
  TaskSource,
  TaskSourceInput,
  Decider,
  DeliveryPolicy,
  Directive,
  DirectiveScope,
  Epic,
  Escalation,
  EscalationAction,
  Finding,
  ReviewQuestion,
  Verification,
  RecoverAction,
  RecoveryOptions,
  StopRequest,
  StopResult,
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
  ModelTestInput,
  ProxyModels,
  JudgementCallDetail,
  JudgementCallsResponse,
  JudgementSettings,
  JudgementTestResult,
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
  RefusedName,
  SettingsResponse,
  IndexStatus,
  Memory,
  MemoryInput,
  SearchOutcome,
  AttachmentInfo,
  ProjectSecret,
  SecretRecipient,
  NoteRecord,
  EpicNotes,
  EpicPeers,
  EpicThreads,
  PinNoteInput,
  PinnedNote,
  AutoStartHow,
  RelationSide,
  TaskRelations,
  TaskStatus,
  PullRequestChoice,
} from "@dude/domain";
import type {
  ChatConnectStateView,
  ChatInstallationView,
  ChatOverview,
  ChatProjectsView,
  ChatRequestRow,
  ChatRequestsSummary,
  ProviderRoutingInstructions,
  RoutingInstructions,
} from "@dude/domain";
import type { ServerLogLine } from "@dude/design-system";
import type { OrgSecret, OrgSecretsResponse } from "@dude/domain";

/** A page of dude's guide; its sections' text only when one page is read (the intro's anchor and heading are ""). */
export interface HelpPage {
  slug: string;
  title: string;
  sections: { anchor: string; heading: string; text?: string }[];
}

// ---------------------------------------------------------------------------
// Wire types
//
// The API returns domain records, sometimes with their children embedded.
// These aliases add only that nesting: anything else about a Project or a Run
// is the domain package's to say, so the two cannot drift.
// ---------------------------------------------------------------------------

export type {
  CriterionVerdict,
  Epic,
  Escalation,
  EscalationAction,
  Finding,
  ReviewQuestion,
  Verification,
  VerdictStatus,
  RecoverAction,
  RecoveryOptions,
  StopRequest,
  StopResult,
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
  EpicNote,
  EpicNotes,
  EpicPeers,
  EpicThreads,
  NoteRecord,
  PeerMessageView,
  PeerState,
  PeerThread,
  PeerWaitView,
  PinNoteInput,
  PinnedNote,
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
  /** The organisation the key acts in; absent from a key's own list, which is its organisation's. */
  organizationId?: string;
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
      /** A GitHub App's id, and its default installation (none: found per repository owner). */
      appId?: string | null;
      installationId?: string | null;
      oauthClientId?: string | null;
      oauthClientSecretConfigured?: boolean;
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
  /** A GitHub App: a token was minted for `repository`, and its bot is `bot`. */
  | { ok: true; app: true; bot: string | null; repository: string | null }
  | { ok: false; reason: string };

/** A GitHub App's credential: its private key (PEM), App ID and, optionally, a default installation. */
export interface GithubAppInput {
  privateKey: string;
  appId: string;
  installationId?: string;
  oauthClientId?: string;
  oauthClientSecret?: string;
  clearOauthClientSecret?: boolean;
}

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
  checksFixRounds: number;
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
  /** Existing work it starts from (a pull request or a branch), or null for new work; fixed once delivery starts. */
  source?: TaskSourceInput | null;
}

export type { TaskRepository, TaskSource, TaskSourceInput, TaskSourceMode } from "@dude/domain";

/** A project repository as the source picker offers it: writable says dude may push to it; reason says why not. */
export interface SourceRepository {
  id: string;
  name: string;
  writable: boolean;
  reason?: string;
}

/** A task with its attempts, newest first, and why it waits if delivery stopped for a person. `GET /v1/tasks/:id`. */
export interface TaskDetail extends Task {
  runs: Run[];
  /** Each delivery's attempt, who decided it, and the task branch it named (null before it named one). */
  deciders?: Array<{ attempt: number; decider: Decider; branch?: string | null }>;
  escalation: Escalation | null;
  /** The machines its agents raised, by role, names resolved by the control plane; empty when none. */
  agentMachines: AgentMachine[];
}

/** What a message in a task's Chat reached: its conductor, made by it or not. */
export interface ChatSent {
  runId: string;
  taskId: string;
  created: boolean;
  directiveId?: string;
  questionId?: string;
  /** Who decides the task's delivery after the message: it hands a delivery in progress to the conductor. */
  decider?: Decider;
}

/** What a task held before its pull request waits for, on the board and in the tree. */
export const READY_FOR_REVIEW_WAITING = "Ready for your review: no pull request yet";

/**
 * The navigation tree gives each task's escalation, when delivery stopped
 * for a person, and who decides it with whether its conductor is live; the
 * design system's NavTask shows them as what the task waits for, in words,
 * and as its driver.
 */
export function withWaitingFor(projects: NavProject[]): NavProject[] {
  const task = (t: NavTask & { escalation?: Escalation | null; decider?: Decider; conductorLive?: boolean }): NavTask => {
    const { decider, conductorLive, ...rest } = t;
    const driven = decider ? { ...rest, driver: driverMode(decider, conductorLive === true) } : rest;
    if (t.escalation) return { ...driven, heldForReview: false, waitingFor: escalationWords(t.escalation).short };
    return t.heldForReview ? { ...driven, waitingFor: READY_FOR_REVIEW_WAITING } : driven;
  };
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
  /** Its tasks' agents waiting on a peer's reply now, as [waiting task's key, the key it waits on]. */
  waiting?: Array<[string, string]>;
  createdAt: string;
  updatedAt: string;
}

export interface ProjectOverview {
  project: { id: string; name: string };
  epics: EpicOverview[];
  /** Its archived epics, out of the list: here to unarchive. */
  archivedEpics?: Array<{ id: string; title: string; archivedAt: string }>;
}

/** A Run with the sessions it spawned. `GET /v1/runs/:id`. */
export interface RunDetail extends Run {
  sessions: Session[];
}

export type { CostSplit, RunDiff, RunDiffFile, RunDiffSummary } from "@dude/domain";
export type { FeedbackDraftInput, FeedbackItem, FeedbackList } from "@dude/domain";

/** Where a submitted batch went (`POST /v1/tasks/:id/feedback/submit`): the orchestrator's answer. */
export interface FeedbackSubmitted {
  batchId: string;
  itemIds: string[];
  routedTo: "conductor" | "steer" | "workflow";
  runId?: string;
  /** Why nobody works on it yet, when nobody does. */
  waiting?: string;
  /** This key was taken before: this is that batch. */
  repeated?: boolean;
}

/** A reply, resolve or reopen taken; a reopen says where the item went again. */
export interface FeedbackActed {
  itemId: string;
  action: "reply" | "resolve" | "reopen";
  routedTo?: string;
  runId?: string;
  waiting?: string;
}
export type { AutoStartHow, RelationSide, TaskRelations, TaskRelationView } from "@dude/domain";

/** A start of a blocked task anyway: a person's, with the note recorded on it. */
export interface StartAnyway {
  override?: true;
  note?: string;
}

/** The open blockers a start refused as `blocked` names (its details). */
export function refusedBlockers(e: unknown): Array<{ key: string; status: TaskStatus }> | null {
  if (!(e instanceof ApiError) || e.code !== "blocked") return null;
  const d = e.details as { blockers?: Array<{ key: string; status: TaskStatus }> } | undefined;
  return d?.blockers ?? [];
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

export interface MeResponse {
  person: Me;
  organization: { id: string; name: string; slug?: string };
  authMethod?: "api_key" | "cloudflare_access";
  logoutUrl?: "/cdn-cgi/access/logout";
  /** The sign-in gate's answer (docs/design/accounts.md); absent from an older backend, which is a key's member. */
  gate?: Gate;
  superAdmin?: boolean;
  account?: AccountInfo | null;
  organizations?: MembershipInfo[];
  invitations?: InvitationInfo[];
}

export type Gate = "member" | "invited" | "super_admin" | "none";

/** `GET /v1/me` before any organisation is named: one of the gate's four answers. */
export type MeAnswer =
  | (MeResponse & { gate?: "member" })
  | { gate: "member"; account: AccountInfo | null; organizations: MembershipInfo[]; invitations: InvitationInfo[];
      organization: null; superAdmin: boolean; authMethod: "cloudflare_access"; person?: undefined }
  | { gate: "invited" | "super_admin"; email: string; name: string | null; photoUrl: string | null; superAdmin: boolean;
      invitations?: InvitationInfo[]; person?: undefined }
  | { gate: "none"; email: string; name: string | null; photoUrl: string | null; superAdmin?: undefined; person?: undefined };

export interface AccountInfo {
  id: string;
  email: string;
  name: string;
  photoUrl: string | null;
}

/** One of your organisations, as `/v1/me` lists them. */
export interface MembershipInfo {
  id: string;
  name: string;
  slug: string;
  role: PersonRole;
  personId: string;
  waitingOnYou: number;
  /** When its first-time page was left; null until then. Absent from an older backend and in API-key mode. */
  welcomedAt?: string | null;
}

/** An invitation as its invitee (or an admin) sees it. */
export interface InvitationInfo {
  id: string;
  code: string;
  email: string;
  name: string | null;
  organization: { id: string; name: string; slug: string };
  role: PersonRole;
  /** Who sent it; "the operator" for a super-admin's. */
  inviter: string;
  createdAt: string;
  expiresAt: string;
}

export type InvitationStatus = "pending" | "expired" | "accepted" | "revoked";

/** `GET /v1/organization/invitations/counts`: every invitation of the organisation, by status. */
export type InvitationCounts = Record<InvitationStatus | "all", number>;

/** One row of an organisation's Invitations page. */
export interface InvitationRow extends InvitationInfo {
  status: InvitationStatus;
  acceptedAt: string | null;
  acceptedPersonId: string | null;
  revokedAt: string | null;
  renewedAt: string | null;
}

/** `GET /v1/invitations/:code` for the signed-in email. */
export type InvitationView = InvitationInfo & { state: "pending" | "member"; personId: string | null };

/** What inviting did with one address. */
export interface InviteResult {
  email: string;
  result: "created" | "renewed" | "existing" | "member" | "invalid" | "duplicate";
  invitation?: { id: string; code: string; role: PersonRole; expiresAt: string };
  /** `#/invite/<code>`, for the first three. */
  link?: string;
}

/** An organisation as the super-admin lists them. */
export interface AdminOrganization {
  id: string;
  name: string;
  slug: string;
  createdAt: string;
  members: number;
  admins: number;
  pendingInvitations: number;
}

/** A person of an organisation as the super-admin sees them: email null for one who signs in only with a key. */
export type AdminPerson = PersonRef & { email: string | null; role: PersonRole; accountId: string | null };

/** Where an id lives among your organisations. `GET /v1/locate/:id`. */
export interface Located {
  organization: { id: string; name: string; slug: string };
  kind: "task" | "project" | "epic" | "run" | "brainstorm" | "connect";
  id: string;
}

/** Whose preview is served at an origin. `GET /v1/preview-servers/lookup`. */
export interface PreviewServerAt {
  organizationSlug: string;
  taskId: string;
  taskKey: string;
  server: string;
}

/** You, as your own profile shows you: who you are on GitHub too. */
export type Me = Member & { githubLogin?: string | null; commitEmail?: string | null };

export interface ApiClientOptions {
  /** Base URL; empty means same-origin, which is the browser and Tauri case. */
  baseUrl?: string;
  apiKey?: string | undefined;
  /**
   * The organisation tenant requests act in: `X-Dude-Organization` on each
   * call, `org=` on the event stream. Account routes (`/v1/me` without
   * one, your keys, invitations, the super-admin's) need none.
   */
  organizationId?: string | undefined;
}

/** A file an agent published, as its task or brainstorm session lists it. `GET /v1/artifacts`. */
export interface Artifact {
  id: string;
  /** null for a brainstorm session's. */
  taskId: string | null;
  sessionId?: string | null;
  runId: string | null;
  name: string;
  contentType: string;
  sizeBytes: number;
  sha256: string;
  /** What the agent said the file is for, one short line; "" when it said nothing. */
  description: string;
  epoch: number;
  createdAt: string;
  phase: string | null;
  role: AgentRole | null;
  /** 1 for the first of its name; a later Run saving the same name adds one. */
  version: number;
  /** How many of its name there are. */
  versions: number;
  /** The small JPEG dude publish made of it (an image's or a video's), when there is one. */
  thumbnailId?: string | null;
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

/** What an image is uploaded to: a task, or a brainstorm session. */
export type UploadRoute = "tasks" | "brainstorms";

export class ApiClient {
  readonly #baseUrl: string;
  readonly #apiKey: string | undefined;
  readonly #organizationId: string | undefined;
  /** What this browser has open, for presence ("TEXT-14"): see `setWhere`. */
  #where = "";

  constructor(options: ApiClientOptions) {
    this.#baseUrl = (options.baseUrl ?? "").replace(/\/$/, "");
    this.#apiKey = options.apiKey;
    this.#organizationId = options.organizationId;
  }

  /** The organisation this client's tenant requests name, if any. */
  get organizationId(): string | undefined {
    return this.#organizationId;
  }

  /** The client this one was made from by `inOrganization`, or itself: the session's credential. */
  get root(): ApiClient {
    return this.#root ?? this;
  }
  #root: ApiClient | undefined;

  /** This credential acting in `organizationId`: a new client, so a tab in another organisation keeps its own. */
  inOrganization(organizationId: string): ApiClient {
    const scoped = new ApiClient({ baseUrl: this.#baseUrl, apiKey: this.#apiKey, organizationId });
    scoped.#root = this.root;
    return scoped;
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
        ...(this.#organizationId ? { "x-dude-organization": this.#organizationId } : {}),
        "content-type": raw ? body.type : "application/json",
        ...(this.#where ? { "x-dude-where": this.#where } : {}),
        // Only a request from a tab someone is using says they are here.
        ...(isActive(Date.now()) ? { "x-dude-active": "1" } : {}),
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

  /** What a brainstorm session's agent published: every Run of the session's, for its members. */
  listSessionArtifacts(sessionId: string): Promise<{ artifacts: Artifact[] }> {
    return this.#request("GET", `/v1/artifacts${qs({ sessionId })}`);
  }

  /** The latest version of each of a session's files, as a zip. */
  async sessionArtifactsZip(sessionId: string): Promise<Blob> {
    return (await this.#fetch("GET", `/v1/brainstorms/${encodeURIComponent(sessionId)}/artifacts.zip`)).blob();
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

  /** Questions the task's reviewers asked a person, every status, oldest first. */
  reviewQuestions(taskId: string): Promise<{ questions: ReviewQuestion[] }> {
    return this.#request("GET", `/v1/tasks/${encodeURIComponent(taskId)}/review-questions`);
  }

  /** The task's owner answers a reviewer's open question. */
  answerReviewQuestion(id: string, answer: string): Promise<{ id: string; status: "answered" }> {
    return this.#request("POST", `/v1/review-questions/${encodeURIComponent(id)}/answer`, { answer });
  }

  /** The task's owner withdraws a reviewer's open question as moot, saying why. */
  withdrawReviewQuestion(id: string, reason: string): Promise<{ id: string; status: "withdrawn" }> {
    return this.#request("POST", `/v1/review-questions/${encodeURIComponent(id)}/withdraw`, { reason });
  }

  /** The attempt's submitted feedback and your own drafts on it; the current attempt unless named. */
  feedback(taskId: string, attempt?: number): Promise<FeedbackList> {
    return this.#request("GET", `/v1/tasks/${encodeURIComponent(taskId)}/feedback${qs({ attempt })}`);
  }

  createFeedbackDraft(taskId: string, draft: FeedbackDraftInput): Promise<FeedbackItem> {
    return this.#request("POST", `/v1/tasks/${encodeURIComponent(taskId)}/feedback/drafts`, draft);
  }

  /** A draft written again: its words and where it points, the whole of it. */
  updateFeedbackDraft(id: string, draft: FeedbackDraftInput): Promise<FeedbackItem> {
    return this.#request("PATCH", `/v1/feedback/${encodeURIComponent(id)}`, draft);
  }

  deleteFeedbackDraft(id: string): Promise<void> {
    return this.#request("DELETE", `/v1/feedback/${encodeURIComponent(id)}`);
  }

  /** Drafts as one batch; the same key again is the same batch, so a retry never submits twice. */
  submitFeedback(taskId: string, draftIds: ReadonlyArray<string>, submissionKey: string): Promise<FeedbackSubmitted> {
    return this.#request("POST", `/v1/tasks/${encodeURIComponent(taskId)}/feedback/submit`, { draftIds, submissionKey });
  }

  replyFeedback(id: string, body: string): Promise<FeedbackActed> {
    return this.#request("POST", `/v1/feedback/${encodeURIComponent(id)}/reply`, { body });
  }

  resolveFeedback(id: string, note?: string): Promise<FeedbackActed> {
    return this.#request("POST", `/v1/feedback/${encodeURIComponent(id)}/resolve`, note ? { note } : {});
  }

  reopenFeedback(id: string, body?: string): Promise<FeedbackActed> {
    return this.#request("POST", `/v1/feedback/${encodeURIComponent(id)}/reopen`, body ? { body } : {});
  }

  /** Each test's verdict per acceptance criterion, the latest first. */
  listVerification(taskId: string): Promise<{ verifications: Verification[] }> {
    return this.#request("GET", `/v1/tasks/${encodeURIComponent(taskId)}/verification`);
  }

  forgeConnection(): Promise<ForgeConnection> {
    return this.#request("GET", "/v1/forge/credential");
  }

  /** Ask GitHub who the stored token is, and what it may do on each repository. */
  verifyForge(): Promise<ForgeVerification> {
    return this.#request("POST", "/v1/forge/credential/verify", {});
  }

  /** Store the organization's token; with `publicUrl`, also register dude's webhook on every repository, delivering there, in the background. */
  connectForge(token: string, apiBaseUrl?: string, publicUrl?: string): Promise<unknown> {
    return this.#request("POST", "/v1/forge/credential", { auth: "pat", secret: token, ...(apiBaseUrl ? { apiBaseUrl } : {}),
      ...(publicUrl ? { publicUrl } : {}) });
  }

  /** Store the organization's GitHub App: its webhook is the App's own, so nothing is registered. */
  connectGithubApp(app: GithubAppInput, apiBaseUrl?: string): Promise<unknown> {
    return this.#request("POST", "/v1/forge/credential", { auth: "github_app", secret: app.privateKey, appId: app.appId,
      installationId: app.installationId || null, oauthClientId: app.oauthClientId, oauthClientSecret: app.oauthClientSecret,
      clearOauthClientSecret: app.clearOauthClientSecret, ...(apiBaseUrl ? { apiBaseUrl } : {}) });
  }

  githubAuthorization(): Promise<{ available: boolean; connection: { login: string; expiresAt: string; refreshExpiresAt: string; reconnectRequired: boolean } | null }> {
    return this.#request("GET", "/v1/me/github");
  }

  connectGithub(): Promise<{ url: string }> {
    return this.#request("POST", "/v1/me/github/connect", {});
  }

  disconnectGithub(): Promise<void> {
    return this.#request("DELETE", "/v1/me/github");
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

  /** Ask the task's workflow to merge the base in and resolve the conflicts. */
  resolvePullRequestConflicts(id: string): Promise<unknown> {
    return this.#request("POST", `/v1/pull-requests/${id}/resolve-conflicts`, {});
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

  /** The sign-in gate's answer for this credential, read before any organisation is mounted. */
  gate(): Promise<MeAnswer> {
    return this.#request("GET", "/v1/me");
  }

  /** Which of your organisations an id is in, for a link made before URLs named one. */
  locate(id: string): Promise<Located> {
    return this.#request("GET", `/v1/locate/${encodeURIComponent(id)}`);
  }

  // -- invitations ----------------------------------------------------------

  /** What an invitation link shows the signed-in email: 404 `invitation_unusable`, 403 `invitation_for_another_email`. */
  invitation(code: string): Promise<InvitationView> {
    return this.#request("GET", `/v1/invitations/${encodeURIComponent(code)}`);
  }

  /** Join: makes your account if there is none, and your membership. */
  acceptInvitation(code: string, name?: string): Promise<{ person: { id: string; role: PersonRole; name: string }; organization: { id: string; name: string; slug: string } }> {
    return this.#request("POST", `/v1/invitations/${encodeURIComponent(code)}/accept`, name ? { name } : {});
  }

  /** Admins: the organisation's invitations, filtered by status and searched on the server. */
  listInvitations(params: { status?: InvitationStatus | "all"; q?: string; cursor?: string } = {}): Promise<{ invitations: InvitationRow[]; cursor: string | null }> {
    return this.#request("GET", `/v1/organization/invitations${qs({ ...params, q: params.q || undefined })}`);
  }

  /** Admins: how many invitations there are in each status, and in all. */
  invitationCounts(): Promise<InvitationCounts> {
    return this.#request("GET", "/v1/organization/invitations/counts");
  }

  /** Admins: invite each address with `role`; one result per address, in order. */
  invite(invitations: Array<{ email: string; name?: string }>, role: PersonRole): Promise<{ results: InviteResult[] }> {
    return this.#request("POST", "/v1/organization/invitations", { invitations, role });
  }

  /** 14 more days, the same link. */
  renewInvitation(id: string): Promise<{ code: string }> {
    return this.#request("POST", `/v1/organization/invitations/${encodeURIComponent(id)}/renew`, {});
  }

  revokeInvitation(id: string): Promise<void> {
    return this.#request("DELETE", `/v1/organization/invitations/${encodeURIComponent(id)}`);
  }

  // -- the super-admin: outside every organisation ------------------------------

  adminOrganizations(): Promise<{ organizations: AdminOrganization[] }> {
    return this.#request("GET", "/v1/admin/organizations");
  }

  /** A new organisation and an admin invitation for `email`; the result carries its link. */
  createOrganization(input: { name: string; slug: string; email: string }): Promise<{ organization: { id: string; name: string; slug: string } } & InviteResult> {
    return this.#request("POST", "/v1/admin/organizations", input);
  }

  adminOrganization(id: string, params: { status?: InvitationStatus | "all"; q?: string } = {}):
    Promise<{ organization: { id: string; name: string; slug: string }; people: AdminPerson[]; invitations: InvitationRow[]; cursor: string | null }> {
    return this.#request("GET", `/v1/admin/organizations/${encodeURIComponent(id)}${qs({ ...params, q: params.q || undefined })}`);
  }

  adminInvitations(id: string, params: { status?: InvitationStatus | "all"; q?: string; cursor?: string } = {}): Promise<{ invitations: InvitationRow[]; cursor: string | null }> {
    return this.#request("GET", `/v1/admin/organizations/${encodeURIComponent(id)}/invitations${qs({ ...params, q: params.q || undefined })}`);
  }

  adminInvitationCounts(id: string): Promise<InvitationCounts> {
    return this.#request("GET", `/v1/admin/organizations/${encodeURIComponent(id)}/invitations/counts`);
  }

  adminInvite(id: string, invitations: Array<{ email: string; name?: string }>, role: PersonRole): Promise<{ results: InviteResult[] }> {
    return this.#request("POST", `/v1/admin/organizations/${encodeURIComponent(id)}/invitations`, { invitations, role });
  }

  adminRenewInvitation(id: string, invitationId: string): Promise<{ code: string }> {
    return this.#request("POST", `/v1/admin/organizations/${encodeURIComponent(id)}/invitations/${encodeURIComponent(invitationId)}/renew`, {});
  }

  adminRevokeInvitation(id: string, invitationId: string): Promise<void> {
    return this.#request("DELETE", `/v1/admin/organizations/${encodeURIComponent(id)}/invitations/${encodeURIComponent(invitationId)}`);
  }

  /** A key-only person's email (linking an account), or anyone's role. */
  adminUpdatePerson(personId: string, change: { email: string } | { role: PersonRole }): Promise<unknown> {
    return this.#request("PATCH", `/v1/admin/people/${encodeURIComponent(personId)}`, change);
  }

  adminRemovePerson(id: string, personId: string): Promise<void> {
    return this.#request("DELETE", `/v1/admin/organizations/${encodeURIComponent(id)}/people/${encodeURIComponent(personId)}`);
  }

  /** Your profile; "" or null clears githubLogin or commitEmail. */
  updateMe(changes: { name?: string; photoUrl?: string | null; githubLogin?: string | null; commitEmail?: string | null }):
    Promise<{ person: Me }> {
    return this.#request("PATCH", "/v1/me", changes);
  }

  /** Your membership in `organizationId` was welcomed: its first-time page is not shown again. Idempotent. */
  markWelcomed(organizationId: string): Promise<{ welcomedAt: string }> {
    return this.#request("POST", `/v1/me/memberships/${encodeURIComponent(organizationId)}/welcomed`, {});
  }

  /** Your photo: the image itself, stored and served back by the backend. */
  uploadMyPhoto(image: Blob): Promise<{ person: Member }> {
    return this.#upload("PUT", "/v1/me/photo", image);
  }

  /** Your keys; over Access, every organisation's, each naming its own. */
  listMyKeys(): Promise<{ keys: ApiKeyInfo[] }> {
    return this.#request("GET", "/v1/me/keys");
  }

  /** A new key for you in `organizationId` (the client's own when omitted); `key` is shown this once. */
  createMyKey(name: string, organizationId = this.#organizationId): Promise<{ id: string; name: string; prefix: string; key: string }> {
    return this.#request("POST", "/v1/me/keys", { name, ...(organizationId ? { organizationId } : {}) });
  }

  revokeMyKey(id: string): Promise<void> {
    return this.#request("DELETE", `/v1/me/keys/${id}`);
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

  createTask(input: { projectId: string } & Partial<TaskFields> & { title: string; pullRequestChoice?: PullRequestChoice | null }): Promise<Task> {
    return this.#request("POST", "/v1/tasks", input);
  }

  /** What Deliver does before the task's pull request, its own (null: its project's): the owner's, until one is open. */
  setPullRequestChoice(taskId: string, choice: PullRequestChoice | null): Promise<{ taskId: string; pullRequestChoice: PullRequestChoice | null }> {
    return this.#request("PUT", `/v1/tasks/${encodeURIComponent(taskId)}/pull-request-choice`, { choice });
  }

  /** The owner's Done without a pull request, at Deliver's hold before it: the branch kept, the task Done. */
  doneWithoutPullRequest(taskId: string): Promise<{ taskId: string; status: "done" }> {
    return this.#request("POST", `/v1/tasks/${encodeURIComponent(taskId)}/done-without-pull-request`);
  }

  /** Edit a task. What it asks for is fixed once delivery starts; where it sits is not. */
  updateTask(id: string, changes: Partial<TaskFields>): Promise<Task> {
    return this.#request("PATCH", `/v1/tasks/${id}`, changes);
  }

  /**
   * Look up the existing work a task would start from: the pull request's
   * title, head and base, or the branch and its base — or why dude will not
   * take it over (an ApiError with the rule's code).
   */
  checkTaskSource(projectId: string, source: TaskSourceInput, taskId?: string): Promise<TaskSource> {
    return this.#request("POST", `/v1/projects/${projectId}/sources/check`, { source, ...(taskId ? { taskId } : {}) });
  }

  /** The project's repositories, each with whether dude may push to it (GitHub asked). */
  sourceRepositories(projectId: string): Promise<{ repositories: SourceRepository[] }> {
    return this.#request("GET", `/v1/projects/${projectId}/sources/repositories`);
  }

  /** A project repository's branches whose names contain `q`: at most 50, `more` when cut short. */
  sourceBranches(projectId: string, repositoryId: string, q: string): Promise<{ branches: string[]; more: boolean }> {
    return this.#request("GET", `/v1/projects/${projectId}/sources/repositories/${encodeURIComponent(repositoryId)}/branches?q=${encodeURIComponent(q)}`);
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
    /** Omitted: the API derives one from the slug. */
    key?: string;
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

  // -- help: dude's own guide, as the orchestrator embeds it --

  helpPages(): Promise<{ pages: HelpPage[] }> {
    return this.#request("GET", "/v1/help");
  }

  helpPage(slug: string): Promise<HelpPage> {
    return this.#request("GET", `/v1/help/${encodeURIComponent(slug)}`);
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

  /** Names a project's agents looked up in the last `days` and lux refused, most called first. */
  refusedNames(projectId: string, days = 7): Promise<{ refused: RefusedName[] }> {
    return this.#request("GET", `/v1/projects/${projectId}/network/refused${qs({ days })}`);
  }

  /** Append names to a project's own egress list: the one-click Allow. */
  allowNames(projectId: string, names: string[]): Promise<SettingsResponse> {
    return this.#request("POST", `/v1/projects/${projectId}/network/allow`, { names });
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

  /** Reset a role's raised machine to its settings' size; `moved` is false when no live Run had to move. */
  resetTaskMachine(id: string, role: string, note = ""): Promise<{ ok: true; moved: boolean }> {
    return this.#request("DELETE", `/v1/tasks/${encodeURIComponent(id)}/agent-machines/${encodeURIComponent(role)}`, note ? { note } : {});
  }

  resetSessionMachine(id: string, note = ""): Promise<{ ok: true; moved: boolean }> {
    return this.#request("DELETE", `/v1/sessions/${encodeURIComponent(id)}/agent-machine`, note ? { note } : {});
  }

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

  /** One small request for a tier's model with its effort, options and headers, as its agent sends it. */
  testModel(input: ModelTestInput): Promise<{ model: string; result: ModelTestResult }> {
    return this.#request("POST", "/v1/models/test", input);
  }

  // -- automations: the organisation's and each project's -------------------

  automations(query: Record<string, string> = {}): Promise<{ automations: Automation[] }> {
    const qs = new URLSearchParams(query).toString();
    return this.#request("GET", `/v1/automations${qs ? `?${qs}` : ""}`);
  }

  projectAutomations(projectId: string): Promise<{ automations: Automation[]; alsoRunningHere: Automation[] }> {
    return this.#request("GET", `/v1/projects/${encodeURIComponent(projectId)}/automations`);
  }

  automation(id: string): Promise<Automation> {
    return this.#request("GET", `/v1/automations/${encodeURIComponent(id)}`);
  }

  createAutomation(input: NewAutomation & { projectId: string | null; tz: string }): Promise<Automation> {
    return this.#request("POST", "/v1/automations", input);
  }

  updateAutomation(id: string, input: AutomationInput): Promise<Automation> {
    return this.#request("PUT", `/v1/automations/${encodeURIComponent(id)}`, input);
  }

  deleteAutomation(id: string): Promise<void> {
    return this.#request("DELETE", `/v1/automations/${encodeURIComponent(id)}`);
  }

  setAutomationEnabled(id: string, enabled: boolean): Promise<Automation> {
    return this.#request("PUT", `/v1/automations/${encodeURIComponent(id)}/enabled`, { enabled });
  }

  transferAutomation(id: string, personId: string): Promise<Automation> {
    return this.#request("PUT", `/v1/automations/${encodeURIComponent(id)}/owner`, { personId });
  }

  automationRepositories(): Promise<{ groups: Array<{ projectId: string; project: string; repos: string[] }> }> {
    return this.#request("GET", "/v1/automations/repositories");
  }

  /** A schedule's next three times, as the orchestrator's scheduler computes them. */
  scheduleNext(cron: string, tz: string): Promise<{ times: string[] }> {
    return this.#request("POST", "/v1/automations/schedule/next", { cron, tz });
  }

  eventSubscriptions(): Promise<EventSubscriptions> {
    return this.#request("GET", "/v1/automations/subscriptions");
  }

  automationRuns(id: string, status?: string): Promise<AutomationRunsResponse> {
    return this.#request("GET", `/v1/automations/${encodeURIComponent(id)}/runs${status ? `?status=${encodeURIComponent(status)}` : ""}`);
  }

  automationRun(id: string, runId: string): Promise<AutomationRunDetail> {
    return this.#request("GET", `/v1/automations/${encodeURIComponent(id)}/runs/${encodeURIComponent(runId)}`);
  }

  automationDeliveries(id: string): Promise<{ deliveries: PastDelivery[] }> {
    return this.#request("GET", `/v1/automations/${encodeURIComponent(id)}/deliveries`);
  }

  runAutomation(id: string, body: RunNow): Promise<{ runId: string }> {
    return this.#request("POST", `/v1/automations/${encodeURIComponent(id)}/runs`, body);
  }

  evaluateTestEvent(id: string, body: Record<string, unknown>): Promise<EventVerdict> {
    return this.#request("POST", `/v1/automations/${encodeURIComponent(id)}/test-event/evaluate`, { body });
  }

  startAutomationFresh(id: string): Promise<void> {
    return this.#request("POST", `/v1/automations/${encodeURIComponent(id)}/start-fresh`);
  }

  automationPromptHistory(id: string): Promise<{ versions: AutomationPromptVersion[] }> {
    return this.#request("GET", `/v1/automations/${encodeURIComponent(id)}/prompt-history`);
  }

  restoreAutomationPrompt(id: string, versionId: string): Promise<Automation> {
    return this.#request("POST", `/v1/automations/${encodeURIComponent(id)}/prompt-history/restore`, { versionId });
  }

  duplicateAutomation(id: string): Promise<Automation> {
    return this.#request("POST", `/v1/automations/${encodeURIComponent(id)}/duplicate`);
  }

  // -- judgements: the organisation's judgement tier and its calls -----------

  judgementSettings(): Promise<JudgementSettings> {
    return this.#request("GET", "/v1/judgements/settings");
  }

  /** Sets the judgement tier; null is Off. */
  setJudgementTier(tierId: string | null): Promise<JudgementSettings> {
    return this.#request("PUT", "/v1/judgements/settings", { tierId });
  }

  judgements(query: { asker?: string | undefined; limit?: number | undefined; before?: string | undefined } = {}): Promise<JudgementCallsResponse> {
    const params = new URLSearchParams();
    if (query.asker) params.set("asker", query.asker);
    if (query.limit) params.set("limit", String(query.limit));
    if (query.before) params.set("before", query.before);
    const qs = params.toString();
    return this.#request("GET", `/v1/judgements${qs ? `?${qs}` : ""}`);
  }

  judgement(id: string): Promise<JudgementCallDetail> {
    return this.#request("GET", `/v1/judgements/${encodeURIComponent(id)}`);
  }

  /** The test question, asked of a tier as a judgement would be; not recorded. */
  testJudgement(tierId: string): Promise<JudgementTestResult> {
    return this.#request("POST", "/v1/judgements/test", { tierId });
  }

  // -- chat providers ---------------------------------------------------------

  chatOverview(): Promise<ChatOverview> {
    return this.#request("GET", "/v1/organization/chat");
  }

  chatInstallation(provider: string): Promise<ChatInstallationView> {
    return this.#request("GET", `/v1/organization/chat/${encodeURIComponent(provider)}`);
  }

  /** An admin installing a provider's app: the provider's page to send the browser to. */
  installChat(provider: string): Promise<{ url: string }> {
    return this.#request("POST", `/v1/chat/${encodeURIComponent(provider)}/install`, {});
  }

  disconnectChat(provider: string): Promise<void> {
    return this.#request("DELETE", `/v1/organization/chat/${encodeURIComponent(provider)}`);
  }

  /** Linking your provider account; `prompt` is the connect page's state, whose kept mention is routed once linked. */
  connectChat(provider: string, prompt?: string): Promise<{ url: string }> {
    return this.#request("POST", `/v1/chat/${encodeURIComponent(provider)}/connect`, prompt ? { prompt } : {});
  }

  unlinkChat(provider: string): Promise<void> {
    return this.#request("DELETE", `/v1/me/chat/${encodeURIComponent(provider)}`);
  }

  chatConnectState(provider: string, state: string): Promise<ChatConnectStateView> {
    return this.#request("GET", `/v1/chat/${encodeURIComponent(provider)}/connect/state?state=${encodeURIComponent(state)}`);
  }

  routingInstructions(): Promise<RoutingInstructions> {
    return this.#request("GET", "/v1/organization/routing-instructions");
  }

  setRoutingInstructions(additional: string): Promise<RoutingInstructions> {
    return this.#request("PUT", "/v1/organization/routing-instructions", { additional });
  }

  providerRoutingInstructions(provider: string): Promise<ProviderRoutingInstructions & { installed: boolean }> {
    return this.#request("GET", `/v1/organization/chat/${encodeURIComponent(provider)}/routing-instructions`);
  }

  setProviderRoutingInstructions(provider: string, additional: string): Promise<ProviderRoutingInstructions & { installed: boolean }> {
    return this.#request("PUT", `/v1/organization/chat/${encodeURIComponent(provider)}/routing-instructions`, { additional });
  }

  chatProjects(provider: string): Promise<ChatProjectsView> {
    return this.#request("GET", `/v1/organization/chat/${encodeURIComponent(provider)}/projects`);
  }

  setChannelProjects(provider: string, conversationId: string, conversationName: string, projectIds: string[]): Promise<ChatProjectsView> {
    return this.#request("PUT", `/v1/organization/chat/${encodeURIComponent(provider)}/channels/${encodeURIComponent(conversationId)}/projects`,
      { conversationName, projectIds });
  }

  setHiddenProjects(provider: string, projectIds: string[]): Promise<ChatProjectsView> {
    return this.#request("PUT", `/v1/organization/chat/${encodeURIComponent(provider)}/hidden-projects`, { projectIds });
  }

  chatRequests(filter: { provider?: string | undefined; route?: string | undefined; limit?: number | undefined; summary?: boolean | undefined } = {}):
    Promise<{ requests: ChatRequestRow[]; week?: ChatRequestsSummary }> {
    const params = new URLSearchParams();
    if (filter.provider) params.set("provider", filter.provider);
    if (filter.route) params.set("route", filter.route);
    if (filter.limit) params.set("limit", String(filter.limit));
    if (filter.summary) params.set("summary", "1");
    const qs = params.toString();
    return this.#request("GET", `/v1/organization/chat/requests${qs ? `?${qs}` : ""}`);
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
  saveImageDraft(id: string, draft: { containerfile: string; buildArgs?: Record<string, string>; note?: string; canRunContainers?: boolean }): Promise<ImageDetail> {
    return this.#request("PUT", `/v1/images/${encodeURIComponent(id)}/draft`, draft);
  }

  discardImageDraft(id: string): Promise<ImageDetail> {
    return this.#request("DELETE", `/v1/images/${encodeURIComponent(id)}/draft`);
  }

  /** Build & publish: the draft (saved first, when given) numbered and queued. */
  buildImage(
    id: string,
    draft?: { containerfile: string; buildArgs?: Record<string, string>; note?: string; canRunContainers?: boolean },
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

  /** An epic's notes: the current ones (at most `limit`), and the replaced and retired. */
  epicNotes(epicId: string): Promise<EpicNotes> {
    return this.#request("GET", `/v1/epics/${encodeURIComponent(epicId)}/notes`);
  }

  /**
   * Pin a note on an epic, or replace note `replaces`, as the person: every
   * live agent on the epic is told at its next step. 409 past the limit
   * (`note_limit`) or for a note no longer current.
   */
  pinEpicNote(epicId: string, input: PinNoteInput): Promise<PinnedNote> {
    return this.#request("POST", `/v1/epics/${encodeURIComponent(epicId)}/notes`, input);
  }

  /** Retire note `n` with no replacement: a person's alone; agents are told. */
  retireEpicNote(epicId: string, n: number): Promise<NoteRecord> {
    return this.#request("POST", `/v1/epics/${encodeURIComponent(epicId)}/notes/${n}/retire`, {});
  }

  /** The threads between an epic's tasks' agents: read only, for people. */
  epicThreads(epicId: string): Promise<EpicThreads> {
    return this.#request("GET", `/v1/epics/${encodeURIComponent(epicId)}/threads`);
  }

  epicWaiting(epicId: string): Promise<{ epicId: string; waiting: number }> {
    return this.#request("GET", `/v1/epics/${encodeURIComponent(epicId)}/waiting`);
  }

  /** Each task on an epic as its peers see it: phase, live or parked, whom it waits on. */
  epicPeers(epicId: string): Promise<EpicPeers> {
    return this.#request("GET", `/v1/epics/${encodeURIComponent(epicId)}/peers`);
  }

  /**
   * Start the delivery workflow: implement, review, fix, simplify, then a
   * pull request. Idempotent — a second call joins the delivery in flight.
   */
  deliver(taskId: string, start: StartAnyway = {}): Promise<{ workflowRunId: string; alreadyRunning: boolean }> {
    return this.#request("POST", `/v1/tasks/${taskId}/deliver`, start);
  }

  /** A task's relations, what holds it up and what it holds up, and its start when unblocked. */
  relations(taskId: string): Promise<TaskRelations> {
    return this.#request("GET", `/v1/tasks/${encodeURIComponent(taskId)}/relations`);
  }

  /** Relate a task to another, named by key or id; refused as `cycle` (details.loop), `duplicate` or `self`. */
  addRelation(taskId: string, kind: RelationSide, other: string, why?: string): Promise<TaskRelations> {
    return this.#request("POST", `/v1/tasks/${encodeURIComponent(taskId)}/relations`, { kind, task: other, ...(why ? { why } : {}) });
  }

  removeRelation(taskId: string, kind: RelationSide, other: string): Promise<void> {
    return this.#request("DELETE", `/v1/tasks/${encodeURIComponent(taskId)}/relations/${kind}/${encodeURIComponent(other)}`);
  }

  /** Start the task with `how` once its last blocker merges: its owner only. */
  setAutoStart(taskId: string, how: AutoStartHow): Promise<TaskRelations> {
    return this.#request("PUT", `/v1/tasks/${encodeURIComponent(taskId)}/auto-start`, { how });
  }

  cancelAutoStart(taskId: string): Promise<void> {
    return this.#request("DELETE", `/v1/tasks/${encodeURIComponent(taskId)}/auto-start`);
  }

  /** Approve or decline an agent's request for a repository. */
  decideRepositoryRequest(id: string, approve: boolean, note = ""): Promise<{ status: string }> {
    return this.#request("POST", `/v1/repository-requests/${encodeURIComponent(id)}/decide`, { approve, note });
  }

  /** How long a task's agents worked and waited, how long it sat in review, what it cost; with `attempt`, that attempt's Runs' alone. */
  taskMetrics(taskId: string, attempt?: number): Promise<TaskMetrics> {
    return this.#request("GET", `/v1/tasks/${encodeURIComponent(taskId)}/metrics${attempt ? `?attempt=${attempt}` : ""}`);
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

  /** Whether this browser's subscription is registered in this client's organisation. */
  pushSubscribed(endpoint: string): Promise<{ subscribed: boolean }> {
    return this.#request("POST", "/v1/push/subscriptions/lookup", { endpoint });
  }

  /** Remove this browser's registration in this client's organisation only. */
  unsubscribePush(endpoint: string): Promise<void> {
    return this.#request("POST", "/v1/push/subscriptions/remove", { endpoint });
  }

  /** Finished work with nothing to merge — a write-up, a design — is done once a person has read it. */
  markDone(taskId: string): Promise<{ status: string }> {
    return this.#request("POST", `/v1/tasks/${taskId}/done`, {});
  }

  /** Close a task, saying why: its owner or an admin. */
  closeTask(taskId: string, decision: CloseDecision): Promise<unknown> {
    return this.#request("POST", `/v1/tasks/${encodeURIComponent(taskId)}/close`, decision);
  }

  /** Undo a Close: back to Backlog, its runs kept. Refused on an archived task. */
  reopenTask(taskId: string): Promise<unknown> {
    return this.#request("POST", `/v1/tasks/${encodeURIComponent(taskId)}/reopen`, {});
  }

  /** Archive a task: one that hasn't ended is closed with the decision first, in one step. */
  archiveTask(taskId: string, decision?: CloseDecision): Promise<unknown> {
    return this.#request("POST", `/v1/tasks/${encodeURIComponent(taskId)}/archive`, decision ?? {});
  }

  unarchiveTask(taskId: string): Promise<unknown> {
    return this.#request("POST", `/v1/tasks/${encodeURIComponent(taskId)}/unarchive`, {});
  }

  /** Archive an epic with its tasks; with withEndedTasks, its ended tasks alone. 409 unfinished names the rest. */
  archiveEpic(epicId: string, withEndedTasks = false): Promise<{ archived: boolean; tasks: string[] }> {
    return this.#request("POST", `/v1/epics/${encodeURIComponent(epicId)}/archive`, withEndedTasks ? { withEndedTasks: true } : {});
  }

  unarchiveEpic(epicId: string): Promise<{ tasks: string[] }> {
    return this.#request("POST", `/v1/epics/${encodeURIComponent(epicId)}/unarchive`, {});
  }

  /** A project's archived tasks, newest archived first. */
  archivedTasks(projectId: string): Promise<{ tasks: Task[] }> {
    return this.#request("GET", `/v1/tasks${qs({ projectId, archived: "1" })}`);
  }

  /** How a stopped task can be picked back up now, and until when a resume can. */
  recoveryOptions(taskId: string): Promise<RecoveryOptions> {
    return this.#request("GET", `/v1/tasks/${taskId}/recover`);
  }

  /** Pick a stopped task back up — the task's owner does — with a note for the agents; Start over may say who decides the new attempt. */
  recover(taskId: string, action: RecoverAction, note: string, start: { override?: true; decider?: Decider } = {}): Promise<{ action: RecoverAction }> {
    return this.#request("POST", `/v1/tasks/${taskId}/recover`, { action, note, ...start });
  }

  /** Stop the whole task where it stands, or stop it and start its next attempt (who decides it: `decider`). Owner or admin. */
  stopTask(taskId: string, stop: StopRequest): Promise<StopResult> {
    return this.#request("POST", `/v1/tasks/${encodeURIComponent(taskId)}/stop`, stop);
  }

  /**
   * How delivery goes on after it stopped for a person: the task's owner decides. scope "attempt" offers a
   * checks waiver to later Runs of the attempt reporting the same failure.
   */
  decide(taskId: string, action: EscalationAction, note: string, scope?: "attempt"): Promise<{ action: EscalationAction }> {
    return this.#request("POST", `/v1/tasks/${taskId}/decide`, { action, note, ...(scope ? { scope } : {}) });
  }

  // -- intervention (plan §24) --------------------------------------------

  /** Answer an agent's question through its form: one answer per question, a note (several only), the note's images. */
  answerQuestions(questionId: string, answers: ReadonlyArray<SentAnswer>, note = "", attachmentIds: ReadonlyArray<string> = []): Promise<{ id: string; status: "answered" }> {
    return this.#request("POST", `/v1/questions/${questionId}/answer`, {
      answers, ...(note ? { note } : {}), ...(attachmentIds.length > 0 ? { attachmentIds } : {}),
    });
  }

  // -- images a person sends an agent -------------------------------------

  /** Whether images can be attached here, and the limits the browser keeps to. */
  attachmentLimits(): Promise<AttachmentLimits> {
    return this.#request("GET", "/v1/attachment-limits");
  }

  /**
   * Upload one image to a task, or with `route` "brainstorms" to a session:
   * the original as picked and the variant the agent is sent. `onProgress`
   * follows the upload (0..1). XHR, because fetch reports no upload progress.
   */
  uploadAttachment(taskId: string, image: { name: string; original: Blob; delivered: Blob },
    onProgress?: (fraction: number) => void, route: UploadRoute = "tasks"): Promise<AttachmentInfo> {
    const form = new FormData();
    form.set("name", image.name);
    form.set("original", image.original, image.name);
    form.set("originalType", image.original.type);
    form.set("delivered", image.delivered, image.name);
    form.set("deliveredType", image.delivered.type);
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      xhr.open("POST", `${this.#baseUrl}/v1/${route}/${encodeURIComponent(taskId)}/attachments`);
      xhr.withCredentials = true;
      if (this.#apiKey) xhr.setRequestHeader("authorization", `Bearer ${this.#apiKey}`);
      if (this.#organizationId) xhr.setRequestHeader("x-dude-organization", this.#organizationId);
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

  /**
   * A message in a task's Chat: it starts the task's conductor, or is its
   * next input (or the answer to its question). Says which Run heard it.
   */
  chat(taskId: string, text: string, opts: { aside?: boolean } = {}): Promise<ChatSent> {
    return this.#request("POST", `/v1/tasks/${taskId}/chat`, { text, ...(opts.aside ? { aside: true } : {}) });
  }

  /** Try again on a task whose conductor failed: a fresh conductor takes what it never read. */
  retryConductor(taskId: string): Promise<{ runId: string; taskId: string; created: boolean }> {
    return this.#request("POST", `/v1/tasks/${encodeURIComponent(taskId)}/retry`, {});
  }

  /** The conductor's question answered through its form, in the task's Chat. */
  chatAnswer(taskId: string, questionId: string, answers: ReadonlyArray<SentAnswer>, note = ""): Promise<ChatSent> {
    return this.#request("POST", `/v1/tasks/${taskId}/chat`, { questionId, answers, ...(note ? { note } : {}) });
  }

  /**
   * Talk it through: a task not started gets its conductor, which plans it
   * with the person; nothing is built until it decides to.
   */
  talk(taskId: string, start: StartAnyway = {}): Promise<ChatSent> {
    return this.#request("POST", `/v1/tasks/${taskId}/talk`, start);
  }

  // -- brainstorm sessions --------------------------------------------------

  /** Your sessions, the invitations waiting on you, and questions put to you. With `archived`, the sessions you archived too. */
  sessions(opts: { archived?: boolean } = {}): Promise<SessionsList> {
    return this.#request("GET", opts.archived ? "/v1/brainstorms?archived=1" : "/v1/brainstorms");
  }

  getSession(id: string): Promise<SessionDetail> {
    return this.#request("GET", `/v1/brainstorms/${encodeURIComponent(id)}`);
  }

  /**
   * A new session. Given its first message, it is made with that message and
   * what it reads in one call, its agent started (`runId`); without one it is
   * untitled and empty until someone writes.
   */
  createSession(input: { message?: string; projects?: SessionLink[]; tier?: string; harness?: Harness } = {}): Promise<{ id: string; title: string | null; runId?: string }> {
    return this.#request("POST", "/v1/brainstorms", input);
  }

  /** The owner sets the model tier and harness its agent's next start uses; null follows the organisation. */
  setSessionModel(id: string, choice: { tier: string | null; harness: Harness | null }): Promise<{ id: string; model: SessionModel }> {
    return this.#request("POST", `/v1/brainstorms/${encodeURIComponent(id)}/model`, choice);
  }

  /** Name the session, as a member who can chat: the agent leaves a person's name alone. */
  renameSession(id: string, title: string): Promise<{ id: string; title: string }> {
    return this.#request("POST", `/v1/brainstorms/${encodeURIComponent(id)}/title`, { title });
  }

  /** A message to the session's agent: it starts it, or is its next input (or the answer it waits on from you). */
  sessionChat(id: string, text: string, opts: { aside?: boolean; attachmentIds?: ReadonlyArray<string>; mentions?: ReadonlyArray<string>; shareWith?: ReadonlyArray<string> } = {}): Promise<{ runId: string; created: boolean; questionId?: string; directiveId?: string; attachments?: AttachmentInfo[] }> {
    return this.#request("POST", `/v1/brainstorms/${encodeURIComponent(id)}/chat`, { text, ...(opts.aside ? { aside: true } : {}), ...(opts.attachmentIds?.length ? { attachmentIds: opts.attachmentIds } : {}),
      ...(opts.mentions?.length ? { mentions: opts.mentions } : {}), ...(opts.shareWith?.length ? { shareWith: opts.shareWith } : {}) });
  }

  /** Try again on a session whose agent failed, to start or to resume: a fresh agent takes what it never read. */
  retrySessionAgent(id: string): Promise<{ runId: string; created: boolean }> {
    return this.#request("POST", `/v1/brainstorms/${encodeURIComponent(id)}/retry`, {});
  }

  /** The session agent's question answered through its form. */
  sessionAnswer(id: string, questionId: string, answers: ReadonlyArray<SentAnswer>, note = ""): Promise<{ id: string; status: "answered" }> {
    return this.#request("POST", `/v1/brainstorms/${encodeURIComponent(id)}/questions/${encodeURIComponent(questionId)}/answer`,
      { answers, ...(note ? { note } : {}) });
  }

  linkSession(id: string, projects: SessionLink[]): Promise<{ id: string }> {
    return this.#request("POST", `/v1/brainstorms/${encodeURIComponent(id)}/link`, { projects });
  }

  inviteToSession(id: string, people: string[], role: "chat" | "read"): Promise<{ id: string }> {
    return this.#request("POST", `/v1/brainstorms/${encodeURIComponent(id)}/people`, { people, role });
  }

  setSessionRole(id: string, person: string, role: "chat" | "read"): Promise<{ id: string }> {
    return this.#request("POST", `/v1/brainstorms/${encodeURIComponent(id)}/people/${encodeURIComponent(person)}/role`, { role });
  }

  removeFromSession(id: string, person: string): Promise<{ id: string }> {
    return this.#request("POST", `/v1/brainstorms/${encodeURIComponent(id)}/people/${encodeURIComponent(person)}/remove`);
  }

  /** Make someone else the owner; you keep chat or read, or leave. */
  handOverSession(id: string, person: string, keep: "chat" | "read" | "leave"): Promise<{ id: string; pending: boolean }> {
    return this.#request("POST", `/v1/brainstorms/${encodeURIComponent(id)}/owner`, { person, keep });
  }

  acceptSession(id: string): Promise<{ id: string }> {
    return this.#request("POST", `/v1/brainstorms/${encodeURIComponent(id)}/accept`);
  }

  declineSession(id: string): Promise<{ id: string }> {
    return this.#request("POST", `/v1/brainstorms/${encodeURIComponent(id)}/decline`);
  }

  /** File items of the agent's proposal, as you. */
  fileProposal(id: string, proposalId: string, items: number[]): Promise<FileResult> {
    return this.#request("POST", `/v1/brainstorms/${encodeURIComponent(id)}/file`, { proposalId, items });
  }

  /** Say you have the session open (or no longer): its members see it, no one else. */
  sessionOpen(id: string, open: boolean): Promise<{ open: boolean }> {
    return this.#request("POST", `/v1/brainstorms/${encodeURIComponent(id)}/open`, { open });
  }

  /** Take the session out of your own list and sidebar (or put it back): nobody else's, and nothing else changes. */
  archiveSession(id: string, archived: boolean): Promise<{ id: string; archived: boolean }> {
    return this.#request("POST", `/v1/brainstorms/${encodeURIComponent(id)}/${archived ? "archive" : "unarchive"}`);
  }

  /** Every session open to the organisation, each with whether you joined it, and how many you joined. */
  openSessions(): Promise<OpenSessionsList> {
    return this.#request("GET", "/v1/brainstorms/open");
  }

  /** Open the session to everyone in the organisation: one way, the owner's (or an admin's while its admins own it). */
  openSessionToOrganisation(id: string): Promise<{ id: string; access: "organisation"; changed: boolean }> {
    return this.#request("PUT", `/v1/brainstorms/${encodeURIComponent(id)}/access`, { access: "organisation" });
  }

  /** An admin makes themselves the owner of a session its admins own (its owner left). */
  takeSessionOwnership(id: string): Promise<{ id: string }> {
    return this.#request("POST", `/v1/brainstorms/${encodeURIComponent(id)}/take-ownership`);
  }

  /**
   * Who takes the delivery's decisions from the next one on: "policy" is Let Deliver finish it.
   * openPullRequest confirms that Deliver opens the pull request now, past the conductor's gate question;
   * with it, draft is the person's Open (false) or Draft (true).
   */
  setDecider(taskId: string, decider: Decider, openPullRequest = false, draft?: boolean): Promise<{ taskId: string; decider: Decider }> {
    return this.#request("POST", `/v1/tasks/${taskId}/decider`,
      openPullRequest ? { decider, openPullRequest, ...(draft === undefined ? {} : { draft }) } : { decider });
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

  /** The project's secrets: names, recipients and hints, never a value. */
  projectSecrets(projectId: string): Promise<{ secrets: ProjectSecret[] }> {
    return this.#request("GET", `/v1/projects/${encodeURIComponent(projectId)}/secrets`);
  }

  addProjectSecret(projectId: string, name: string, value: string, recipients?: readonly SecretRecipient[]): Promise<ProjectSecret> {
    return this.#request("POST", `/v1/projects/${encodeURIComponent(projectId)}/secrets`, recipients ? { name, value, recipients } : { name, value });
  }

  changeProjectSecretRecipients(projectId: string, name: string, recipients: readonly SecretRecipient[]): Promise<ProjectSecret> {
    return this.#request("PUT", `/v1/projects/${encodeURIComponent(projectId)}/secrets/${encodeURIComponent(name)}/recipients`, { recipients });
  }

  replaceProjectSecret(projectId: string, name: string, value: string): Promise<ProjectSecret> {
    return this.#request("PUT", `/v1/projects/${encodeURIComponent(projectId)}/secrets/${encodeURIComponent(name)}`, { value });
  }

  removeProjectSecret(projectId: string, name: string): Promise<void> {
    return this.#request("DELETE", `/v1/projects/${encodeURIComponent(projectId)}/secrets/${encodeURIComponent(name)}`);
  }

  /** The run serving a task — its agent's, else its branch preview — and that run's servers. */
  taskServers(taskId: string): Promise<TaskServers> {
    return this.#request("GET", `/v1/tasks/${encodeURIComponent(taskId)}/servers`);
  }

  runServers(runId: string): Promise<TaskServers> {
    return this.#request("GET", `/v1/runs/${encodeURIComponent(runId)}/servers`);
  }

  /** The task, among your organisations', whose live preview is served at an https origin; 404 when none is yours. */
  previewServerAt(origin: string): Promise<PreviewServerAt> {
    return this.#request("GET", `/v1/preview-servers/lookup?origin=${encodeURIComponent(origin)}`);
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

  /** A stalled Run, started over in its slot: a fresh agent, told the note. Returns the new Run. */
  restart(runId: string, note: string): Promise<{ runId: string; replaced: string }> {
    return this.#request("POST", `/v1/runs/${runId}/restart`, { note });
  }

  /** A stalled Run left as it is: its owner is not asked again; its time limit stops it. */
  leaveStalled(runId: string): Promise<{ runId: string; left: true }> {
    return this.#request("POST", `/v1/runs/${runId}/leave`, {});
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
    return `${this.#baseUrl}/v1/events/stream${qs({ ...rest, ...(live ? { live: 1 } : {}),
      ...(this.#organizationId ? { org: this.#organizationId } : {}), ...(this.#apiKey ? { key: this.#apiKey } : {}) })}`;
  }

  /** The organisation's secrets: names, recipients, hints; which projects have their own; who picks each (admins). */
  organizationSecrets(orgId: string): Promise<OrgSecretsResponse> {
    return this.#request("GET", `/v1/organizations/${encodeURIComponent(orgId)}/secrets`);
  }

  addOrganizationSecret(orgId: string, name: string, value: string, recipients: readonly SecretRecipient[]): Promise<OrgSecret> {
    return this.#request("POST", `/v1/organizations/${encodeURIComponent(orgId)}/secrets`, { name, value, recipients });
  }

  changeOrganizationSecretRecipients(orgId: string, name: string, recipients: readonly SecretRecipient[]): Promise<OrgSecret> {
    return this.#request("PUT", `/v1/organizations/${encodeURIComponent(orgId)}/secrets/${encodeURIComponent(name)}/recipients`, { recipients });
  }

  replaceOrganizationSecret(orgId: string, name: string, value: string): Promise<OrgSecret> {
    return this.#request("PUT", `/v1/organizations/${encodeURIComponent(orgId)}/secrets/${encodeURIComponent(name)}`, { value });
  }

  removeOrganizationSecret(orgId: string, name: string): Promise<void> {
    return this.#request("DELETE", `/v1/organizations/${encodeURIComponent(orgId)}/secrets/${encodeURIComponent(name)}`);
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

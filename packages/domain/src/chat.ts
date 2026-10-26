import { z } from "zod";
import type { PersonRef } from "./hierarchy.ts";

/**
 * Chat providers (Slack first): an organisation's admin installs a
 * provider's app in one workspace, its people link their provider accounts,
 * and a mention of dude there is routed — by one judgement on the
 * organisation's Judgements tier — to a brainstorm session, a task, an epic
 * with its tasks, a session to resume, or a clarifying question. Everything
 * here is provider-neutral: a provider is an id with a name and a glyph.
 */

/** A provider as shared surfaces draw it: its name and its glyph's id. */
export const chatProviderSchema = z.object({
  id: z.string().regex(/^[a-z][a-z0-9_]{1,31}$/),
  name: z.string().min(1),
  /** An icon id the web maps to its icon set; "integration" when the provider has none of its own. */
  glyph: z.string().min(1),
  /** What the provider calls a workspace ("workspace", "server"). */
  workspaceWord: z.string().min(1),
});
export type ChatProvider = z.infer<typeof chatProviderSchema>;

/** Where a mention was made, which decides who gets a session made from it. */
export const CONVERSATION_KINDS = ["public", "private", "group", "dm"] as const;
export const conversationKindSchema = z.enum(CONVERSATION_KINDS);
export type ConversationKind = z.infer<typeof conversationKindSchema>;

/** What the routing judgement may answer. */
export const ROUTER_ROUTES = ["brainstorm", "task", "epic", "resume", "clarify", "channel_projects"] as const;
/** Every route a request is logged with: the router's, a bound thread's link, and the refusals. */
export const CHAT_ROUTES = [...ROUTER_ROUTES, "task_link", "not_connected", "not_member", "failed"] as const;
export const chatRouteSchema = z.enum(CHAT_ROUTES);
export type ChatRoute = z.infer<typeof chatRouteSchema>;

/** The fixed facts of routing, the same for every provider and not settings. */
export const CHAT_CONFIDENCE_BAR = 0.75;
export const CHAT_CLARIFY_ROUNDS = 2;
export const CHAT_EPIC_TASKS_MAX = 12;
export const CHAT_INSTRUCTIONS_MAX = 4000;
/** Requests are kept this long. */
export const CHAT_REQUESTS_DAYS = 30;

/** Where a session, task or epic came from outside dude (tasks.origin, epics.origin, sessions.origin). */
export const chatOriginSchema = z.object({
  provider: z.string().min(1),
  workspaceId: z.string(),
  conversationId: z.string(),
  conversationName: z.string(),
  conversationKind: conversationKindSchema.optional(),
  threadId: z.string(),
  permalink: z.string(),
  /** The provider display name of who asked. */
  requestedBy: z.string(),
  /** The thread as it was filed (a task's, a session's); `app` marks a bot's message. */
  thread: z.array(z.object({ by: z.string(), handle: z.string().optional(), person: z.string().optional(), text: z.string(), inDude: z.boolean(),
    app: z.boolean().optional() }))
    .optional(),
  /** The channel before a request made outside a thread, as it was filed. */
  channel: z.array(z.object({ by: z.string(), handle: z.string().optional(), person: z.string().optional(), text: z.string(), inDude: z.boolean(),
    app: z.boolean().optional() }))
    .optional(),
});
export type ChatOrigin = z.infer<typeof chatOriginSchema>;

/** A chat-born session's origin: the thread as the session was given it, and how it was routed. */
export interface SessionChatOrigin extends ChatOrigin {
  routing?: {
    route: ChatRoute;
    askedRoute?: ChatRoute;
    confidence: number | null;
    reasoning: string;
    /** Person ids added, and provider user ids skipped (not connected). */
    added: string[];
    skipped: string[];
    /** The skipped people's display names, in `skipped`'s order. */
    skippedNames?: string[];
    requestId: string;
  };
}

/** `GET /v1/organization/chat`: each provider dude has, installed here or not. */
export interface ChatProviderEntry {
  provider: ChatProvider;
  installed: boolean;
  workspaceName: string | null;
  /** The workspace's address at the provider, when it has one. */
  workspaceDomain?: string | null;
  installedAt: string | null;
  /** The caller's own link in this provider's workspace, and their mentions of the last 30 days. */
  connected: { handle: string; displayName: string; linkedAt: string; mentions?: number } | null;
}
export interface ChatOverview {
  providers: ChatProviderEntry[];
  canEdit: boolean;
  /** The organisation's admins, for "Ask an admin". */
  admins: PersonRef[];
}

/** `GET /v1/organization/chat/:provider`. Health and people are an admin's only. */
export interface ChatInstallationView {
  provider: ChatProvider;
  installed: boolean;
  canEdit: boolean;
  /** The operator configured this provider's app (its credentials); without it nobody can install. */
  configured: boolean;
  workspaceName: string | null;
  workspaceDomain: string | null;
  installedAt: string | null;
  installedBy: PersonRef | null;
  /** The app's bot user at the provider, by its id there. */
  botUser?: string | null;
  scopes: string[];
  connectedCount: number;
  connected: { handle: string; displayName: string; linkedAt: string } | null;
  health: { lastEventAt: string | null; failedToday: number; lastFailure: string | null } | null;
  people: Array<{ person: PersonRef; handle: string; displayName: string; linkedAt: string }> | null;
  admins: PersonRef[];
}

/** `GET /v1/organization/routing-instructions`. */
export interface RoutingInstructions {
  builtIn: string;
  additional: string;
  canEdit: boolean;
}
/** `GET /v1/organization/chat/:provider/routing-instructions`: the provider's layer and the whole order. */
export interface ProviderRoutingInstructions extends RoutingInstructions {
  organisation: string;
}
export const routingInstructionsInputSchema = z.object({ additional: z.string().max(CHAT_INSTRUCTIONS_MAX) }).strict();
export type RoutingInstructionsInput = z.infer<typeof routingInstructionsInputSchema>;

/** A project as the chat settings name it. */
export interface ChatProjectRef { id: string; key: string; name: string }
/**
 * `GET /v1/organization/chat/:provider/projects`: the conversations dude has
 * heard from (or has hints for), each with the projects it is usually about
 * (a hint the routing model weighs, never a rule); the projects hidden from
 * this provider (nothing it starts can see them); and every project, to pick
 * from. Admins edit; members read.
 */
export interface ChatProjectsView {
  channels: Array<{ conversationId: string; conversationName: string; projects: ChatProjectRef[] }>;
  hidden: ChatProjectRef[];
  projects: ChatProjectRef[];
  canEdit: boolean;
}
/** At most so many usual projects per conversation, and hidden projects per provider. */
export const CHAT_PROJECTS_MAX = 50;
export const chatChannelProjectsInputSchema = z.object({
  conversationName: z.string().max(200).optional(),
  projectIds: z.array(z.string().min(1)).max(CHAT_PROJECTS_MAX),
}).strict();
export const chatHiddenProjectsInputSchema = z.object({ projectIds: z.array(z.string().min(1)).max(CHAT_PROJECTS_MAX) }).strict();

/** One row of Requests. */
export interface ChatRequestRow {
  id: string;
  provider: string;
  conversationKind: ConversationKind | null;
  conversationName: string;
  externalUserName: string;
  person: PersonRef | null;
  text: string;
  permalink: string;
  route: ChatRoute;
  askedRoute: ChatRoute | null;
  confidence: number | null;
  reasoning: string;
  rounds: number;
  result: { kind: "session" | "task" | "epic"; id: string; label: string | null } | null;
  sharedAdded: PersonRef[];
  sharedSkipped: string[];
  createdAt: string;
}
export const CHAT_REQUESTS_MAX = 200;
export const chatRequestsQuerySchema = z.object({
  provider: z.string().optional(),
  route: chatRouteSchema.optional(),
  /** Only the newest so many (at most CHAT_REQUESTS_MAX). */
  limit: z.coerce.number().int().min(1).max(CHAT_REQUESTS_MAX).optional(),
  /** "1": also count the last 7 days, over every row the filters match. */
  summary: z.enum(["1"]).optional(),
});
/** The last 7 days of the filtered Requests: mentions, and those that asked the model. */
export interface ChatRequestsSummary { mentions: number; asked: number }

/** Where a member's connect callback lands (`?chat=<provider>&outcome=<o>`). */
export const CHAT_CONNECT_OUTCOMES = ["connected", "authorization_denied", "invalid_or_expired", "workspace_not_installed",
  "already_linked", "wrong_organization", "failed"] as const;
export type ChatConnectOutcome = (typeof CHAT_CONNECT_OUTCOMES)[number];
/** Where an admin's install callback lands (`?chatInstall=<provider>&outcome=<o>`). */
export const CHAT_INSTALL_OUTCOMES = ["installed", "authorization_denied", "invalid_or_expired", "workspace_taken", "failed"] as const;
export type ChatInstallOutcome = (typeof CHAT_INSTALL_OUTCOMES)[number];

/** `GET /v1/chat/:provider/connect/state?state=`: what the connect page shows before anyone signs in with the provider. */
export interface ChatConnectStateView {
  status: "ready" | "connected" | "expired" | "wrong_organization";
  provider: ChatProvider;
  workspaceName: string | null;
  /** The workspace's address at the provider ("acme-hq" of acme-hq.slack.com), when it has one. */
  workspaceDomain?: string | null;
  connected: { handle: string; displayName: string } | null;
  /** Ready: the provider account the prompt was sent to, which signing in links. */
  asker?: { id: string; handle: string; displayName: string } | null;
  /** The message that offered the Connect button, at the provider: "Back to <provider>". */
  backUrl?: string | null;
}

/**
 * Which routes made something, linked something, or refused: a refusal or a
 * failure reads quieter than a route that made or linked work.
 */
export function routeMadeSomething(route: ChatRoute): boolean {
  return route === "brainstorm" || route === "task" || route === "epic" || route === "resume" || route === "task_link";
}

/**
 * Whether an epic's tasks, each naming others of the same answer by ref in
 * blockedBy, are acceptable: at most CHAT_EPIC_TASKS_MAX, refs unique, no
 * dangling ref, no cycle. The orchestrator applies the same rule; this is
 * its statement for the web and tests.
 */
export function epicTasksProblem(tasks: ReadonlyArray<{ ref: string; blockedBy: ReadonlyArray<string> }>): string | null {
  if (tasks.length === 0) return "an epic needs at least one task";
  if (tasks.length > CHAT_EPIC_TASKS_MAX) return `at most ${CHAT_EPIC_TASKS_MAX} tasks`;
  const refs = new Set<string>();
  for (const t of tasks) {
    if (!t.ref || refs.has(t.ref)) return `task ref ${JSON.stringify(t.ref)} is missing or repeated`;
    refs.add(t.ref);
  }
  for (const t of tasks) for (const b of t.blockedBy) if (!refs.has(b)) return `task ${t.ref} is blocked by unknown ${b}`;
  const edges = new Map(tasks.map((t) => [t.ref, t.blockedBy]));
  const state = new Map<string, 1 | 2>();
  const visit = (ref: string): boolean => {
    if (state.get(ref) === 2) return false;
    if (state.get(ref) === 1) return true;
    state.set(ref, 1);
    for (const b of edges.get(ref) ?? []) if (visit(b)) return true;
    state.set(ref, 2);
    return false;
  };
  for (const t of tasks) if (visit(t.ref)) return "the tasks' blocked-by form a cycle";
  return null;
}

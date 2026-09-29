/*
 * The mockup's world: Acme, the gallery's projects and tasks (WI-24xx), and
 * what dude remembers about them. Deterministic, so screenshots compare.
 */

import type { AgentRole, TaskStatus } from "@dude/domain";
import type { Person } from "../../../../packages/design-system/src/components/PersonAvatar.tsx";

export const ORG = "Acme";
export const P: Record<string, Person> = {
  marcio: { id: "u_marcio", name: "Márcio Martins", online: true },
  ana: { id: "u_ana", name: "Ana Ribeiro", online: true },
  tom: { id: "u_tom", name: "Tom Okafor" },
  kai: { id: "u_kai", name: "Kai Nakamura" },
};

export type MemoryKind = "fact" | "procedure" | "note";
export type DocType = "memory" | "task" | "epic" | "project";

/** Who wrote it: a person, dude itself (an automation), or an agent working for a person on a task. */
export type Author =
  | { readonly kind: "person"; readonly person: Person }
  | { readonly kind: "system"; readonly what: string }
  | { readonly kind: "agent"; readonly role: AgentRole; readonly for: Person; readonly task: string };

export interface Ref {
  readonly type: "task" | "epic" | "project";
  readonly label: string;
  readonly status?: TaskStatus;
}

export interface Memory {
  readonly id: string;
  readonly title: string;
  readonly content: string;
  readonly kind: MemoryKind;
  /** null: the whole organisation. */
  readonly project: string | null;
  readonly author: Author;
  /** Where it was learned. */
  readonly from?: Ref;
  /** What it is about. */
  readonly about: readonly Ref[];
  readonly added: string;
  readonly archived?: boolean;
  readonly embedded: "yes" | "pending" | "failed";
}

export const MEMORIES: readonly Memory[] = [
  {
    id: "mem_01JB8Q2W4K",
    title: "GitHub retries a delivery for up to 3 days; dedupe on X-GitHub-Delivery",
    content:
      "GitHub re-sends a webhook delivery it thinks failed for up to **3 days**, with the same `X-GitHub-Delivery` id.\n\nDedupe on that header in `webhook_deliveries` (unique index), not on the payload hash: two genuine pushes can have identical payloads.",
    kind: "fact",
    project: "control-plane",
    author: { kind: "agent", role: "implementer", for: P["marcio"]!, task: "WI-2402" },
    from: { type: "task", label: "WI-2402", status: "running" },
    about: [
      { type: "epic", label: "Webhook reliability" },
      { type: "task", label: "WI-2402", status: "running" },
      { type: "task", label: "WI-2401", status: "awaiting_input" },
    ],
    added: "2h",
    embedded: "yes",
  },
  {
    id: "mem_01JB7ZK1RD",
    title: "Run the control-plane tests against a throwaway database",
    content:
      "1. `docker run -d -p 5434:5432 postgres:17`\n2. `DATABASE_URL=postgres://postgres@localhost:5434/dude bun run migrate`\n3. `bun test apps/control-plane`\n\nNever with a dev backend up on the same database: the sweeper races the tests.",
    kind: "procedure",
    project: "control-plane",
    author: { kind: "person", person: P["ana"]! },
    about: [{ type: "project", label: "control-plane" }],
    added: "1d",
    embedded: "yes",
  },
  {
    id: "mem_01JB6T0M9C",
    title: "4xx from a webhook consumer is never retried",
    content: "Márcio answered on WI-2401: a 4xx means the request was wrong, and retrying it cannot help. Only 5xx and timeouts are retried.",
    kind: "fact",
    project: "control-plane",
    author: { kind: "system", what: "from an answer" },
    from: { type: "task", label: "WI-2401", status: "awaiting_input" },
    about: [{ type: "task", label: "WI-2401", status: "awaiting_input" }],
    added: "3h",
    embedded: "pending",
  },
  {
    id: "mem_01JB5D8H2N",
    title: "Commit messages are prose; no conventional-commit prefixes",
    content: "Every repository at Acme: a sentence that says what changed and why. No `feat:` / `fix:`.",
    kind: "fact",
    project: null,
    author: { kind: "person", person: P["marcio"]! },
    about: [],
    added: "6d",
    embedded: "yes",
  },
  {
    id: "mem_01JB4R7Y6P",
    title: "The transcript page's LCP is dominated by the font load",
    content: "Measured on WI-2431: 1.1s of the 2.3s LCP is the webfont. Bundled fonts fixed it.",
    kind: "note",
    project: "web",
    author: { kind: "agent", role: "investigator", for: P["kai"]!, task: "WI-2431" },
    from: { type: "task", label: "WI-2431", status: "done" },
    about: [{ type: "epic", label: "Navigation" }],
    added: "4d",
    embedded: "failed",
  },
  {
    id: "mem_01JB3A1C0E",
    title: "OpenCode resumes a session only with the same --session id",
    content: "A resumed run must pass the stored session id; a new one starts from an empty context.",
    kind: "fact",
    project: "runner",
    author: { kind: "agent", role: "reviewer", for: P["tom"]!, task: "WI-2440" },
    from: { type: "task", label: "WI-2440", status: "failed" },
    about: [{ type: "epic", label: "Harness adapters" }],
    added: "9d",
    embedded: "yes",
  },
  {
    id: "mem_01JB2B9Z3F",
    title: "Staging webhooks go through smee.io",
    content: "Replaced by the tunnel in September.",
    kind: "note",
    project: "control-plane",
    author: { kind: "person", person: P["tom"]! },
    about: [],
    added: "21d",
    archived: true,
    embedded: "yes",
  },
];

export interface Result {
  readonly type: DocType;
  readonly title: string;
  readonly key?: string;
  readonly status?: TaskStatus;
  readonly project: string | null;
  /** Matched words are wrapped in ⟦ ⟧. */
  readonly snippet: string;
  /** 1-based rank in each list; null when it was not in that list. */
  readonly text: { readonly rank: number; readonly score: number } | null;
  readonly vector: { readonly rank: number; readonly distance: number } | null;
  readonly fused: number;
  readonly embedded: "yes" | "pending";
  readonly memory?: Memory;
}

const m = (id: string) => MEMORIES.find((x) => x.id === id)!;

export const QUERY = "webhook retries duplicate deliveries";

export const RESULTS: readonly Result[] = [
  {
    type: "memory",
    title: m("mem_01JB8Q2W4K").title,
    project: "control-plane",
    snippet: "GitHub re-sends a ⟦webhook⟧ ⟦delivery⟧ it thinks failed for up to 3 days … Dedupe on that header in webhook_deliveries",
    text: { rank: 1, score: 0.612 },
    vector: { rank: 1, distance: 0.182 },
    fused: 0.0328,
    embedded: "yes",
    memory: m("mem_01JB8Q2W4K"),
  },
  {
    type: "task",
    key: "WI-2402",
    status: "running",
    title: "Dedupe deliveries by X-GitHub-Delivery across restarts",
    project: "control-plane",
    snippet: "Goal: a redelivered ⟦webhook⟧ is processed once, even after the control plane restarts between the two ⟦deliveries⟧",
    text: { rank: 2, score: 0.544 },
    vector: { rank: 2, distance: 0.201 },
    fused: 0.0323,
    embedded: "yes",
  },
  {
    type: "task",
    key: "WI-2401",
    status: "awaiting_input",
    title: "Add retry with backoff to the GitHub webhook handler",
    project: "control-plane",
    snippet: "Goal: transient failures in the ⟦webhook⟧ handler are ⟦retried⟧ with exponential backoff, at most 5 attempts",
    text: { rank: 3, score: 0.498 },
    vector: { rank: 4, distance: 0.244 },
    fused: 0.0315,
    embedded: "yes",
  },
  {
    type: "epic",
    title: "Webhook reliability",
    project: "control-plane",
    snippet: "Every GitHub ⟦webhook⟧ is verified, deduplicated, ⟦retried⟧ on our side, and replayable when we miss one",
    text: { rank: 4, score: 0.431 },
    vector: { rank: 3, distance: 0.229 },
    fused: 0.0315,
    embedded: "yes",
  },
  {
    type: "memory",
    title: m("mem_01JB6T0M9C").title,
    project: "control-plane",
    snippet: "a 4xx means the request was wrong, and ⟦retrying⟧ it cannot help. Only 5xx and timeouts are ⟦retried⟧",
    text: { rank: 5, score: 0.377 },
    vector: null,
    fused: 0.0154,
    embedded: "pending",
    memory: m("mem_01JB6T0M9C"),
  },
  {
    type: "task",
    key: "WI-2404",
    status: "review",
    title: "Replay endpoint for missed deliveries",
    project: "control-plane",
    snippet: "An admin can replay a GitHub delivery we never received, by id, from the ⟦deliveries⟧ page",
    text: null,
    vector: { rank: 5, distance: 0.268 },
    fused: 0.0154,
    embedded: "yes",
  },
  {
    type: "project",
    title: "control-plane",
    project: "control-plane",
    snippet: "The API, auth, SSE and GitHub ⟦webhooks⟧ for dude",
    text: null,
    vector: { rank: 6, distance: 0.301 },
    fused: 0.0152,
    embedded: "yes",
  },
];

/** What `search_memory` hands the agent: the same ranking, as text it can read. */
export const AGENT_VIEW = `1. memory · control-plane · mem_01JB8Q2W4K
   GitHub retries a delivery for up to 3 days; dedupe on X-GitHub-Delivery
   GitHub re-sends a webhook delivery it thinks failed for up to 3 days, with the same
   X-GitHub-Delivery id. Dedupe on that header in webhook_deliveries (unique index)…
2. task WI-2402 · running · Dedupe deliveries by X-GitHub-Delivery across restarts
   Goal: a redelivered webhook is processed once, even after the control plane restarts…
3. task WI-2401 · awaiting input · Add retry with backoff to the GitHub webhook handler
   Goal: transient failures in the webhook handler are retried with exponential backoff…
4. epic · Webhook reliability
   Every GitHub webhook is verified, deduplicated, retried on our side, and replayable…
5. memory · control-plane · mem_01JB6T0M9C
   4xx from a webhook consumer is never retried
   Márcio answered on WI-2401: a 4xx means the request was wrong…

Use get_memory / list_tasks for the full text.`;

export const INDEX = {
  provider: "llm-proxy",
  endpoint: "https://llm.absmartly.dev/v1/embeddings",
  model: "gemini-embedding-001",
  dimensions: 768,
  kinds: [
    { type: "memory" as const, label: "Memories", total: 128, embedded: 126, pending: 1, failed: 1 },
    { type: "task" as const, label: "Tasks", total: 1_020, embedded: 1_017, pending: 2, failed: 1 },
    { type: "epic" as const, label: "Epics", total: 64, embedded: 64, pending: 0, failed: 0 },
    { type: "project" as const, label: "Projects", total: 8, embedded: 8, pending: 0, failed: 0 },
  ],
  failures: [
    { type: "memory" as const, label: "The transcript page's LCP is dominated by the font load", key: "mem_01JB4R7Y6P", error: "429 from llm-proxy: pool gemini is over its share", attempts: 5, last: "3m" },
    { type: "task" as const, label: "Type the event payloads end to end", key: "WI-2419", error: "timed out after 30s (llm-proxy)", attempts: 1, last: "1h" },
  ],
};

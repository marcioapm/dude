/*
 * The mockup's world: Acme's project "abs", the epic "Metric preview
 * in the metric form", and its four tasks as they would have gone on
 * 2026-10-12 with peers, messages and epic notes. Times are that day's
 * local clock so the transcripts read 13:43, 14:05…; ages on the board are
 * measured from now so they do not drift between screenshots.
 */

import type { AgentRole, RunStatus, TaskStatus } from "@dude/domain";
import type { Person } from "../../../../packages/design-system/src/components/PersonAvatar.tsx";
import type { NavProject, NavSession, NavTask } from "../../../../packages/design-system/src/util/navModel.ts";

export const PROJECT = "abs";
export const EPIC = "Metric preview in the metric form";
export const EPIC_ID = "ep_metric_preview";

export const P: Record<string, Person> = {
  marcio: { id: "u_marcio", name: "Márcio Martins", online: true },
  jonas: { id: "u_jonas", name: "Jonas Alves", online: true },
};

/** 2026-10-12 at hh:mm(:ss), local time. */
export function at(hm: string): Date {
  const [h, m, s] = hm.split(":").map(Number);
  return new Date(2026, 9, 12, h ?? 0, m ?? 0, s ?? 0);
}

export type TaskKey = "ABS-1" | "ABS-2" | "ABS-3" | "ABS-4";

/** A peer as `peers` returns it, and as the Coordination view's task list shows it. */
export interface Peer {
  readonly key: TaskKey;
  readonly id: string;
  readonly title: string;
  readonly status: TaskStatus;
  /** The live Run's phase and role, or the last one's. */
  readonly phase: string;
  readonly role: AgentRole;
  readonly run: RunStatus;
  readonly live: boolean;
  readonly branch: string;
  readonly head: string;
  readonly files: ReadonlyArray<{ readonly path: string; readonly add: number; readonly del: number }>;
  readonly lastSaid: string;
  readonly owner: Person;
  /** Waiting on another task's reply. */
  readonly waitingOn?: TaskKey | undefined;
}

export const PEERS: Record<TaskKey, Peer> = {
  "ABS-1": {
    key: "ABS-1",
    id: "wi_0mv5au1",
    title: "Engine: estimate histogram endpoint",
    status: "running",
    phase: "implement",
    role: "implementer",
    run: "running",
    live: true,
    branch: "dude/wi_0mv5au1/attempt-1",
    head: "3f9a2c1",
    files: [
      { path: "engine/src/query/estimate/EstimateMetricsHistogramQueryHandler.java", add: 214, del: 0 },
      { path: "engine/src/query/estimate/HistogramBins.java", add: 96, del: 0 },
      { path: "engine/src/http/Routes.java", add: 6, del: 1 },
    ],
    lastSaid: "Added EstimateMetricsHistogramQueryHandler at POST /v1/query/estimate/histogram/metrics. Bins are fixed-width over [min, max] of the sample; running the engine tests next.",
    owner: P["marcio"]!,
  },
  "ABS-2": {
    key: "ABS-2",
    id: "wi_0mv5auwan3e9310013429faaa",
    title: "Backend: metric preview endpoints",
    status: "running",
    phase: "implement",
    role: "implementer",
    run: "running",
    live: true,
    branch: "dude/wi_0mv5auwan3e9310013429faaa/attempt-1",
    head: "b71e04d",
    files: [
      { path: "backend/src/routes/metrics/preview.ts", add: 188, del: 0 },
      { path: "backend/src/engine/client.ts", add: 24, del: 3 },
      { path: "backend/src/routes/metrics/preview.test.ts", add: 141, del: 0 },
    ],
    lastSaid: "Pinned the preview contract as an epic note. Routes read `body.draft`; the engine path is ABS-1's, so I'm not adding a handler here.",
    owner: P["jonas"]!,
  },
  "ABS-3": {
    key: "ABS-3",
    id: "wi_0mv5au3",
    title: "Custom SQL on the estimate path",
    status: "review",
    phase: "review",
    role: "reviewer",
    run: "running",
    live: true,
    branch: "dude/wi_0mv5au3/attempt-1",
    head: "e20c9b7",
    files: [
      { path: "engine/src/query/estimate/CustomSqlEstimate.java", add: 132, del: 18 },
      { path: "engine/src/query/estimate/EstimateQuery.java", add: 12, del: 4 },
    ],
    lastSaid: "Implementation done; custom SQL metrics go through the same sampling as built-in ones. In review.",
    owner: P["marcio"]!,
  },
  "ABS-4": {
    key: "ABS-4",
    id: "wi_0mv5au4",
    title: "Frontend: preview panel in the metric form",
    status: "running",
    phase: "implement",
    role: "implementer",
    run: "running",
    live: true,
    branch: "dude/wi_0mv5au4/attempt-1",
    head: "—",
    files: [],
    lastSaid: "Reading the metric form. ABS-2 owns the preview routes; asking it for the body and response before writing the client.",
    owner: P["marcio"]!,
    waitingOn: "ABS-2",
  },
};

export const KEYS: readonly TaskKey[] = ["ABS-1", "ABS-2", "ABS-3", "ABS-4"];

// --------------------------------------------------------------------------- messages and notes

export type Delivery =
  /** Sent, not read yet: lands at the recipient's next step. */
  | { readonly kind: "queued" }
  /** Read by the recipient's agent, after what it finished. */
  | { readonly kind: "read"; readonly at: Date; readonly after?: string | undefined }
  /** The recipient has no live Run: it is in that task's next Run briefing. */
  | { readonly kind: "inbox" };

export interface PeerMsg {
  readonly id: string;
  readonly from: TaskKey;
  readonly fromRole: AgentRole;
  /** A task, or the whole epic. */
  readonly to: TaskKey | "epic";
  readonly text: string;
  readonly sentAt: Date;
  readonly delivery: Delivery;
  readonly replyTo?: string | undefined;
  /** The sender's Run waits for a reply (`wait`). */
  readonly waits?: boolean | undefined;
}

export interface Thread {
  readonly id: string;
  readonly a: TaskKey;
  readonly b: TaskKey;
  readonly subject: string;
  readonly messages: readonly PeerMsg[];
}

const CONTRACT_Q =
  "What body and response will `/v1/metrics/preview/*` use? I'll code to it.";
const CONTRACT_A =
  "Both are `POST`, `history` and `histogram` under `/v1/metrics/preview/`. Body: `{draft, unit_type_id, applications, from, sample, period, cumulative, bin_count, include_non_achievers}` — `draft` is the metric form's value as it stands, not a saved metric. Response columns `metric_<id>_mean`, `_var`, `_min`, `_bins`, plus `sample_fraction`. Pinned it as an epic note.";
const PATH_Q = "Which path does your estimate histogram use? I call it from `/v1/metrics/preview/histogram`.";
const PATH_A =
  "`POST /v1/query/estimate/histogram/metrics`, handler `EstimateMetricsHistogramQueryHandler`, on `dude/wi_0mv5au1/attempt-1` at 3f9a2c1. Same request as `/estimate/metrics` plus `bin_count`.";

export const THREADS: readonly Thread[] = [
  {
    id: "th_contract",
    a: "ABS-4",
    b: "ABS-2",
    subject: "Preview API body and response",
    messages: [
      { id: "pm_1", from: "ABS-4", fromRole: "implementer", to: "ABS-2", text: CONTRACT_Q, sentAt: at("13:43:20"), delivery: { kind: "read", at: at("13:43:52"), after: "Bash" }, waits: true },
      { id: "pm_4", from: "ABS-2", fromRole: "implementer", to: "ABS-4", text: CONTRACT_A, sentAt: at("13:49:05"), delivery: { kind: "read", at: at("13:49:06") }, replyTo: "pm_1" },
    ],
  },
  {
    id: "th_path",
    a: "ABS-2",
    b: "ABS-1",
    subject: "Engine path for the estimate histogram",
    messages: [
      { id: "pm_2", from: "ABS-2", fromRole: "implementer", to: "ABS-1", text: PATH_Q, sentAt: at("13:45:10"), delivery: { kind: "read", at: at("13:45:31"), after: "Edit" } },
      { id: "pm_3", from: "ABS-1", fromRole: "implementer", to: "ABS-2", text: PATH_A, sentAt: at("13:46:02"), delivery: { kind: "read", at: at("13:46:40"), after: "Read" }, replyTo: "pm_2" },
    ],
  },
];

export const MSG = Object.fromEntries(THREADS.flatMap((t) => t.messages).map((m) => [m.id, m])) as Record<string, PeerMsg>;

export type NoteAuthor =
  | { readonly kind: "agent"; readonly task: TaskKey; readonly role: AgentRole; readonly for: Person }
  | { readonly kind: "person"; readonly person: Person };

export interface Note {
  readonly id: string;
  readonly text: string;
  readonly by: NoteAuthor;
  readonly at: Date;
  /** How many of the epic's live agents have read it. */
  readonly readBy: number;
}

export const NOTES: readonly Note[] = [
  {
    id: "en_2",
    text:
      "**Preview API (ABS-2).** `POST /v1/metrics/preview/history` and `/histogram`. Body `{draft, unit_type_id, applications, from, sample, period, cumulative, bin_count, include_non_achievers}`. Columns `metric_<id>_mean`, `_var`, `_min`, `_bins`, and `sample_fraction`. Engine call: `POST /v1/query/estimate/histogram/metrics` (ABS-1).",
    by: { kind: "agent", task: "ABS-2", role: "implementer", for: P["jonas"]! },
    at: at("13:48:40"),
    readBy: 3,
  },
  {
    id: "en_1",
    text: "Read `.ai/personal/marcio/docs/llm-agent/INDEX.md` for how to run tests here.",
    by: { kind: "person", person: P["marcio"]! },
    at: at("13:41:00"),
    readBy: 4,
  },
];

// --------------------------------------------------------------------------- the tree and the board

const NOW = Date.now();
const MIN = 60_000;

function ses(id: string, role: AgentRole, status: "running" | "completed", activity?: string): NavSession {
  return { id, role, status, activity };
}

function task(p: Peer, sessions: NavSession[], since: number, cost: number): NavTask {
  return {
    id: p.id,
    key: p.key,
    title: p.title,
    status: p.status,
    people: [p.owner],
    statusSince: NOW - since * MIN,
    costUsd: cost,
    runs: [{ id: `run_${p.key}`, attempt: 1, status: "running", sessions }],
  };
}

/** The tree as the sidebar and the board take it. `waiting` puts "Waiting on ABS-2" on ABS-4's live session (screen 6). */
export function navAbs(waiting: boolean): NavProject {
  return {
    id: "p_abs",
    name: PROJECT,
    epics: [
      {
        id: EPIC_ID,
        title: EPIC,
        tasks: [
          task(PEERS["ABS-1"], [ses("s1", "implementer", "running", "Running engine tests")], 34, 1.84),
          task(PEERS["ABS-2"], [ses("s2c", "conductor", "completed"), ses("s2", "implementer", "running", "Writing preview.test.ts")], 34, 2.31),
          task(PEERS["ABS-3"], [ses("s3i", "implementer", "completed"), ses("s3", "reviewer", "running", "Reviewing correctness")], 12, 3.02),
          task(PEERS["ABS-4"], [ses("s4", "implementer", "running", waiting ? "Waiting on ABS-2" : "Writing previewClient.ts")], 34, 0.92),
        ],
      },
      {
        id: "ep_exports",
        title: "Experiment exports",
        tasks: [
          { id: "wi_abs_9", key: "ABS-9", title: "CSV export of segment results", status: "done", statusSince: NOW - 3 * 24 * 60 * MIN, costUsd: 4.1, people: [P["jonas"]!] },
        ],
      },
    ],
  };
}

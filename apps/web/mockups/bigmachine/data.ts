/*
 * The bigmachine mockup's world: Acme's machine sizes, the billing
 * project, BILL-42 and its implementer that ran out of memory, a brainstorm
 * session, and the scenarios the bar switches between. The evidence is
 * from production: a jest run with --maxWorkers 4 OOM-killed on Standard,
 * testers peaking at 10.3 GiB (p90 9.0) on 8 GiB.
 */

import type { Person } from "@dude/design-system";

export const ORG = "Acme";
export const PROJECT = "billing";

export const P: Record<string, Person> = {
  marcio: { id: "u_marcio", name: "Márcio Martins" },
  ana: { id: "u_ana", name: "Ana Ribeiro" },
};

/** One of Acme's sizes, as `machine_sizes` holds it, with lux's pool name. */
export interface Size {
  readonly id: string;
  readonly name: string;
  readonly cpus: number;
  readonly memoryGiB: number;
  readonly diskGiB: number;
  /** lux's pool id; null is the organisation's default pool. */
  readonly poolId: string | null;
  readonly pool: string;
  readonly isDefault?: boolean;
  /** What an hour of it costs, when dude knows (a lux cost plugin's rate). */
  readonly usdPerHour: number | null;
}

export const SIZES: readonly Size[] = [
  { id: "ms_tiny", name: "Tiny", cpus: 0.5, memoryGiB: 1, diskGiB: 10, poolId: null, pool: "default", usdPerHour: 0.02 },
  { id: "ms_small", name: "Small", cpus: 1, memoryGiB: 4, diskGiB: 20, poolId: null, pool: "default", usdPerHour: 0.06 },
  { id: "ms_standard", name: "Standard", cpus: 2, memoryGiB: 8, diskGiB: 20, poolId: null, pool: "default", isDefault: true, usdPerHour: 0.12 },
  { id: "ms_large", name: "Large", cpus: 4, memoryGiB: 16, diskGiB: 40, poolId: null, pool: "default", usdPerHour: 0.24 },
  { id: "ms_xl", name: "XL", cpus: 8, memoryGiB: 32, diskGiB: 80, poolId: "pool_highmem", pool: "highmem", usdPerHour: 0.52 },
];

export const size = (name: string): Size => SIZES.find((s) => s.name === name)!;

/** "2 CPUs · 8 GiB · 20 GiB", MachineChip's spec. */
export const specOf = (s: Size) => `${s.cpus} ${s.cpus === 1 ? "CPU" : "CPUs"} · ${s.memoryGiB} GiB · ${s.diskGiB} GiB`;

export const TASK = {
  key: "BILL-42",
  title: "Faster invoice export",
  run: "run_7f3k",
  branch: "dude/BILL-42/attempt-1",
  commit: "a41c9e2",
};

export const REASON_LARGE =
  "The backend jest suite was OOM-killed (exit 137) with --maxWorkers 4: four workers rendering PDFs peaked at 7.9 of 8 GiB. " +
  "With --maxWorkers 2 it passes but takes 14 minutes, and every fix round reruns it. Large (16 GiB) fits four workers with room.";

export const REASON_XL =
  "Loading the full 2025 invoice fixture (2.1 million rows) into the export's in-memory sort needs about 20 GiB; " +
  "it was OOM-killed at 7.9 of 8 GiB. Large would not fit it either. I pushed what passes at a41c9e2; the sort in stream.ts is uncommitted because it does not pass yet.";

export const DECLINE_NOTE = "Run the suite with --maxWorkers 2 and only the export tests; CI runs the whole thing.";

export const SESSION = {
  title: "Invoice archive: what it would cost to keep 7 years",
  reason:
    "To size the archive I am loading last year's invoices into DuckDB here; the 6 GiB parquet export needs more than Small's 4 GiB. " +
    "Standard would fit it with a 2 GiB margin.",
};

/**
 * The stages a resize goes through, in the startup design's words
 * (github/design/agent-startup, STAGE_WORDS), led by the park that a
 * resize in place is: lux stops the container, keeps its state, and
 * resumes it with the new resources.
 */
export const MOVE_STAGES = [
  { id: "park", now: "Stopping it, keeping its work", done: "Stopped, work kept" },
  { id: "waiting", now: "Waiting for a machine", done: "Machine found" },
  { id: "volumes", now: "Restoring its workspace", done: "Workspace restored" },
  { id: "container", now: "Starting the container", done: "Container started" },
  { id: "running", now: "Starting the agent", done: "Agent started" },
] as const;

export type Scenario =
  | "ask" | "ask-fresh" | "resizing" | "auto" | "declined" | "run" | "task" | "settings-org" | "settings-project" | "session";

export const SCENARIOS: ReadonlyArray<{ readonly value: Scenario; readonly label: string; readonly note: string }> = [
  { value: "ask", label: "1 · Asks", note: "BILL-42's implementer ran out of memory on Standard and asks for Large. Acme asks a person: Márcio, the task's owner, decides in Chat." },
  { value: "ask-fresh", label: "2 · Other pool", note: "It asks for XL, which is in another lux pool: a Run can't change pools, so approving starts a fresh implementer. The card says what is lost and what it pushed first." },
  { value: "resizing", label: "3 · Moving", note: "Márcio approved Large. The implementer is parked and resumed on Large, with its conversation and uncommitted work: the startup design's row, as “Moving to Large”." },
  { value: "auto", label: "4 · Automatic", note: "billing lets agents go up to Large without asking: the request is a notice, and the move follows. Nothing waits on anyone." },
  { value: "declined", label: "5 · Declined", note: "Márcio declined with a note. The agent is told both, and carries on on Standard." },
  { value: "run", label: "6 · Run header", note: "The fixer's Run page later in the task: its machine chip says Large, raised for this task, and its tooltip who asked and who approved." },
  { value: "task", label: "7 · Task", note: "The task's Overview: the machines raised in this task, beside the pull request, with Reset." },
  { value: "settings-org", label: "8 · Acme", note: "Acme → Machines: whether agents may ask for a larger machine, and who decides." },
  { value: "settings-project", label: "8 · billing", note: "billing → Machines: the project goes up to Large without asking, overriding Acme's “Ask a person”." },
  { value: "session", label: "9 · Session", note: "A brainstorm session on Small asks for Standard. Ana owns the session; Márcio reads it, so the card says it waits on Ana." },
];

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
}

export const SIZES: readonly Size[] = [
  { id: "ms_tiny", name: "Tiny", cpus: 0.5, memoryGiB: 1, diskGiB: 10, poolId: null, pool: "default" },
  { id: "ms_small", name: "Small", cpus: 1, memoryGiB: 4, diskGiB: 20, poolId: null, pool: "default" },
  { id: "ms_standard", name: "Standard", cpus: 2, memoryGiB: 8, diskGiB: 20, poolId: null, pool: "default", isDefault: true },
  { id: "ms_large", name: "Large", cpus: 4, memoryGiB: 16, diskGiB: 40, poolId: null, pool: "default" },
  { id: "ms_xl", name: "XL", cpus: 8, memoryGiB: 32, diskGiB: 80, poolId: "pool_highmem", pool: "highmem" },
];

export const size = (name: string): Size => SIZES.find((s) => s.name === name)!;

/** "2 CPUs · 8 GiB · 20 GiB", MachineChip's spec. */
export const specOf = (s: Size) => `${s.cpus} ${s.cpus === 1 ? "CPU" : "CPUs"} · ${s.memoryGiB} GiB · ${s.diskGiB} GiB`;

export const TASK = {
  key: "BILL-42",
  title: "Faster invoice export",
  run: "run_7f3k",
  fresh: "run_q8w2",
  branch: "dude/BILL-42/attempt-1",
  runBranch: "dude/BILL-42/run-run_7f3k",
  commit: "a41c9e2",
};

export const REASON_LARGE =
  "The backend jest suite was OOM-killed (exit 137) with --maxWorkers 4: four workers rendering PDFs ran out of Standard's 8 GiB. " +
  "With --maxWorkers 2 it passes but takes 14 minutes, and every fix round reruns it. Large (16 GiB) fits four workers.";

export const REASON_XL =
  "Loading the full 2025 invoice fixture (2.1 million rows) into the export's in-memory sort needs about 20 GiB; " +
  "it was OOM-killed on Standard (8 GiB), and Large (16 GiB) would not fit it either. What passes is pushed at a41c9e2.";

/** The tool's answers, as the agent reads them. */
export const ANSWER_LARGE =
  "Moved: you run on Large (4 CPUs · 16 GiB · 40 GiB) from your next step; your conversation and checkout are kept. " +
  "Later implementer and fixer Runs in this task run on Large too. End your turn now; you are resumed on Large.";

export const ANSWER_XL =
  "XL is in another pool: you will be replaced by a fresh implementer starting from what you pushed (a41c9e2), " +
  "with your reason as its note. Nothing unpushed was found. End your turn now.";

export const REFUSED_UNPUSHED =
  "XL is in another pool: you would be replaced by a fresh implementer starting from what you pushed; commit and push first. " +
  "Not pushed: 1 commit (3f9e0b1 “Sort the export in chunks”), and uncommitted src/export/stream.ts (M +42 −7), src/export/sort.test.ts (A +88). " +
  "Push with `dude push`, then ask again; or ask with --fresh to leave them behind.";

export const REFUSED_LARGEST =
  "You run on XL (8 CPUs · 32 GiB · 80 GiB), Acme's largest size: there is none larger to ask for. " +
  "Use fewer workers or less data, and say in your work what did not fit.";

export const SESSION = {
  title: "Invoice archive: what it would cost to keep 7 years",
  reason:
    "Joining last year's invoices with their lines in DuckDB needs about 18 GiB (the 6 GiB parquet export expands 3×); " +
    "it ran out of Small's 4 GiB, and Standard and Large would not fit it either.",
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

/** A fresh Run's start, the startup design's stages unchanged. */
export const START_STAGES = [
  { id: "waiting", now: "Waiting for a machine", done: "Machine found" },
  { id: "image", now: "Getting the image ready", done: "Image ready" },
  { id: "volumes", now: "Restoring its workspace", done: "Workspace restored" },
  { id: "repositories", now: "Checking out repositories", done: "Repositories checked out" },
  { id: "container", now: "Starting the container", done: "Container started" },
  { id: "running", now: "Starting the agent", done: "Agent started" },
] as const;

export type Scenario =
  | "move" | "move-pool" | "refused" | "refused-largest" | "run" | "task" | "reset" | "reset-pool"
  | "role-org" | "role-project" | "session" | "start-over";

export const SCENARIOS: ReadonlyArray<{ readonly value: Scenario; readonly label: string; readonly note: string }> = [
  { value: "move", label: "1 · Moves, same pool", note: "BILL-42's implementer ran out of memory on Standard and asked for Large. billing lets it, so it moves at once, in place: it keeps its conversation and checkout." },
  { value: "move-pool", label: "2 · Moves, another pool", note: "It needs XL, which is in another lux pool. It pushed first, then asked: a fresh implementer starts on XL from a41c9e2, with its reason as its note. The old Run is Replaced." },
  { value: "refused", label: "3 · Refused: unpushed", note: "In the implementer's own Run: it asks for XL across pools with a commit and two files not pushed. The tool refuses and names them; the agent pushes and asks again." },
  { value: "refused-largest", label: "3 · Refused: largest", note: "The implementer on XL asks again. XL is Acme's largest: the tool says so. (With the role's switch off there is no tool to refuse: it isn't offered.)" },
  { value: "run", label: "4 · Run header", note: "The fixer's Run later in the task: its machine chip says Large, raised for this task. The tooltip says who asked and why. Nobody approved: the agent decided." },
  { value: "task", label: "5 · Task", note: "The task's Overview: the machines raised in this task, beside the pull request. Anyone in the project may Reset." },
  { value: "reset", label: "5 · Reset", note: "Reset moves the running implementer back down now. Same pool: resized in place, keeping its work." },
  { value: "reset-pool", label: "5 · Reset, other pool", note: "The implementer runs on XL in highmem: Reset replaces it with a fresh implementer on Standard, from what it pushed." },
  { value: "role-org", label: "6 · Implementer, Acme", note: "Acme → Agents → Implementer: “May ask for a larger machine”, on, next to Machine." },
  { value: "role-project", label: "6 · Simplifier, billing", note: "billing → Agents → Simplifier: the switch overridden off, with its source and Reset." },
  { value: "session", label: "7 · Session", note: "A brainstorm on Small needs XL, in another pool: a fresh brainstorm takes over with the conversation. Its scratch files are not kept." },
  { value: "start-over", label: "8 · After Start over", note: "Attempt 2's first implementer Run: Start over keeps the task, so it starts on Large, raised for this task." },
];

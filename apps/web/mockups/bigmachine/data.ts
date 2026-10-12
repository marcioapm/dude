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
  branch: "dude/BILL-42/attempt-1",
};

export const REASON_LARGE =
  "The backend jest suite was OOM-killed (exit 137) with --maxWorkers 4: four workers rendering PDFs ran out of Standard's 8 GiB. " +
  "With --maxWorkers 2 it passes but takes 14 minutes, and every fix round reruns it. Large (16 GiB) fits four workers.";

export const REASON_XL =
  "Loading the full 2025 invoice fixture (2.1 million rows) into the export's in-memory sort needs about 20 GiB; " +
  "it was OOM-killed on Standard (8 GiB), and Large (16 GiB) would not fit it either.";

export const REASON_CONDUCTOR =
  "Márcio asked me to run the backend suite before I hand over. It needs 4 jest workers rendering PDFs; " +
  "my Standard (8 GiB) is the size the implementer was OOM-killed on with --maxWorkers 4.";

/** What every move keeps and loses, the same for a same-pool and a cross-pool move. */
const KEEPS =
  "Your conversation and your checkout are kept, committed or not, with everything else on /workspace and your home. " +
  "Running processes and /tmp are not: start your servers and watchers again.";

/** The tool's answers, as the agent reads them. */
export const ANSWER_LARGE =
  `Moved: you run on Large (4 CPUs · 16 GiB · 40 GiB) from your next step. ${KEEPS} ` +
  "Later implementer and fixer Runs in this task run on Large too. End your turn now; you are resumed on Large.";

export const ANSWER_XL =
  `Moved: you run on XL (8 CPUs · 32 GiB · 80 GiB) in the highmem pool from your next step. ${KEEPS} ` +
  "Later implementer and fixer Runs in this task run on XL too. End your turn now; you are resumed on XL.";

export const ANSWER_CONDUCTOR =
  `Moved: you run on Large (4 CPUs · 16 GiB · 40 GiB) from your next step. ${KEEPS} ` +
  "Later conductors in this task run on Large too. End your turn now; you are resumed on Large.";

export const ANSWER_SESSION =
  `Moved: you run on XL (8 CPUs · 32 GiB · 80 GiB) in the highmem pool from your next step. ${KEEPS} ` +
  "End your turn now; you are resumed on XL.";

export const REASON_TESTER =
  "Playwright with 4 workers against the export page crashed Chromium twice (\"Target crashed\", the renderer OOM-killed) while the 2.1M-row preview rendered; " +
  "each worker's browser peaks at about 5 GiB, so 4 need about 20 GiB, more than Standard's 8 or Large's 16.";

export const REASON_TESTER_LARGE =
  "Same crash as above; XL is refused for this image. Large (16 GiB) fits 3 browser workers at about 5 GiB each.";

export const ANSWER_TESTER_LARGE =
  `Moved: you run on Large (4 CPUs · 16 GiB · 40 GiB) from your next step. ${KEEPS} ` +
  "Later tester Runs in this task run on Large too. End your turn now; you are resumed on Large.";

/** lux's 422 platform_mismatch, passed through in plain words. */
export const REFUSED_PLATFORM =
  "XL is in highmem, whose machines are linux/arm64; this agent's image is linux/amd64 only. " +
  "Ask for another size, or ask a person to add an arm64 image.";

export const REFUSED_LARGEST =
  "You run on XL (8 CPUs · 32 GiB · 80 GiB), Acme's largest size: there is none larger to ask for. " +
  "Use fewer workers or less data, and say in your work what did not fit.";

export const REFUSED_MOVES =
  "This Run has moved 3 times, the limit for one Run; a Reset does not give a move back. " +
  "Carry on on Standard, or say in your work what did not fit.";

/** Reset's note in the mockup; the agent is told the lead, then ": <note>" or ".". */
export const RESET_NOTE = "The suite passes with --maxWorkers 2 in 9 minutes; keep it there and finish the export first.";
export const resetLead = (to: string) => `Moved back to ${to} by Márcio`;

export const SESSION = {
  title: "Invoice archive: what it would cost to keep 7 years",
  reason:
    "Joining last year's invoices with their lines in DuckDB needs about 18 GiB (the 6 GiB parquet export expands 3×); " +
    "it ran out of Small's 4 GiB, and Standard and Large would not fit it either.",
};

/**
 * Stand-in for the design system's shared stage-words map (agent-startup,
 * with its `park` entry): what each stage says while current and once done.
 * `waiting` names the pool when the move changes it (`startSteps(ids, {pool})`).
 */
export const STAGE_WORDS = {
  park: { label: "Stopping it, keeping its work", doneLabel: "Stopped, work kept" },
  waiting: {
    label: "Waiting for a machine", doneLabel: "Machine found",
    pooled: (pool: string) => ({ label: `Waiting for a machine in ${pool}`, doneLabel: `Machine found in ${pool}` }),
  },
  image: { label: "Getting the image ready", doneLabel: "Image ready" },
  volumes: { label: "Restoring its workspace", doneLabel: "Workspace restored" },
  repositories: { label: "Checking out repositories", doneLabel: "Repositories checked out" },
  container: { label: "Starting the container", doneLabel: "Container started" },
  running: { label: "Starting the agent", doneLabel: "Agent started" },
} as const;

export type StageId = keyof typeof STAGE_WORDS;

/** Every move, in its pool or to another: lux parks the container, keeping its state, and resumes it with the new resources. The park, then the shared resume list (RESUME_STAGES, which shows image and repositories: lux announces both on every resume). */
export const MOVE_STAGES: readonly StageId[] = ["park", "waiting", "image", "volumes", "repositories", "container", "running"];

export type Scenario =
  | "move" | "move-pool" | "refused-platform" | "refused-largest" | "refused-moves" | "conductor" | "run" | "task" | "reset"
  | "session" | "start-over";

export const SCENARIOS: ReadonlyArray<{ readonly value: Scenario; readonly label: string; readonly note: string }> = [
  { value: "move", label: "1 · Moves, same pool", note: "BILL-42's implementer ran out of memory on Standard and asked for Large. Every agent may ask: it is parked and resumed on Large, keeping its conversation and checkout." },
  { value: "move-pool", label: "2 · Moves, another pool", note: "It needs XL, in lux's highmem pool. The same move: parked, then resumed on XL with the new pool. Its conversation and checkout, committed or not, come with it." },
  { value: "refused-platform", label: "3 · Refused: platform", note: "The tester's image is linux/amd64 only and lux's pool list says highmem is arm64, so its size list left XL out. The conductor told it to try XL; it names it anyway, and the tool refuses before parking, with lux's platform_mismatch in plain words. It moves to Large instead." },
  { value: "refused-largest", label: "3 · Refused: largest", note: "The implementer on XL asks again. XL is Acme's largest: the tool says so." },
  { value: "refused-moves", label: "3 · Refused: 3 moves", note: "An implementer that moved to Large 3 times, and was Reset to Standard after each, asks again: 3 moves is the limit per Run, and a Reset gives none back." },
  { value: "conductor", label: "4 · The conductor moves", note: "In the task's Chat, the conductor asks Standard → Large to run the backend suite. Same pool, so it is resized in place, keeping the Chat's conversation." },
  { value: "run", label: "5 · Run header", note: "The fixer's Run later in the task: its machine chip says Large, raised for this task. The tooltip says who asked and why. Nobody approved: the agent decided." },
  { value: "task", label: "6 · Task", note: "The task's Overview: the machines raised in this task, beside the pull request. Anyone in the project may Reset." },
  { value: "reset", label: "6 · Reset", note: "Reset moves the running implementer back down now, with an optional note it is told. One plain confirm for any move: it keeps its work, in its pool or across." },
  { value: "session", label: "7 · Session", note: "A brainstorm on Small needs XL, in the highmem pool. It moves like any Run: parked, resumed on XL, with its conversation and archive.db." },
  { value: "start-over", label: "8 · After Start over", note: "Attempt 2's first implementer Run: Start over keeps the task, so it starts on Large, raised for this task." },
];

/*
 * The machinemoves mockup's world: Acme's sizes, BILL-42 and its
 * implementer, its conductor, a brainstorm session, and what dude tells
 * each agent when its move applies, word for word as
 * orchestrator/internal/phases/resize.go (moveTold, keptInput) and
 * delivery/machine_refusals.go (ForAgent, ForPerson) write them.
 */

import type { Machine } from "@dude/domain";

export const ORG = "org_acme";
export const PROJECT_ID = "prj_billing";
export const TASK_ID = "tsk_bill42";
export const SESSION_ID = "ses_archive";
export const TASK = { key: "BILL-42", title: "Faster invoice export", branch: "dude/BILL-42/attempt-1" };
export const SESSION_TITLE = "How big would a seven-year invoice archive be?";

export const PEOPLE = {
  marcio: { id: "u_marcio", name: "Márcio Martins" },
  ana: { id: "u_ana", name: "Ana Ribeiro" },
} as const;

function size(sizeId: string, name: string, cpus: number, memoryGiB: number, diskGiB: number, pool: string | null, poolId: string | null): Machine {
  return { sizeId, name, cpus, memoryMiB: memoryGiB * 1024, diskGiB, poolId, pool, from: "project" };
}

export const SMALL = size("ms_small", "Small", 1, 4, 20, "default", null);
export const STANDARD = size("ms_standard", "Standard", 2, 8, 20, "default", null);
export const LARGE = size("ms_large", "Large", 4, 16, 40, "default", null);
export const XL = size("ms_xl", "XL", 8, 32, 80, "highmem", "pool_highmem");

/** delivery.Machine.Spec: "4 CPUs · 16 GiB · 40 GiB". */
export const spec = (m: Machine) => `${m.cpus === 1 ? "1 CPU" : `${m.cpus} CPUs`} · ${m.memoryMiB / 1024} GiB · ${m.diskGiB} GiB`;

export const REASON_LARGE =
  "The backend jest suite was OOM-killed (exit 137) with --maxWorkers 4: four workers rendering PDFs ran out of Standard's 8 GiB. Large (16 GiB) fits four.";
export const REASON_XL =
  "Loading the full 2025 invoice fixture (2.1 million rows) into the export's in-memory sort needs about 20 GiB; it was OOM-killed on Standard and would not fit Large either.";
export const REASON_CONDUCTOR =
  "Márcio asked me to run the backend suite with 4 workers before I hand over; on Standard (8 GiB) that is what OOM-killed the implementer.";
export const REASON_SESSION =
  "Joining last year's invoices with their lines in DuckDB needs about 18 GiB; Small (4 GiB) ran out at 3.7 GiB.";

const KEEPS =
  "Your conversation and your checkout are kept, committed or not, with everything else on /workspace and your home. " +
  "Running processes and /tmp are not: start your servers and watchers again.";

/** request_machine's answer, as the agent reads it in the call. */
export function answer(to: Machine, later: string, otherPool: boolean): string {
  return `Moved: you run on ${to.name} (${spec(to)})${otherPool ? ` in the ${to.pool} pool` : ""} from your next step. ${KEEPS} ${later} End your turn now; you are resumed on ${to.name}.`;
}

/** resize.go moveTold, a raise: "You now run on Large (…)[ in highmem]. Start again … Carry on." */
export function toldRaise(to: Machine, otherPool: boolean): string {
  return `You now run on ${to.name} (${spec(to)})${otherPool ? ` in ${to.pool}` : ""}. Start again any server or watcher you had running. Carry on.`;
}

/** resize.go moveTold, a Reset: "Moved back to Standard by Ana Ribeiro: <note>". */
export function toldReset(to: Machine, by: string, note: string): string {
  return note ? `Moved back to ${to.name} by ${by}: ${note}` : `Moved back to ${to.name} by ${by}.`;
}

/** machine_refusals.go platform_mismatch: what lux refused, and the agent's advice. */
export const REFUSED_SAID = "XL is in highmem, whose machines are linux/arm64; this agent's image is linux/amd64 only.";
export const REFUSED_ADVICE = " Ask for another size, or ask a person to add an arm64 image.";
/** ForPerson: machine.refused's message. */
export const REFUSED_FOR_PERSON = REFUSED_SAID.replace(/\.$/, "");

/** resize.go keptInput, a raise lux refused at the real resume. */
export function toldKept(asked: Machine, kept: Machine): string {
  return `You asked for ${asked.name}; lux kept you on ${kept.name}: ${REFUSED_SAID}${REFUSED_ADVICE}`;
}

export const RESET_NOTE = "The suite fits in 8 GiB with --maxWorkers 2 since the PDF fixtures were shrunk; keep it at 2.";
export const ANA_STEER = "When you are back, run the PDF tests on their own first, then the rest of the suite.";

/*
 * Each state's ledger, as the orchestrator writes it today, event by event,
 * plus the one event the proposal adds: `run.told`, on the Run in the
 * transaction that records the resume's acceptance, carrying the input
 * dude sent lux with the resume, verbatim. Today's fold ignores it (an
 * unknown type), so "before" is these same ledgers without it.
 */

import { EventTypes, type Machine, type PersistedEvent } from "@dude/domain";
import {
  ANA_STEER, LARGE, ORG, PEOPLE, PROJECT_ID, REASON_CONDUCTOR, REASON_LARGE, REASON_SESSION, REASON_XL, REFUSED_FOR_PERSON, RESET_NOTE, SESSION_ID, SMALL,
  STANDARD, TASK_ID, XL, answer, toldKept, toldRaise, toldReset,
} from "./data.ts";

/** The proposed event: what dude put in front of the agent's input at a move's resume. */
export const RUN_TOLD = "run.told";

/** phases.resumeNudge, which the resume carries after the move's sentence unless a person's words wait. */
const RESUME_NUDGE = "You were paused and have been resumed. Continue the task where you left off.";

// The ledgers end a few minutes before the page is opened, so the live clocks read as a Run at work.
const BASE = Math.floor(Date.now() / 60_000) * 60_000;
const S = 1_000;
const MIN = 60 * S;

type Actor = PersistedEvent["actor"];
const SYSTEM: Actor = { type: "system", id: "dude" };
const AGENT: Actor = { type: "agent", id: "agent" };
const person = (p: { id: string; name: string }): Actor => ({ type: "person", id: p.id, name: p.name });

/** A Run's ledger, written in order: each event one cursor on. */
class Ledger {
  readonly events: PersistedEvent[] = [];
  private cursor = 0;
  constructor(readonly runId: string, readonly where: { taskId: string | null; sessionId: string | null }) {}

  add(eventType: string, atMs: number, payload: Record<string, unknown> = {}, actor: Actor = SYSTEM): this {
    this.cursor += 1;
    this.events.push({
      eventId: `ev_${this.runId}_${this.cursor}`, eventType, occurredAt: new Date(BASE + atMs).toISOString(), organizationId: ORG,
      projectId: PROJECT_ID, taskId: this.where.taskId, runId: this.runId, sessionId: this.where.sessionId, workflowRunId: null,
      actor, source: actor.type === "agent" ? "harness" : actor.type === "person" ? "web" : "control-plane",
      correlationId: null, causationId: null, payload, cursor: this.cursor,
    });
    return this;
  }

  say(atMs: number, text: string, contextTokens = 61_000): this {
    return this.add(EventTypes.AgentMessage, atMs, { text, contextTokens }, AGENT);
  }

  bash(atMs: number, id: string, command: string, tookMs: number, out?: { exitCode: number; stderr: string }): this {
    this.add(EventTypes.ToolCalled, atMs, { callId: id, tool: "bash", input: { command } }, AGENT);
    if (tookMs < 0) return this;
    return this.add(EventTypes.ToolCompleted, atMs + tookMs, {
      callId: id, tool: "bash", status: out && out.exitCode !== 0 ? "error" : "completed",
      ...(out ? { exitCode: out.exitCode, stderr: { head: out.stderr } } : { exitCode: 0, stdout: { head: "Tests: 412 passed, 412 total" } }),
    }, AGENT);
  }

  /** The agent's request_machine call, accepted (dude's machine.requested and machine.raised in between), and its turn's end. */
  request(atMs: number, role: string, from: Machine, to: Machine, reason: string, later: string, moveId: string): this {
    const pool = poolOf(from, to);
    this.add(EventTypes.ToolCalled, atMs, { callId: `call_${moveId}`, tool: "request_machine", input: { size: to.name, reason } }, AGENT);
    this.add(EventTypes.MachineRequested, atMs + 600, { moveId, role, from, to, reason, ...(pool ? { pool } : {}) });
    this.add(EventTypes.MachineRaised, atMs + 610, { moveId, role, from, to, ...(pool ? { pool } : {}) });
    const said = JSON.stringify({ size: to.name, ...(pool ? { pool: to.pool } : {}), answer: answer(to, later, pool !== null) });
    this.add(EventTypes.ToolCompleted, atMs + 700, { callId: `call_${moveId}`, tool: "request_machine", status: "completed", output: { head: said } }, AGENT);
    return this.add(EventTypes.SessionStopped, atMs + 2_000, { reason: "turn_complete" }, AGENT);
  }

  /** dude's stop for the move, confirmed. */
  parked(atMs: number): this {
    return this.add(EventTypes.RunPaused, atMs, { confirmed: true });
  }

  /** lux's stages of the resumed placement, from `atMs`, each lasting its ms. */
  stages(atMs: number, epoch: number, took: ReadonlyArray<readonly [string, number]>): number {
    let t = atMs;
    for (const [stage, ms] of took) {
      this.add(EventTypes.RunStage, t, { stage, since: new Date(BASE + t).toISOString(), epoch });
      t += ms;
    }
    return t;
  }

  /** The resume's timing, from the move's start (woken) to the agent's first output. */
  timed(atMs: number, epoch: number, cause: string, phases: Record<string, number>): this {
    const totalMs = Object.values(phases).reduce((a, b) => a + b, 0);
    const untilBusyMs = totalMs - (phases.firstOutput ?? 0);
    return this.add(EventTypes.RunResumeTimed, atMs, { epoch, cause, moved: true, hostName: "host-b", totalMs, untilBusyMs, phases });
  }
}

/** A move event's pool, when the move changes it. */
function poolOf(from: Machine, to: Machine) {
  return from.poolId === to.poolId ? null : { from: { id: from.poolId, name: from.pool }, to: { id: to.poolId, name: to.pool } };
}

/** runs.machine as recorded after the resume: the size lux applied, on the resumed epoch. */
function resized(m: Machine, epoch: number, from: "task" | "project", note?: string) {
  return { machine: { ...m, from, sinceEpoch: epoch, ...(note ? { note } : {}) } };
}

const RESUME_STAGES: ReadonlyArray<readonly [string, number]> = [
  ["waiting", 3_100], ["image", 400], ["volumes", 6_200], ["repositories", 900], ["container", 1_100], ["running", 11_500],
];

/** The phases of run.resume.timed for a move: `react` is the park, from the move's start to dude asking lux to resume. */
function movePhases(park: number, extra = 0) {
  return { react: park, schedule: 3_100 + extra, image: 400, restore: 6_200, start: 2_000, reload: 11_500, take: 1_200, firstOutput: 13_700 };
}

function implementerOpening(l: Ledger, prompt: string) {
  l.add(EventTypes.RunCreated, -60 * MIN, { phase: "implement", role: "implementer" });
  l.add(EventTypes.PromptDelivered, -59 * MIN, { text: prompt, lands: "next_step" });
  return l;
}

const IMPLEMENT_PROMPT =
  "Implement BILL-42, “Faster invoice export”, on `dude/BILL-42/attempt-1`.\n\nExporting a large customer's invoices times out. Stream the CSV from the database instead of building it in memory, so Globex's 2.1M invoices export in under a minute.";

/** States 1 and 2: the implementer raises itself, in its pool or to another. */
export function raise(otherPool: boolean): PersistedEvent[] {
  const to = otherPool ? XL : LARGE;
  const l = implementerOpening(new Ledger("run_7f3k", { taskId: TASK_ID, sessionId: null }), IMPLEMENT_PROMPT);
  l.say(-12 * MIN, otherPool
    ? "The chunked export works on the 2025 sample. Loading the full fixture to check the sort's peak memory."
    : "Streaming from a server-side cursor now. Running the backend suite with the workers CI uses.");
  l.bash(-11 * MIN, "c1", otherPool ? "npx jest --maxWorkers 1 export/full-fixture" : "npx jest --maxWorkers 4 packages/backend", 3 * MIN,
    { exitCode: 137, stderr: "Killed" });
  l.say(-7 * MIN, otherPool
    ? "Killed at 8 GiB: the full fixture's sort needs about 20 GiB, more than Large has. Asking for XL."
    : "Killed (exit 137): four workers rendering PDFs do not fit Standard's 8 GiB. Asking for Large rather than calling it flaky.");
  const move = `mmv_${otherPool ? "xl" : "large"}`;
  l.request(-6 * MIN, "implementer", STANDARD, to, otherPool ? REASON_XL : REASON_LARGE, "Later implementer and fixer Runs in this task run on " + to.name + " too.", move);
  // The move: dude's stop (park), the resume lux accepted with the size, then the new placement's stages.
  const start = -6 * MIN + 600;
  const park = 9_800;
  l.parked(start + park - 200);
  l.add(EventTypes.RunResized, start + park + 300, resized(to, 2, "task"));
  l.add(RUN_TOLD, start + park + 320, { epoch: 2, moveId: move, kind: "moved", text: `${toldRaise(to, otherPool)}\n\n${RESUME_NUDGE}` });
  const extra = otherPool ? 24_000 : 0;
  const up = l.stages(start + park + 400, 2, RESUME_STAGES.map(([s, ms]) => [s, s === "waiting" ? ms + extra : ms] as const));
  l.add(EventTypes.SessionStarted, up + 1_200, {}, AGENT);
  const first = start + Object.values(movePhases(park, extra)).reduce((a, b) => a + b, 0);
  l.say(first, otherPool
    ? "On XL in highmem now, same checkout. Running the full fixture again, one worker, and watching the sort's peak."
    : "On Large now, same checkout. Running the backend suite again with 4 workers.", 64_000);
  l.timed(first + 300, 2, "machine", movePhases(park, extra));
  l.bash(first + 2_000, "c2", otherPool ? "npx jest --maxWorkers 1 export/full-fixture" : "npx jest --maxWorkers 4 packages/backend", -1);
  return l.events;
}

/** States 3 and 7: Ana resets the implementer to Standard with a note; in 7 she also steers it on the same resume. */
export function reset(withSteer: boolean): PersistedEvent[] {
  const l = implementerOpening(new Ledger("run_9b2m", { taskId: TASK_ID, sessionId: null }),
    "Fix the review's 2 open findings on `dude/BILL-42/attempt-1`.\n\nYou run on Large (4 CPUs · 16 GiB · 40 GiB), raised for this task after the implementer's jest run ran out of memory.");
  l.say(-9 * MIN, "Both findings are in `stream.ts`: the cursor is not closed on a client abort, and the header row is written twice on retry. Fixing both, then the suite.");
  l.bash(-8 * MIN, "c1", "npx jest --maxWorkers 4 packages/backend", 2 * MIN);
  l.say(-5 * MIN, "Suite green with 4 workers. Writing the abort test next.");
  const move = "mmv_reset";
  const start = -3 * MIN;
  l.bash(-4 * MIN, "c2", "npx jest packages/backend/export/stream.test.ts --watch", -1);
  l.add(EventTypes.MachineReset, start, {
    moveId: move, role: "implementer", from: { ...LARGE, from: "task" }, to: STANDARD, by: { id: PEOPLE.ana.id, name: PEOPLE.ana.name }, note: RESET_NOTE,
  }, person(PEOPLE.ana));
  if (withSteer) l.add(EventTypes.RunSteered, start + 6_000, { directiveId: "dir_ana", text: ANA_STEER }, person(PEOPLE.ana));
  const park = 7_400;
  // The watcher ends with the move's stop: running processes are not kept.
  l.add(EventTypes.ToolCompleted, start + park - 900, { callId: "c2", tool: "bash", status: "error", exitCode: 143, stderr: { head: "Terminated" } }, AGENT);
  l.parked(start + park - 200);
  l.add(EventTypes.RunResized, start + park + 300, resized(STANDARD, 2, "project"));
  // A person's words wait (the steer): the resume carries the move's sentence alone, and the steer goes as its own input once running.
  l.add(RUN_TOLD, start + park + 320, { epoch: 2, moveId: move, kind: "moved",
    text: withSteer ? toldReset(STANDARD, PEOPLE.ana.name, RESET_NOTE) : `${toldReset(STANDARD, PEOPLE.ana.name, RESET_NOTE)}\n\n${RESUME_NUDGE}` });
  const up = l.stages(start + park + 400, 2, RESUME_STAGES);
  l.add(EventTypes.SessionStarted, up + 1_200, {}, AGENT);
  const first = start + Object.values(movePhases(park)).reduce((a, b) => a + b, 0);
  l.say(first, "Back on Standard: the watcher I had running stopped with the move. Running the stream tests with --maxWorkers 2, as the note says.", 66_000);
  l.timed(first + 300, 2, "machine", movePhases(park));
  if (withSteer) {
    l.add(EventTypes.DirectiveAccepted, first + 1_000, { directiveId: "dir_ana", receipt: true, lands: "next_step" });
    l.bash(first + 2_000, "c3", "npx jest --maxWorkers 2 packages/backend/export", 70_000);
    l.add(EventTypes.DirectiveDelivered, first + 72_500, { directiveId: "dir_ana", read: true });
    l.say(first + 80_000, "Running the PDF tests on their own first, as Ana asked.");
    l.bash(first + 81_000, "c4", "npx jest --maxWorkers 2 packages/backend/pdf", -1);
  } else {
    l.bash(first + 2_000, "c3", "npx jest --maxWorkers 2 packages/backend/export", -1);
  }
  return l.events;
}

/** State 4: the dry run passed, then lux refused XL at the real resume; the Run resumed on Standard. */
export function refused(): PersistedEvent[] {
  const l = implementerOpening(new Ledger("run_7f3k", { taskId: TASK_ID, sessionId: null }), IMPLEMENT_PROMPT);
  l.say(-12 * MIN, "Loading the full fixture to check the sort's peak memory.");
  l.bash(-11 * MIN, "c1", "npx jest --maxWorkers 1 export/full-fixture", 3 * MIN, { exitCode: 137, stderr: "Killed" });
  l.say(-7 * MIN, "Killed at 8 GiB: the sort needs about 20 GiB. Asking for XL.");
  const move = "mmv_xl";
  l.request(-6 * MIN, "implementer", STANDARD, XL, REASON_XL, "Later implementer and fixer Runs in this task run on XL too.", move);
  const start = -6 * MIN + 600;
  const park = 9_600;
  l.parked(start + park - 200);
  // lux refused the sized resume (422 platform_mismatch); dude resumed it without resources, on Standard.
  l.add(EventTypes.RunResized, start + park + 900, resized(STANDARD, 1, "project", "Its settings now name XL, but this resume kept Standard (lux refused its resources)."));
  const pool = poolOf(STANDARD, XL);
  l.add(EventTypes.MachineRefused, start + park + 950, {
    moveId: move, role: "implementer", kind: "raise", from: STANDARD, to: XL, ...(pool ? { pool } : {}), kept: STANDARD, code: "platform_mismatch", message: REFUSED_FOR_PERSON,
  });
  l.add(RUN_TOLD, start + park + 970, { epoch: 2, moveId: move, kind: "kept", text: `${toldKept(XL, STANDARD)}\n\n${RESUME_NUDGE}` });
  const up = l.stages(start + park + 1_000, 2, RESUME_STAGES);
  l.add(EventTypes.SessionStarted, up + 1_200, {}, AGENT);
  const first = start + Object.values(movePhases(park)).reduce((a, b) => a + b, 0) + 600;
  l.say(first, "XL's machines cannot run this image. Staying on Standard: sorting in 8 chunks of 270k rows and merging the runs on disk keeps the peak under 7 GiB.", 64_000);
  l.timed(first + 300, 2, "machine", { ...movePhases(park), firstOutput: 14_300 });
  l.bash(first + 2_000, "c2", "npx jest --maxWorkers 1 export/full-fixture", -1);
  return l.events;
}

/** State 5: the task's conductor moves itself, in its Chat. */
export function conductor(): PersistedEvent[] {
  const l = new Ledger("run_c0n1", { taskId: TASK_ID, sessionId: null });
  l.add(EventTypes.RunCreated, -20 * MIN, { role: "conductor" });
  l.add(EventTypes.ChatMessage, -20 * MIN, { text: "Before it goes to review, run the whole backend suite yourself with the workers CI uses (4). I want to see it green on this branch." },
    person(PEOPLE.marcio));
  l.add(EventTypes.ConductorBriefed, -20 * MIN + 400, {
    text: "Conductor, Márcio wrote in the Chat of BILL-42, “Faster invoice export”. You are this task's conductor: answer them.\n\n## The task\n\nBILL-42 · status running\n\n## Runs (1)\n\n- run_7f3k · implement · implementer · completed · Streams the export from a server-side cursor.\n\n## Márcio Martins's message\n\nBefore it goes to review…",
  });
  l.add(EventTypes.SessionStarted, -19 * MIN, {}, AGENT);
  l.say(-18 * MIN, "With 4 workers the suite renders PDFs in each of them; on Standard (8 GiB) that is what OOM-killed the implementer. Asking for Large first, then I run it.");
  const move = "mmv_conductor";
  l.request(-17 * MIN, "conductor", STANDARD, LARGE, REASON_CONDUCTOR, "Later conductors in this task run on Large too.", move);
  const start = -17 * MIN + 600;
  const park = 6_900;
  l.parked(start + park - 200);
  l.add(EventTypes.RunResized, start + park + 300, resized(LARGE, 2, "task"));
  l.add(RUN_TOLD, start + park + 320, { epoch: 2, moveId: move, kind: "moved", text: `${toldRaise(LARGE, false)}\n\n${RESUME_NUDGE}` });
  const up = l.stages(start + park + 400, 2, RESUME_STAGES);
  l.add(EventTypes.SessionStarted, up + 1_200, {}, AGENT);
  const first = start + Object.values(movePhases(park)).reduce((a, b) => a + b, 0);
  l.say(first, "On Large now. Running the backend suite with 4 workers on this branch.", 52_000);
  l.timed(first + 300, 2, "machine", movePhases(park));
  l.bash(first + 2_000, "c1", "npx jest --maxWorkers 4 packages/backend", 4 * MIN);
  l.say(first + 4 * MIN + 10_000, "Green: 412 tests in 3m 58s with 4 workers on Large. It is ready for review.", 55_000);
  return l.events;
}

/** State 6: a brainstorm session moves Small → XL, in the highmem pool. */
export function session(): PersistedEvent[] {
  const l = new Ledger("run_br41", { taskId: null, sessionId: SESSION_ID });
  l.add(EventTypes.RunCreated, -20 * MIN, { role: "brainstorm" });
  l.add(EventTypes.ChatMessage, -16 * MIN, { text: "How much would it cost to keep seven years of invoices in the archive? Use last year's, with their lines, as the sample." },
    person(PEOPLE.ana));
  l.add(EventTypes.BrainstormBriefed, -16 * MIN + 300, { text: "You are a brainstorm session's agent…\n\n## The first message\n\nHow much would it cost…" });
  l.add(EventTypes.SessionStarted, -15 * MIN, {}, AGENT);
  l.say(-14 * MIN, "I'll load last year's invoices and their lines from the parquet export into DuckDB and measure what they take compressed.");
  l.bash(-13 * MIN, "c1", "duckdb archive.db -c \"CREATE TABLE inv AS SELECT * FROM 'invoices-2025.parquet' JOIN 'lines-2025.parquet' USING (id)\"", 48_000,
    { exitCode: 1, stderr: "Out of Memory Error: could not allocate block of 262144 bytes (3.7 GiB/3.7 GiB used)" });
  const move = "mmv_session";
  l.request(-11 * MIN, "brainstorm", SMALL, XL, REASON_SESSION, "", move);
  const start = -11 * MIN + 600;
  const park = 8_300;
  l.parked(start + park - 200);
  l.add(EventTypes.RunResized, start + park + 300, resized(XL, 2, "task"));
  l.add(RUN_TOLD, start + park + 320, { epoch: 2, moveId: move, kind: "moved", text: `${toldRaise(XL, true)}\n\n${RESUME_NUDGE}` });
  const up = l.stages(start + park + 400, 2, RESUME_STAGES.map(([s, ms]) => [s, s === "waiting" ? ms + 19_000 : ms] as const));
  l.add(EventTypes.SessionStarted, up + 1_200, {}, AGENT);
  const first = start + Object.values(movePhases(park, 19_000)).reduce((a, b) => a + b, 0);
  l.say(first, "On XL now, in the same workspace: `archive.db` and both parquet files are where I left them. Running the join again, then measuring the compressed size per year.", 40_000);
  l.timed(first + 300, 2, "machine", movePhases(park, 19_000));
  l.bash(first + 2_000, "c2", "duckdb archive.db -c \"CREATE TABLE inv AS SELECT * FROM 'invoices-2025.parquet' JOIN 'lines-2025.parquet' USING (id)\"", -1);
  return l.events;
}

export const NOW = BASE;

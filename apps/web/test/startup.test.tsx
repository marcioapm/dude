/**
 * While an agent starts: the projection's `start` from `run.stage`, and
 * where the screens show it — the row where its first turn will be, the
 * header's chip, the queued message and the composer — on a brainstorm
 * session, a task's Run and a conductor's Chat.
 */

import { afterEach, describe, expect, test } from "bun:test";
import type { PersistedEvent, RunStatus, TaskServers } from "@dude/domain";
import { act, mount, until } from "./dom.ts";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { FixtureClient, emit, type LedgerQuery } from "../src/fixtures/client.ts";
import { RUN_ID, TASK_ID, run } from "../src/fixtures/data.ts";
import { PeopleProvider } from "../src/people.tsx";
import { TaskScreen } from "../src/screens/TaskScreen.tsx";
import { NOT_STARTED, NOT_STARTED_TAKEN, READ_WHEN_BACK, READ_WHEN_STARTED, project, type StartState } from "../src/api/conversation.ts";
import { SLOW_AFTER_MS, slowNote } from "../src/startCue.tsx";
import { runStatusShown } from "../src/runPresentation.tsx";
import { RunScreen } from "../src/screens/RunScreen.tsx";
import type { RunDetail } from "../src/api/client.ts";
import { ApiError } from "../src/api/client.ts";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  localStorage.clear();
});

const T0 = Date.parse("2026-10-12T10:00:00Z");
const at = (ms: number) => new Date(T0 + ms).toISOString();
let cursor = 0;
function ev(eventType: string, payload: Record<string, unknown>, ms: number, actor: PersistedEvent["actor"] = { type: "system", id: "dude" }): PersistedEvent {
  cursor += 1;
  return { eventId: `evt_st${cursor}`, cursor, eventType, occurredAt: at(ms), organizationId: "org_1", projectId: "prj_1", taskId: TASK_ID,
    runId: RUN_ID, sessionId: null, workflowRunId: null, actor, source: "orchestrator", correlationId: null, causationId: null, payload };
}
const stage = (s: string, ms: number, epoch = 1, reason?: string) => ev("run.stage", { stage: s, since: at(ms), epoch, ...(reason ? { reason } : {}) }, ms + 5);
const created = (ms = 0) => ev("run.created", { role: "brainstorm" }, ms);
const message = (text: string, ms: number) => ev("chat.message", { text }, ms, { type: "person", id: "per_marcio" } as PersistedEvent["actor"]);

describe("the projection", () => {
  test("a stage row while starting: the current stage, its since, the passed ones with their times", () => {
    const c = project([created(), stage("waiting", 0), stage("image", 4_000), stage("volumes", 9_000)], "scheduled");
    expect(c.start).toEqual({
      kind: "start", stage: "volumes", stageSince: at(9_000), startedAt: at(0), luxReason: null, failed: null,
      done: [{ stage: "waiting", tookMs: 4_000 }, { stage: "image", tookMs: 5_000 }],
    } satisfies StartState);
  });

  test("gone once the agent is up, with a summary line for a fresh start: Started in …, its stages in the hover", () => {
    const events = [created(), stage("waiting", 0), stage("image", 4_000), stage("running", 9_000),
      ev("agent.session.started", { role: "brainstorm" }, 31_000)];
    const c = project(events, "running");
    expect(c.start).toBeNull();
    const line = c.turns.find((t) => t.kind === "notice")!;
    expect(line).toMatchObject({ text: "Started in 31s.", title: "Machine found 4.0s\nImage ready 5.0s\nAgent started 22s" });
  });

  test("a fresh Run lux reports running is no longer starting, whatever its agent has said", () => {
    const c = project([created(), stage("waiting", 0), stage("container", 3_000)], "running");
    expect(c.start).toBeNull();
  });

  test("run.started ends the start in the projection itself, before any record of the agent and with no status given", () => {
    const c = project([created(), stage("waiting", 0), stage("container", 3_000), ev("run.started", {}, 8_000)]);
    expect(c.start).toBeNull();
  });

  test("a resume: kind resume after run.unparked, its stages, no summary line of its own", () => {
    const events = [created(), stage("waiting", 0), ev("agent.message", { text: "hi" }, 10_000),
      ev("run.parked", { reason: "session" }, 20_000), ev("run.unparked", { reason: "session", epoch: 2 }, 30_000),
      stage("waiting", 30_000, 2), stage("volumes", 32_000, 2)];
    const c = project(events, "running");
    expect(c.start).toMatchObject({ kind: "resume", stage: "volumes", done: [{ stage: "waiting", tookMs: 2_000 }] });
    const up = project([...events, ev("agent.message", { text: "back" }, 40_000)], "running");
    expect(up.start).toBeNull();
    // The first start's line, and none for the resume: its "Taken back up" line says it.
    expect(up.turns.filter((t) => t.kind === "notice" && t.text.startsWith("Started")).map((t) => t.at)).toEqual([at(10_000)]);
  });

  const notices = (events: PersistedEvent[]) => project(events, "running").turns.flatMap((t) => t.kind === "notice" ? [t.text] : []);

  test("a resume says 'Taken back up' only once the agent is up, timed; never while its start is live", () => {
    const ran = [created(), stage("waiting", 0), ev("agent.message", { text: "hi" }, 10_000), ev("run.parked", { reason: "session" }, 20_000)];
    const resuming = [...ran, message("and now?", 29_000), ev("run.unparked", { reason: "session", epoch: 2 }, 30_000),
      stage("waiting", 30_000, 2), stage("volumes", 32_000, 2), stage("running", 40_000, 2)];
    expect(notices(resuming).filter((n) => n.startsWith("Taken back up"))).toEqual([]);
    const up = [...resuming, ev("run.resume.timed", { epoch: 2, totalMs: 30_000, phases: {} }, 60_000), ev("agent.message", { text: "back" }, 60_001)];
    expect(notices(up).filter((n) => n.startsWith("Taken back up"))).toEqual(["Taken back up in 30s."]);
    // It sits where the agent came back, before its first words.
    const turns = project(up, "running").turns;
    expect(turns.at(-2)).toMatchObject({ kind: "notice", text: "Taken back up in 30s.", at: at(60_000) });
  });

  test("a move says 'Taken back up' only once the agent is up; never while its start is live", () => {
    const moving = [created(), stage("waiting", 0), ev("agent.message", { text: "hi" }, 10_000),
      stage("park", 20_000, 1, "migrate"), ev("run.unparked", { reason: "migrate", epoch: 2 }, 25_000),
      stage("waiting", 25_000, 2), stage("running", 30_000, 2)];
    expect(project(moving, "running").start).toMatchObject({ kind: "move", stage: "running" });
    expect(notices(moving).filter((n) => n.startsWith("Taken back up"))).toEqual([]);
    expect(notices([...moving, ev("agent.message", { text: "on Large now" }, 41_000)]).filter((n) => n.startsWith("Taken back up"))).toHaveLength(1);
  });

  test("a resume or a move that fails shows the failure, never 'Taken back up'", () => {
    const resumeFailed = [created(), stage("waiting", 0), ev("agent.message", { text: "hi" }, 10_000), ev("run.parked", { reason: "session" }, 20_000),
      ev("run.unparked", { reason: "session", epoch: 2 }, 30_000), stage("waiting", 30_000, 2),
      ev("run.failed", { status: "failed", error: "x", stage: "waiting", reason: "terminated" }, 40_000)];
    const moveFailed = [created(), stage("waiting", 0), ev("agent.message", { text: "hi" }, 10_000), stage("park", 20_000, 1, "migrate"),
      ev("run.unparked", { reason: "migrate", epoch: 2 }, 25_000), stage("waiting", 25_000, 2),
      ev("run.failed", { status: "failed", error: "x", stage: "waiting", reason: "no capacity" }, 40_000)];
    for (const [events, kind] of [[resumeFailed, "resume"], [moveFailed, "move"]] as const) {
      const c = project(events, "failed");
      expect(c.turns.some((t) => t.kind === "notice" && t.text.startsWith("Taken back up"))).toBe(false);
      const ended = c.turns.find((t) => t.kind === "ended")!;
      expect(ended.kind === "ended" && ended.start?.kind).toBe(kind);
    }
  });

  // A harness taken back up can report a call it finished before the park again; that call keeps its time.
  for (const how of ["resume", "move"] as const) {
    test(`a tool call that finished before a ${how} keeps its duration through it and after`, () => {
      const tool = (type: "agent.tool.called" | "agent.tool.completed", name: string, callId: string, ms: number) =>
        ev(type, { tool: name, callId, ...(type === "agent.tool.completed" ? { status: "completed" } : { input: {} }) }, ms);
      const first = how === "move" ? "request_machine" : "read";
      const before = [created(), stage("waiting", 0), stage("running", 100),
        tool("agent.tool.called", first, "call_1", 1_000), tool("agent.tool.completed", first, "call_1", 1_001)];
      const park = how === "move"
        ? [stage("park", 5_000, 1, "migrate"), ev("run.unparked", { reason: "migrate", epoch: 2 }, 9_000)]
        : [ev("run.parked", { reason: "session" }, 5_000), ev("run.unparked", { reason: "session", epoch: 2 }, 30_000)];
      const back = [stage("waiting", 31_000, 2), stage("volumes", 33_000, 2), stage("running", 38_000, 2),
        // The harness's replay of what it already did, then new work.
        tool("agent.tool.called", first, "call_1", 40_000), tool("agent.tool.completed", first, "call_1", 41_000),
        ev("agent.message", { text: "back" }, 42_000), tool("agent.tool.called", "bash", "call_2", 43_000), tool("agent.tool.completed", "bash", "call_2", 45_000)];
      const tookMs = (events: PersistedEvent[]) => project(events, "running").turns.flatMap((t) =>
        t.kind === "tool" ? [[t.id, Date.parse(t.endedAt!) - Date.parse(t.startedAt)]] : []);
      expect(tookMs(before)).toEqual([["call_1", 1]]);
      expect(tookMs([...before, ...park, ...back])).toEqual([["call_1", 1], ["call_2", 2_000]]);
      expect(project([...before, ...park, ...back], "running").toolCounts.get(first)).toBe(1);
    });
  }

  test("a move: park leads, and it reads as a move", () => {
    const events = [created(), stage("waiting", 0), ev("agent.message", { text: "hi" }, 10_000),
      stage("park", 20_000, 1, "drain"), stage("waiting", 25_000, 1), stage("volumes", 27_000, 2)];
    expect(project(events, "running").start).toMatchObject({ kind: "move", stage: "volumes",
      done: [{ stage: "park", tookMs: 5_000 }, { stage: "waiting", tookMs: 2_000 }] });
  });

  test("failed with its stage and lux's reason; the queued message was not delivered", () => {
    const events = [created(), message("plan the epic", 1), stage("waiting", 0), stage("image", 5_000),
      ev("run.failed", { status: "failed", error: "the agent's run ended before finishing its task: pull failed", stage: "image", reason: "pull failed: manifest unknown" }, 50_000)];
    const c = project(events, "failed");
    expect(c.start).toBeNull();
    const ended = c.turns.find((t) => t.kind === "ended")!;
    expect(ended.kind === "ended" && ended.start).toMatchObject({ stage: "image", failed: { reason: "pull failed: manifest unknown", source: "lux" },
      done: [{ stage: "waiting", tookMs: 5_000 }] });
    const sent = c.turns.find((t) => t.kind === "human")!;
    expect(sent).toMatchObject({ deliveredAt: null, failed: NOT_STARTED });
    // The header's failed state, until a next agent is created.
    expect(c.startFailed).toMatchObject({ stage: "image", failed: { source: "lux" } });
    expect(project([...events, created(60_000)], "pending").startFailed).toBeNull();
  });

  test("a drained Run whose resumePolicy (manual) fails it at the stop did not fail a start: no stopping-it failure", () => {
    const ran = [created(), stage("waiting", 0), ev("agent.message", { text: "hi" }, 10_000), stage("park", 20_000, 1, "drain")];
    const why = "drain: not resumed (resumePolicy manual)";
    for (const failure of [{ status: "failed", error: why, stage: "park", reason: why }, { status: "failed", error: why }]) {
      const c = project([...ran, ev("run.failed", failure, 25_000)], "failed");
      const ended = c.turns.find((t) => t.kind === "ended")!;
      expect(ended.kind === "ended" && ended.start).toBeFalsy();
      expect(ended).toMatchObject({ text: `Failed: ${why}` });
      expect(c.start).toBeNull();
    }
  });

  test("a start that ended (run.completed) before its agent came up: the next agent's start does not mark its message read", () => {
    const events = [created(), message("plan the epic", 1), stage("waiting", 2),
      ev("run.completed", { status: "completed", reason: "its container stopped" }, 5_000),
      created(6_000), stage("waiting", 6_001), ev("agent.message", { text: "Hello." }, 9_000)];
    const c = project(events, "running");
    expect(c.turns.find((t) => t.kind === "human")).toMatchObject({ text: "plan the epic", deliveredAt: null });
    expect(c.start).toBeNull();
    // And with no next Run, before the screen knows its status: the ended start is not shown as still starting.
    expect(project(events.slice(0, 4)).start).toBeNull();
    // A next Run with no run.completed before it (a swap while starting): the same.
    const swapped = project([...events.slice(0, 3), ...events.slice(4)], "running");
    expect(swapped.turns.find((t) => t.kind === "human")).toMatchObject({ text: "plan the epic", deliveredAt: null });
  });

  test("a failed start a later agent took over without Try again: its message no longer says Try again starts it", () => {
    const failedStart = [created(), message("plan", 1), stage("waiting", 2),
      ev("run.failed", { status: "failed", error: "x", stage: "waiting", reason: "terminated" }, 5_000)];
    // The person wrote again instead: a new agent, no retries.
    const c = project([...failedStart, created(6_000), message("plan the epic, then", 6_001)], "pending");
    const ended = c.turns.find((t) => t.kind === "ended")!;
    expect(ended.kind === "ended" && ended.taken).toBe(true);
    expect(c.turns.filter((t) => t.kind === "human").map((t) => t.kind === "human" && [t.text, t.failed])).toEqual([
      ["plan", NOT_STARTED_TAKEN], ["plan the epic, then", null]]);
    expect(NOT_STARTED_TAKEN).toBe("the agent did not start; a later agent took over.");
  });

  test("the message its briefing carries is queued until the agent is up, then read", () => {
    const events = [created(), message("plan the epic", 1), stage("waiting", 0)];
    expect(project(events, "scheduled").turns.find((t) => t.kind === "human")).toMatchObject({ deliveredAt: null, failed: null });
    const up = project([...events, ev("agent.session.started", {}, 9_000)], "running");
    expect(up.turns.find((t) => t.kind === "human")).toMatchObject({ deliveredAt: at(9_000) });
  });

  test("an older lux names no stage: starting, with no stage", () => {
    const c = project([created()], "scheduled");
    expect(c.start).toMatchObject({ kind: "start", stage: null, startedAt: at(0) });
  });

  test("the slow note waits for each stage's threshold: waiting 60s, image 120s", () => {
    const s = (st: "waiting" | "image"): StartState => ({ kind: "start", stage: st, stageSince: at(0), startedAt: at(0), done: [], luxReason: null, failed: null });
    expect(SLOW_AFTER_MS.waiting).toBe(60_000);
    expect(slowNote(s("waiting"), T0 + 59_000)).toBeNull();
    expect(slowNote(s("waiting"), T0 + 60_000)).toBe("No machine free yet: dude is waiting for one. Your message is kept; nothing is spent meanwhile.");
    expect(slowNote(s("image"), T0 + 119_000)).toBeNull();
    expect(slowNote(s("image"), T0 + 120_000)).toContain("A large image can take a few minutes");
  });
});

/** A Run whose ledger is the test's and whose detail is patched; lux's wait reason, when set. */
class StartClient extends FixtureClient {
  waitingReason: string | null = null;
  constructor(private readonly patch: Partial<RunDetail>, public ledger: PersistedEvent[]) {
    super("a");
  }
  override async getRun(id: string): Promise<RunDetail> {
    return { ...(await super.getRun(id)), ...this.patch };
  }
  override async runServers(runId = RUN_ID): Promise<TaskServers> {
    const s = await super.runServers();
    return { ...s, run: s.run ? { ...s.run, id: runId, waitingReason: this.waitingReason } : s.run };
  }
  protected override ledgerFor(q: LedgerQuery): PersistedEvent[] {
    if (q.runId !== RUN_ID) return super.ledgerFor(q);
    return this.ledger.filter((e) => e.cursor > (q.after ?? 0));
  }
}

async function runPage(status: RunStatus, ledger: PersistedEvent[], chat = false, stopped?: Parameters<typeof RunScreen>[0]["stopped"],
  retry?: () => Promise<unknown>, waitingReason: string | null = null) {
  const client = new StartClient({ status, role: chat ? "conductor" : "implementer", phase: chat ? null : "implement" }, ledger);
  client.waitingReason = waitingReason;
  const { container, unmount } = await mount(<RunScreen runId={RUN_ID} onBack={() => {}} client={client} stopped={stopped}
    {...(chat ? { chat: { head: null, send: () => Promise.resolve(), answer: () => Promise.resolve(), briefedWith: [], retry } } : {})} />);
  mounted.push(unmount);
  await until(() => container.querySelector(chat ? "[data-testid=chat-screen]" : "[data-testid=run-screen]"), "the page");
  return container;
}

describe("a task's Run page", () => {
  const recent = (ms: number) => Date.now() - ms;
  const live = () => {
    const t = recent(20_000);
    const iso = (d: number) => new Date(t + d).toISOString();
    return [
      { ...created(), occurredAt: iso(0) },
      { ...stage("waiting", 0), payload: { stage: "waiting", since: iso(0), epoch: 1 } },
      { ...stage("container", 0), payload: { stage: "container", since: iso(15_000), epoch: 1 } },
      { ...ev("run.steered", { text: "Use the jobs table.", directiveId: "dir_1" }, 0, { type: "human", id: "per_ana" }), occurredAt: iso(16_000) },
    ];
  };

  test("the chip is the header's statusNote, bare; the row says the stage; the steer and the composer say when it is read", async () => {
    const page = await runPage("scheduled", live());
    const chip = await until(() => page.querySelector("[data-testid=start-chip]"), "the chip");
    expect(chip.querySelector("[data-status]")).toBeNull();
    expect(chip.textContent).toContain("Starting the container");
    const row = page.querySelector("[data-testid=start-progress]")!;
    expect(row.getAttribute("data-stage")).toBe("container");
    expect(row.querySelector("[role=status]")!.textContent).toBe("Starting: Starting the container.");
    expect(page.querySelector("[data-testid=human-turn]")!.textContent).toContain(READ_WHEN_STARTED);
    expect(page.textContent).toContain(READ_WHEN_STARTED.replace(/\.$/, ""));
    expect(page.textContent).not.toContain("Lands when the agent starts");
  });

  test("waiting for a host that can run containers: the pinned callout says it once; the row keeps its stage and clock, no slow note", async () => {
    const NESTED = "waiting for capacity: 1 host in its pool does not support nested containers";
    const t = Date.now() - 100_000;
    const ledger = [{ ...created(), occurredAt: new Date(t).toISOString() },
      { ...stage("waiting", 0), payload: { stage: "waiting", since: new Date(t).toISOString(), epoch: 1 } }];
    const page = await runPage("scheduled", ledger, false, undefined, undefined, NESTED);
    await until(() => page.querySelector("[data-testid=waiting-for-host]"), "the pinned callout");
    const row = page.querySelector("[data-testid=start-progress]")!;
    expect(row.getAttribute("data-stage")).toBe("waiting");
    expect(row.querySelector("[role=timer]")).not.toBeNull();
    expect(page.querySelector("[data-testid=start-slow]")?.textContent ?? null).toBeNull();
  });

  test("waiting for capacity of another kind: no pinned callout; the row's slow note says it with lux's reason", async () => {
    const t = Date.now() - 100_000;
    const ledger = [{ ...created(), occurredAt: new Date(t).toISOString() },
      { ...stage("waiting", 0), payload: { stage: "waiting", since: new Date(t).toISOString(), epoch: 1 } }];
    const page = await runPage("scheduled", ledger, false, undefined, undefined, "waiting for capacity: 2 hosts lack cpus");
    const slow = await until(() => page.querySelector("[data-testid=start-slow]"), "the slow note");
    expect(slow.textContent).toContain("lux: waiting for capacity: 2 hosts lack cpus");
    expect(page.querySelector("[data-testid=waiting-for-host]")?.textContent ?? null).toBeNull();
  });

  test("the Run's own status: Scheduled while it waits for a machine, Starting once placed (any later stage), as lux moves it", async () => {
    const t = Date.now() - 20_000;
    const iso = (d: number) => new Date(t + d).toISOString();
    const waiting = [{ ...created(), occurredAt: iso(0) }, { ...stage("waiting", 0), payload: { stage: "waiting", since: iso(0), epoch: 1 } }];
    const page = await runPage("scheduled", waiting);
    const status = () => page.querySelector("[data-testid=start-chip]")!.closest("header")!.querySelector("[data-status]")!;
    await until(() => page.querySelector("[data-testid=start-chip]"), "the chip");
    expect(status().getAttribute("data-status")).toBe("scheduled");
    const placed = { ...stage("image", 0), payload: { stage: "image", since: iso(5_000), epoch: 1 } };
    await act(async () => emit(placed));
    await until(() => (status().getAttribute("data-status") === "starting" ? true : null), "Starting once placed");
    expect(status().textContent).toContain("Starting");
  });

  test("the task's Runs read the same from the API's startStage", () => {
    const run = { status: "scheduled" as const, startStage: { stage: "container", since: null, epoch: 1 } };
    expect(runStatusShown(run)).toBe("starting");
    expect(runStatusShown({ ...run, startStage: { stage: "waiting", since: null, epoch: 1 } })).toBe("scheduled");
    expect(runStatusShown({ ...run, startStage: null })).toBe("scheduled");
  });

  /** The task page on `tab`, with a conductor and a phase Run it started that lux placed (stage container), not yet running. */
  async function placedOnTaskPage(tab: "chat" | "sessions" | "overview") {
    const client = new FixtureClient("a");
    const placed = run({ id: "run_placed", phase: "implement", role: "implementer", status: "scheduled", startedAt: null,
      conductorRunId: "run_conductor", startStage: { stage: "container", since: null, epoch: 1 } });
    const getTask = client.getTask.bind(client);
    client.getTask = async (id) => ({ ...(await getTask(id)), decider: "conductor",
      runs: [run({ id: "run_conductor", role: "conductor", phase: null, status: "running" }), placed] });
    const getRun = client.getRun.bind(client);
    client.getRun = async (id) => id === "run_conductor"
      ? { ...(await getRun(RUN_ID)), id, role: "conductor", phase: null, status: "running", endedAt: null }
      : getRun(id);
    const { container, unmount } = await mount(
      <TooltipProvider><ToastProvider><PeopleProvider client={client}>
        <TaskScreen client={client} taskId={TASK_ID} tab={tab} onOpenRun={() => {}} onBack={() => {}} />
      </PeopleProvider></ToastProvider></TooltipProvider>,
    );
    mounted.push(unmount);
    return container;
  }

  test("the conductor's Chat line for a Run lux placed reads Starting", async () => {
    const page = await placedOnTaskPage("chat");
    const line = await until(() => page.querySelector<HTMLElement>("[data-testid=chat-run][data-run=run_placed]"), "the Chat Run line");
    expect(line.getAttribute("data-status")).toBe("starting");
  });

  test("failed in a stage: the row is the danger Callout naming the stage and lux's reason", async () => {
    const ledger = [created(), stage("waiting", 0), stage("image", 5_000),
      ev("run.failed", { status: "failed", error: "x", stage: "image", reason: "pull failed: manifest unknown" }, 50_000)];
    const page = await runPage("failed", ledger);
    const failed = await until(() => page.querySelector("[data-testid=start-failed]"), "the failure");
    expect(failed.textContent).toBe("Did not start: failed while getting the image ready: pull failed: manifest unknown. Nothing was spent on the model.");
    expect(failed.querySelector("code")!.textContent).toBe("pull failed: manifest unknown");
    // The header's statusNote names where it failed, bare beside the Run's own Failed status.
    const chip = page.querySelector("[data-testid=start-chip]")!;
    expect(chip.textContent).toBe("Failed while getting the image ready");
  });

  test("failed before lux named a stage: the Run's header has no stage words to add, and no stale wait badge", async () => {
    const ledger = [created(), ev("run.failed", { status: "failed", error: "No model yet." }, 1_000)];
    expect(project(ledger, "failed").startFailed).toMatchObject({ stage: null, failed: { source: "dude" } });
    const page = await runPage("failed", ledger);
    await until(() => page.querySelector("[data-testid=start-failed]"), "the failure");
    expect(page.querySelector("[data-testid=start-chip]")?.textContent ?? null).toBeNull();
  });

  test("a tier with no model fails before lux: its useful error is projected and shown, and Try again still opens restart", async () => {
    const error = "The Implementer runs on Coder, which names no model yet. An admin sets it in Models.";
    for (const reason of [undefined, "", "   "]) {
      let picked: string | null = null;
      const ledger = [created(), ev("run.failed", { status: "failed", error, ...(reason === undefined ? {} : { reason }) }, 1_000)];
      const ended = project(ledger, "failed").turns.find((t) => t.kind === "ended")!;
      expect(ended.kind === "ended" && ended.start).toMatchObject({ stage: null, failed: { reason: error, source: "dude" } });
      const page = await runPage("failed", ledger, false, { onPickUp: (a) => { picked = a; }, keptUntil: null });
      const failed = await until(() => page.querySelector("[data-testid=start-failed]"), "the failure before lux");
      // dude's own sentence, in body text: no "failed:" with no stage, no doubled full stop.
      expect(failed.textContent).toBe(`Did not start: ${error} Nothing was spent on the model.`);
      expect(failed.querySelector("code")?.textContent ?? null).toBeNull();
      const retry = page.querySelector<HTMLButtonElement>("[data-testid=start-retry]")!;
      await act(async () => retry.click());
      expect(picked).toBe("retry");
    }
  });

  test("Try again on a phase that did not start opens the task's restart dialog", async () => {
    let picked: string | null = null;
    const ledger = [created(), stage("waiting", 0), stage("image", 5_000),
      ev("run.failed", { status: "failed", error: "x", stage: "image", reason: "pull failed: manifest unknown" }, 50_000)];
    const page = await runPage("failed", ledger, false, { onPickUp: (a) => { picked = a; }, keptUntil: null });
    const retry = await until(() => page.querySelector<HTMLButtonElement>("[data-testid=start-retry]"), "Try again");
    await act(async () => retry.click());
    expect(picked).toBe("retry");
  });
});

describe("a conductor's Chat", () => {
  test("the same row shows as it wakes", async () => {
    const t = Date.now() - 5_000;
    const ledger = [{ ...created(), occurredAt: new Date(t).toISOString() },
      { ...stage("waiting", 0), payload: { stage: "waiting", since: new Date(t).toISOString(), epoch: 1 } }];
    const page = await runPage("scheduled", ledger, true);
    const row = await until(() => page.querySelector("[data-testid=start-progress]"), "the row in Chat");
    expect(row.querySelector("[role=status]")!.textContent).toBe("Starting: Waiting for a machine.");
    expect(page.querySelector("[data-testid=composer-start-hint]")!.textContent).toBe(`· ${READ_WHEN_STARTED}`);
  });

  test("a resumed conductor reads as resuming, and a message waits for it to be back", async () => {
    const ledger = [created(), stage("waiting", 0), ev("agent.message", { text: "Ready." }, 1_000), ev("run.parked", { reason: "conductor" }, 2_000),
      ev("run.unparked", { reason: "conductor", epoch: 2 }, 3_000), stage("waiting", 3_000, 2),
      ev("chat.message", { text: "status?", directiveId: "dir_9" }, 3_500, { type: "person", id: "per_marcio" } as PersistedEvent["actor"])];
    const page = await runPage("running", ledger, true);
    const row = await until(() => page.querySelector("[data-testid=start-progress]"), "the row");
    expect(row.querySelector("[role=status]")!.textContent).toBe("Resuming: Waiting for a machine.");
    expect(page.querySelector("[data-testid=human-turn]")!.textContent).toContain(READ_WHEN_BACK);
  });

  test("once it is up the row goes", async () => {
    const ledger = [created(), stage("waiting", 0), ev("agent.message", { text: "Ready." }, 1_000)];
    const page = await runPage("running", ledger, true);
    await until(() => page.querySelector("[data-testid=conductor-turn]"), "its first words");
    expect(page.querySelector("[data-testid=start-progress]")).toBeNull();
  });

  const failedStart = () => [created(), message("what changed?", 1), stage("waiting", 0), stage("container", 3_000),
    ev("run.failed", { status: "failed", error: "x", stage: "container", reason: "start-failed" }, 5_000)];

  test("a conductor whose start failed: Try again starts a fresh one", async () => {
    let retried = 0;
    const page = await runPage("failed", failedStart(), true, undefined, () => { retried += 1; return Promise.resolve(); });
    const failed = await until(() => page.querySelector("[data-testid=start-failed]"), "the failure");
    expect(failed.textContent).toContain("Did not start: failed while starting the container: start-failed.");
    expect(page.querySelector("[data-testid=start-wakes-next]")).toBeNull();
    await act(async () => page.querySelector<HTMLButtonElement>("[data-testid=start-retry]")!.click());
    expect(retried).toBe(1);
  });

  test("a conductor dude woke whose start failed: no Try again; dude wakes a new one", async () => {
    let retried = 0;
    const woken = [created(), ev("conductor.briefed", { text: "Conductor, dude woke you; one thing: …" }, 1),
      ev("conductor.woken", { text: "dude woke you; one thing: …", reasons: ["decision"], started: true }, 2),
      stage("waiting", 3), stage("container", 3_000),
      ev("run.failed", { status: "failed", error: "x", stage: "container", reason: "start-failed" }, 5_000)];
    const page = await runPage("failed", woken, true, undefined, () => { retried += 1; return Promise.resolve(); });
    await until(() => page.querySelector("[data-testid=start-failed]"), "the failure");
    expect(page.querySelector("[data-testid=start-retry]")).toBeNull();
    expect(page.querySelector("[data-testid=start-wakes-next]")!.textContent).toBe("dude will wake a new conductor.");
    expect(page.querySelector("[data-testid=start-retry-taken]")).toBeNull();
    expect(retried).toBe(0);
  });

  test("a conductor dude woke, moved during its first start and then failed before it ran: no Try again; dude wakes a new one", async () => {
    let retried = 0;
    const moved = [created(), ev("conductor.briefed", { text: "Conductor, dude woke you; one thing: …" }, 1),
      ev("conductor.woken", { text: "dude woke you; one thing: …", reasons: ["decision"], started: true }, 2),
      stage("waiting", 3), stage("image", 1_000), stage("park", 2_000, 1, "preempt"), stage("waiting", 3_000, 2),
      ev("run.failed", { status: "failed", error: "x", stage: "waiting", reason: "no capacity" }, 9_000)];
    const ended = project(moved, "failed").turns.find((t) => t.kind === "ended")!;
    expect(ended.kind === "ended" && [ended.start?.kind, ended.woken]).toEqual(["move", true]);
    const page = await runPage("failed", moved, true, undefined, () => { retried += 1; return Promise.resolve(); });
    await until(() => page.querySelector("[data-testid=start-failed]"), "the failure");
    expect(page.querySelector("[data-testid=start-retry]")).toBeNull();
    expect(page.querySelector("[data-testid=start-wakes-next]")!.textContent).toBe("dude will wake a new conductor.");
    expect(retried).toBe(0);
  });

  test("a conductor dude woke that ran, parked, then failed resuming for a person's message: Try again, no wakes-next line", async () => {
    let retried = 0;
    const resumed = [created(), ev("conductor.briefed", { text: "Conductor, dude woke you; one thing: …" }, 1),
      ev("conductor.woken", { text: "dude woke you; one thing: …", reasons: ["decision"], started: true }, 2),
      stage("waiting", 3), ev("agent.message", { text: "Handled the decision." }, 1_000),
      ev("run.parked", { reason: "conductor" }, 2_000),
      ev("chat.message", { text: "and now?", directiveId: "dir_9" }, 3_000, { type: "person", id: "per_marcio" } as PersistedEvent["actor"]),
      ev("run.unparked", { reason: "conductor", epoch: 2 }, 3_100), stage("waiting", 3_100, 2),
      ev("run.failed", { status: "failed", error: "terminated", stage: "waiting", reason: "terminated" }, 9_000)];
    const ended = project(resumed, "failed").turns.find((t) => t.kind === "ended")!;
    expect(ended.kind === "ended" && [ended.start?.kind, ended.woken]).toEqual(["resume", undefined]);
    const page = await runPage("failed", resumed, true, undefined, () => { retried += 1; return Promise.resolve(); });
    const failed = await until(() => page.querySelector("[data-testid=start-failed]"), "the failure");
    expect(failed.textContent).toContain("Did not resume");
    expect(page.querySelector("[data-testid=start-wakes-next]")).toBeNull();
    await act(async () => page.querySelector<HTMLButtonElement>("[data-testid=start-retry]")!.click());
    expect(retried).toBe(1);
  });

  test("Try again after a new conductor already took the messages: says so, and is not offered again", async () => {
    const page = await runPage("failed", failedStart(), true, undefined,
      () => Promise.reject(new ApiError(409, "retry_taken", "a new agent already took the later messages")));
    await act(async () => (await until(() => page.querySelector<HTMLButtonElement>("[data-testid=start-retry]"), "Try again")).click());
    const said = await until(() => page.querySelector("[data-testid=start-retry-taken]"), "the line");
    expect(said.textContent).toBe("A new agent already took the later messages.");
    expect(page.querySelector("[data-testid=start-retry]")).toBeNull();
  });
});

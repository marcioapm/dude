/**
 * A task's conductors beside its attempts: each conductor is the attempt
 * recorded on its Run. A Stop and start begins attempt N+1 with only its
 * conductor; that attempt is current, listed in the picker, and its
 * conductor's session opens on it. An earlier attempt's conductor is
 * read-only with the rest of that attempt. Chat's URL names no attempt.
 * Mounted in happy-dom on the fixture client's started-over task
 * (`dude.fixtures.run=restarted`).
 */

import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { act, click, mount, sessionOptions, settle, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { RESTARTED_RUNS, TASK_ID } from "../src/fixtures/data.ts";
import type { PersistedEvent, Run } from "@dude/domain";
import { DECISION_POINT_LABEL } from "@dude/domain";
import { App } from "../src/App.tsx";
import { PeopleProvider } from "../src/people.tsx";
import { TaskScreen } from "../src/screens/TaskScreen.tsx";
import { attemptMark, attemptStarts, attemptStatus, attemptsOf, eventAttempts, setAsideOf } from "../src/attempts.ts";
import { formatPlace, inTree, parsePlace } from "../src/place.ts";

let mounted: Array<() => Promise<void>> = [];
beforeEach(() => localStorage.clear());
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  localStorage.clear();
  window.history.replaceState(null, "", " ");
});

// One instant for the whole file: two `at(n)` taken apart compare equal.
const NOW = Date.now();
const at = (min: number) => new Date(NOW - min * 60_000).toISOString();
const COND = "run_cond";
/** The task's conductor, recorded on `attempt`, created and ended so many minutes ago. */
const conductor = (attempt: number, more: Partial<Run> = {}): Run => ({
  ...RESTARTED_RUNS.find((r) => r.id === "run_a2_fix")!, id: COND, attempt, phase: null, role: "conductor", category: null,
  status: "running", createdAt: at(30), startedAt: at(30), endedAt: null, ...more,
});

type Task = Awaited<ReturnType<FixtureClient["getTask"]>>;
/** The started-over task with `runs` added, wherever the page reads it. */
function restartedWith(...runs: Run[]) {
  localStorage.setItem("dude.fixtures.run", "restarted");
  const client = new FixtureClient("a");
  const change = (t: Task): Task => ({ ...t, runs: [...runs, ...t.runs] });
  const getTask = client.getTask.bind(client);
  client.getTask = async (id) => change(await getTask(id));
  const getRun = client.getRun.bind(client);
  client.getRun = async (id) => {
    const extra = runs.find((r) => r.id === id);
    if (!extra) return getRun(id);
    return { ...(await getRun("run_a2_fix")), ...extra } as Awaited<ReturnType<typeof getRun>>;
  };
  return client;
}

async function taskPage(client: FixtureClient, props: Partial<Parameters<typeof TaskScreen>[0]> = {}) {
  const { container, unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <PeopleProvider client={client}>
          <TaskScreen client={client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} {...props} />
        </PeopleProvider>
      </ToastProvider>
    </TooltipProvider>,
  );
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=task-screen]"), "the task page");
  return container;
}

const shown = (page: HTMLElement) => page.querySelector("[data-testid=task-screen]")?.getAttribute("data-attempt");
const count = (root: ParentNode, sel: string) => root.querySelectorAll(sel).length;
const picker = (page: HTMLElement) => page.querySelector<HTMLElement>("[data-testid=attempt-picker]");
const tabCount = (page: HTMLElement, name: string) =>
  [...page.querySelectorAll<HTMLElement>("[role=tab]")].find((t) => t.textContent?.startsWith(name))?.textContent?.slice(name.length) ?? "";
const openTab = (page: HTMLElement, name: string) => act(async () => {
  const tab = [...page.querySelectorAll<HTMLElement>("[role=tab]")].find((t) => t.textContent?.startsWith(name))!;
  tab.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, button: 0 }));
});
async function pick(page: HTMLElement, n: number) {
  const trigger = await until(() => picker(page), "the attempt picker");
  await act(async () => void trigger.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
  await click(await until(() => document.querySelector<HTMLElement>(`[role=option][data-value="${n}"]`), `attempt ${n} in the picker`));
  await until(() => (shown(page) === String(n) ? true : null), `attempt ${n} shown`);
}

describe("a conductor's Run and the attempts", () => {
  test("a conductor alone on attempt 3 makes attempt 3, the current one", () => {
    expect(attemptsOf([...RESTARTED_RUNS, conductor(3)])).toEqual([3, 2, 1]);
  });

  test("a phase Run, not the conductor that steered it, is where an attempt stopped", () => {
    // Attempt 1's fixer was aborted 97 minutes ago; its conductor failed after that.
    const runs = [...RESTARTED_RUNS, conductor(1, { status: "failed", error: "host lost", createdAt: at(100), endedAt: at(96) })];
    const task = { status: "running", runs } as unknown as Task;
    expect(attemptStatus(task, 1, 2)).toBe("aborted");
    const people = { you: null, me: null, all: [], byId: new Map(), names: new Map(), refresh: async () => people, seen: () => false } as never;
    expect(setAsideOf(1, runs, [], people).how).toBe("stopped at Fix, aborted");
  });

  test("an attempt begins with its conductor when that is its first Run", () => {
    const start = at(5);
    const starts = attemptStarts([...RESTARTED_RUNS, conductor(3, { createdAt: start })]);
    expect(starts.get(3)).toBe(start);
  });

  test("what happened on an ended conductor is its attempt's; on a live one, the current attempt's", () => {
    const of = eventAttempts([...RESTARTED_RUNS, conductor(1, { status: "aborted", endedAt: at(96) })], [], 2);
    expect(of({ eventType: "chat.message", runId: COND, occurredAt: at(5), payload: {} } as unknown as PersistedEvent)).toBe(1);
    expect(of({ eventType: "run.steered", runId: "run_a1_fix", occurredAt: at(5), payload: {} } as unknown as PersistedEvent)).toBe(1);
    const live = eventAttempts([...RESTARTED_RUNS, conductor(1)], [], 2);
    expect(live({ eventType: "chat.message", runId: COND, occurredAt: at(5), payload: {} } as unknown as PersistedEvent)).toBe(2);
  });
});

describe("the header's mark of a task its conductor decides", () => {
  // Talk it through: running from the start, waiting on the conductor to start building, no phase yet.
  const talked = (...runs: Run[]) => ({ status: "running", decider: "conductor", awaitingDecision: { point: "start" }, runs } as unknown as Task);

  test("waiting on a parked conductor, nothing working: paused, waiting on Chat", () => {
    expect(attemptMark(talked(conductor(1, { status: "paused", dudePause: "conductor" })), 1, 1))
      .toEqual({ status: "paused", label: "Waiting on Chat" });
    // Its conductor ended: the next message starts another.
    expect(attemptMark(talked(conductor(1, { status: "failed" })), 1, 1).status).toBe("paused");
    // A phase it started that ended, and is not at work.
    const implemented = { ...RESTARTED_RUNS.find((r) => r.id === "run_a2_fix")!, attempt: 1, phase: "implement", role: "implementer",
      status: "completed" } as Run;
    expect(attemptMark(talked(conductor(1, { status: "paused", dudePause: "conductor" }), implemented), 1, 1).status).toBe("paused");
  });

  test("a conductor or a phase at work keeps it running", () => {
    expect(attemptMark(talked(conductor(1)), 1, 1)).toEqual({ status: "running" });
    const fixing = { ...RESTARTED_RUNS.find((r) => r.id === "run_a2_fix")!, attempt: 1, status: "running" } as Run;
    expect(attemptMark(talked(conductor(1, { status: "paused", dudePause: "conductor" }), fixing), 1, 1)).toEqual({ status: "running" });
    const reviewing = { ...fixing, phase: "review", role: "reviewer" } as Run;
    expect(attemptMark(talked(conductor(1, { status: "paused", dudePause: "conductor" }), reviewing), 1, 1))
      .toEqual({ status: "running" });
  });

  test("a conductor or a phase paused for a machine move keeps it running: dude resumes it with no message", () => {
    expect(attemptMark(talked(conductor(1, { status: "paused", dudePause: "machine" })), 1, 1)).toEqual({ status: "running" });
    const moving = { ...RESTARTED_RUNS.find((r) => r.id === "run_a2_fix")!, attempt: 1, status: "paused", dudePause: "machine" } as Run;
    expect(attemptMark(talked(conductor(1, { status: "paused", dudePause: "conductor" }), moving), 1, 1)).toEqual({ status: "running" });
    // Paused by a person, it waits on Chat as before.
    expect(attemptMark(talked(conductor(1, { status: "paused", dudePause: "person" })), 1, 1).status).toBe("paused");
  });

  test("a historical attempt keeps its original status while the current one waits on Chat", () => {
    const task = talked(...RESTARTED_RUNS.filter((r) => r.attempt === 1), conductor(2, { status: "paused", dudePause: "conductor" }));
    expect(attemptMark(task, 2, 2)).toEqual({ status: "paused", label: "Waiting on Chat" });
    expect(attemptMark(task, 1, 2)).toEqual({ status: "aborted" });
  });

  test("Deliver's task, or one waiting on no decision, keeps its status", () => {
    const parked = conductor(1, { status: "paused", dudePause: "conductor" });
    expect(attemptMark({ ...talked(parked), decider: "policy" }, 1, 1)).toEqual({ status: "running" });
    expect(attemptMark({ ...talked(parked), awaitingDecision: null }, 1, 1)).toEqual({ status: "running" });
    expect(attemptMark({ ...talked(parked), status: "review" }, 1, 1)).toEqual({ status: "review" });
  });
});

describe("the task page with a conductor", () => {
  test("the current header renders Waiting on Chat, not Running, when its conductor is parked", async () => {
    const client = restartedWith(conductor(2, { status: "paused", dudePause: "conductor" }));
    const getTask = client.getTask.bind(client);
    client.getTask = async (id) => {
      const task = await getTask(id);
      return { ...task, status: "running", decider: "conductor", awaitingDecision: { point: "start" },
        runs: task.runs.filter((r) => r.role === "conductor" || r.attempt === 1) } as Task;
    };
    const page = await taskPage(client);
    const status = await until(() => page.querySelector<HTMLElement>("[data-testid=header-status]"), "the header status");
    expect(status.getAttribute("data-status")).toBe("paused");
    expect(status.textContent).toBe("Waiting on Chat");
  });

  /**
   * Stop and start attempt 2 under the conductor: attempt 1 ended (its
   * phase Runs and its conductor aborted), and attempt 2 has only the new
   * conductor dude started for it, no phase Run yet.
   */
  function stoppedAndStarted(olds: Run[] = [conductor(1, { id: OLD, status: "aborted", createdAt: at(230), endedAt: at(96) })]) {
    localStorage.setItem("dude.fixtures.run", "restarted");
    const client = new FixtureClient("a");
    const fresh = conductor(2, { createdAt: at(5), startedAt: at(5) });
    const runs = [fresh, ...olds, ...RESTARTED_RUNS.filter((r) => r.attempt === 1)];
    const getTask = client.getTask.bind(client);
    client.getTask = async (id) => ({ ...(await getTask(id)), status: "running", decider: "conductor", runs } as Task);
    const getRun = client.getRun.bind(client);
    client.getRun = async (id) => {
      const extra = runs.find((r) => r.id === id && r.role === "conductor");
      return extra ? { ...(await getRun("run_a1_fix")), ...extra } as Awaited<ReturnType<typeof getRun>> : getRun(id);
    };
    return client;
  }
  const OLD = "run_cond_old";

  test("after Stop and start, a conductor alone on attempt 2 makes it current: header, picker and Stop name it", async () => {
    const page = await taskPage(stoppedAndStarted());
    await until(() => (shown(page) === "2" ? true : null), "attempt 2, the current one");
    expect(page.querySelector("[data-testid=earlier-bar]")).toBeNull();
    const trigger = await until(() => picker(page), "the attempt picker");
    expect(trigger.textContent).toContain("Attempt 2");
    await act(async () => void trigger.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
    await until(() => document.querySelector('[role=option][data-value="1"]'), "the options");
    expect([...document.querySelectorAll("[role=option]")].map((o) => o.getAttribute("data-value"))).toEqual(["2", "1"]);
    await act(async () => void document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    await act(async () => page.querySelector<HTMLElement>('[data-testid="task-stop"]')!.click());
    await until(() => document.querySelector('[data-testid="stop-dialog"]'), "the Stop dialog");
    expect([...document.querySelectorAll<HTMLElement>('[data-testid="stop-dialog"] [role="radio"]')].map((r) => r.textContent ?? "")
      .some((t) => t.startsWith("Stop and start attempt 3"))).toBe(true);
  });

  test("after Stop and start, attempt 1 is selectable and read-only", async () => {
    const page = await taskPage(stoppedAndStarted(), { tab: "findings" });
    await pick(page, 1);
    const bar = await until(() => page.querySelector<HTMLElement>("[data-testid=earlier-bar]"), "the earlier-attempt bar");
    expect(bar.textContent).toContain("nothing in it can be merged, resumed or steered.");
    expect(page.querySelector("[data-testid=header-status]")?.getAttribute("data-status")).toBe("aborted");
  });

  test("the old conductor's session opens on attempt 1, read-only, not on the newest attempt", async () => {
    const page = await taskPage(stoppedAndStarted(), { runId: OLD });
    await until(() => (shown(page) === "1" ? true : null), "attempt 1, the old conductor's");
    await until(() => page.querySelector("[data-testid=earlier-bar]"), "the earlier-attempt bar");
    expect(count(page, "[data-testid=abort]")).toBe(0);
    expect(count(page, "textarea")).toBe(0);
  });

  test("the new conductor's session opens on attempt 2 with its composer", async () => {
    const page = await taskPage(stoppedAndStarted(), { runId: COND });
    await until(() => page.querySelector("[data-testid=abort]"), "Abort on the live conductor");
    expect(shown(page)).toBe("2");
    expect(count(page, "[data-testid=earlier-bar]")).toBe(0);
    expect(count(page, "textarea")).toBe(1);
  });

  test("a conductor that failed after attempt 1's fixer is not where attempt 1 stopped", async () => {
    const page = await taskPage(restartedWith(conductor(1, { status: "failed", error: "host lost", createdAt: at(100), endedAt: at(96) })));
    await pick(page, 1);
    expect(page.querySelector("[data-testid=header-status]")?.getAttribute("data-status")).toBe("aborted");
    const bar = await until(() => page.querySelector<HTMLElement>("[data-testid=earlier-bar]"), "the earlier-attempt bar");
    expect(bar.textContent).toContain("It had stopped at Fix, aborted by Ana.");
  });

  const ev = (id: string, eventType: string, min: number, payload: Record<string, unknown>, runId: string | null, actor: PersistedEvent["actor"]) =>
    ({ eventId: id, cursor: 50_000 + 1000 - min, eventType, occurredAt: at(min), organizationId: "org", projectId: "prj", taskId: TASK_ID,
      runId, sessionId: null, workflowRunId: null, actor, source: "orchestrator", correlationId: null, causationId: null, payload }) as PersistedEvent;
  const ana = { type: "human", id: "per_ana" } as const;
  const follows = (a: Node, b: Node) => (a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0;

  test("Chat keeps attempt 1's decision line inside attempt 1's conductor, in time order, before its end", async () => {
    const client = stoppedAndStarted();
    const old = [
      ev("evt_old_msg", "chat.message", 210, { text: "Plan the greeting." }, OLD, ana),
      ev("evt_old_reply", "agent.message", 150, { text: "Planned: one file." }, OLD, { type: "agent", id: OLD }),
      ev("evt_old_abort", "run.aborted", 96, { reason: "Start fresh.", by: "stop" }, OLD, ana),
    ];
    const waited = ev("evt_wait", "conductor.decision_awaited", 200, { point: "start" }, null, { type: "system", id: "dude" });
    const events = client.events.bind(client);
    client.events = async (params) => {
      if (params.runId === OLD) return { events: old.filter((e) => e.cursor > (params.after ?? 0)), nextCursor: old.at(-1)!.cursor };
      const r = await events(params);
      return params.runId ? r : { ...r, events: [...r.events, waited] };
    };
    const page = await taskPage(client, { tab: "chat" });
    const earlier = await until(() => page.querySelector<HTMLElement>(`[data-testid=earlier-conductor][data-run=${OLD}]`), "attempt 1's conductor");
    const decision = await until(() => earlier.querySelector<HTMLElement>("[data-testid=chat-decision]"), "attempt 1's decision line inside it");
    const ended = earlier.querySelector<HTMLElement>("[data-testid=earlier-conductor-ended]")!;
    const said = (text: string) => [...earlier.querySelectorAll<HTMLElement>("*")].find((n) => n.children.length === 0 && n.textContent?.includes(text))!;
    expect([follows(said("Plan the greeting."), decision), follows(decision, said("Planned: one file.")), follows(decision, ended)])
      .toEqual([true, true, true]);
  });

  test("Chat with two conductors in attempt 1 (Try again) puts each line in the conductor of its time, and one after its end after its ended line", async () => {
    const A = "run_cond_a";
    const B = "run_cond_b";
    const client = stoppedAndStarted([
      conductor(1, { id: A, status: "failed", error: "host lost", createdAt: at(220), startedAt: at(220), endedAt: at(180) }),
      conductor(1, { id: B, status: "aborted", createdAt: at(170), startedAt: at(170), endedAt: at(96) }),
    ]);
    const ledgers: Record<string, PersistedEvent[]> = {
      [A]: [ev("evt_a_msg", "chat.message", 215, { text: "Plan the greeting." }, A, ana),
        ev("evt_a_fail", "run.failed", 180, { error: "host lost" }, A, { type: "system", id: "dude" })],
      [B]: [ev("evt_b_msg", "chat.message", 165, { text: "Carry on." }, B, ana),
        ev("evt_b_abort", "run.aborted", 96, { reason: "Start fresh.", by: "stop" }, B, ana)],
    };
    const dude = { type: "system", id: "dude" } as const;
    // At 175 only A had started; at 150 B had; at 90 B had ended.
    const waited = [ev("evt_wait_a", "conductor.decision_awaited", 175, { point: "start" }, null, dude),
      ev("evt_wait_b", "conductor.decision_awaited", 150, { point: "after_implement" }, null, dude),
      ev("evt_wait_late", "conductor.decision_awaited", 90, { point: "after_review" }, null, dude)];
    const events = client.events.bind(client);
    client.events = async (params) => {
      const own = params.runId ? ledgers[params.runId] : undefined;
      if (own) return { events: own.filter((e) => e.cursor > (params.after ?? 0)), nextCursor: own.at(-1)!.cursor };
      const r = await events(params);
      return params.runId ? r : { ...r, events: [...r.events, ...waited] };
    };
    const page = await taskPage(client, { tab: "chat" });
    const inA = await until(() => page.querySelector<HTMLElement>(`[data-testid=earlier-conductor][data-run=${A}]`), "the first conductor");
    const inB = await until(() => page.querySelector<HTMLElement>(`[data-testid=earlier-conductor][data-run=${B}]`), "its retry");
    await until(() => inA.querySelector("[data-testid=earlier-conductor-ended]") && inB.querySelector("[data-testid=earlier-conductor-ended]"), "both read");
    const decisions = (root: HTMLElement) => [...root.querySelectorAll<HTMLElement>("[data-testid=chat-decision]")].map((d) => d.textContent ?? "");
    const point = (label: string) => `Waiting on the conductor: ${label}.`;
    expect(decisions(inA).map((t) => t.includes(point(DECISION_POINT_LABEL.start)))).toEqual([true]);
    expect(decisions(inB).map((t) => [t.includes(point(DECISION_POINT_LABEL.after_implement)), t.includes(point(DECISION_POINT_LABEL.after_review))]))
      .toEqual([[true, false], [false, true]]);
    const late = [...inB.querySelectorAll<HTMLElement>("[data-testid=chat-decision]")].at(-1)!;
    const lateEnded = inB.querySelector<HTMLElement>("[data-testid=earlier-conductor-ended]")!;
    const during = inB.querySelector<HTMLElement>("[data-testid=chat-decision]")!;
    expect([follows(lateEnded, late), follows(during, lateEnded)]).toEqual([true, true]);
  });

  /** Attempt 1's conductor whose ledger read is `read`, with attempt 1's decision line in the task's ledger. */
  function oldLedgerRead(read: () => Promise<never>) {
    const client = stoppedAndStarted();
    const waited = ev("evt_wait", "conductor.decision_awaited", 200, { point: "start" }, null, { type: "system", id: "dude" });
    const events = client.events.bind(client);
    client.events = async (params) => {
      if (params.runId === OLD) return read();
      const r = await events(params);
      return params.runId ? r : { ...r, events: [...r.events, waited] };
    };
    return client;
  }

  test("Chat keeps attempt 1's decision line when its conductor's ledger cannot be read", async () => {
    const page = await taskPage(oldLedgerRead(async () => { throw new Error("the ledger is unreachable"); }), { tab: "chat" });
    await until(() => page.querySelector("[data-testid=earlier-conductor-failed]"), "the failed read");
    await settle();
    expect(count(page, "[data-testid=chat-decision]")).toBe(1);
  });

  test("Chat shows attempt 1's decision line while its conductor's ledger is still being read", async () => {
    const page = await taskPage(oldLedgerRead(() => new Promise<never>(() => {})), { tab: "chat" });
    await until(() => (count(page, "[data-testid=chat-decision]") === 1 ? true : null), "attempt 1's decision line, before the read answers");
    expect(count(page, "[data-testid=earlier-conductor-ended]")).toBe(0);
  });

  test("an Activity line about attempt 1's conductor carries attempt 1", async () => {
    const client = restartedWith(conductor(1, { status: "aborted", createdAt: at(100), endedAt: at(40) }));
    const events = client.events.bind(client);
    client.events = async (params) => {
      const r = await events(params);
      if (params.runId) return r;
      const base = r.events.find((e) => e.eventType === "run.aborted")!;
      return { ...r, events: [...r.events, { ...base, eventId: "evt_cond_abort", cursor: 30_000, runId: COND, occurredAt: at(40),
        payload: { reason: "Not needed now." } }] };
    };
    const page = await taskPage(client, { tab: "activity" });
    const line = await until(() => [...page.querySelectorAll<HTMLElement>("[data-testid=activity-item]")]
      .find((l) => l.textContent?.includes("aborted the conductor")), "the conductor's line");
    expect(line.getAttribute("data-attempt")).toBe("1");
  });

  test("Chat's URL never names an attempt, even with attempt 1 picked", async () => {
    const said: Array<[string | undefined, number | undefined, boolean]> = [];
    const page = await taskPage(restartedWith(conductor(2)), { tab: "findings",
      onNavigate: (tab, attempt, replace) => void said.push([tab, attempt, replace]) });
    await pick(page, 1);
    expect(said.at(-1)).toEqual(["findings", 1, false]);
    await openTab(page, "Chat");
    await settle();
    expect(said.at(-1)).toEqual(["chat", undefined, true]);
    expect(formatPlace(inTree({ kind: "task", id: TASK_ID }, "chat", 1))).toBe(`#/task/${TASK_ID}/chat`);
    expect(parsePlace(`#/task/${TASK_ID}/chat?attempt=1`)).toEqual({ view: "tree", ref: { kind: "task", id: TASK_ID }, tab: "chat" });
  });
});

describe("a conductor's session, from its URL", () => {
  async function app(client: FixtureClient, hash: string) {
    window.history.replaceState(null, "", hash);
    const { container, unmount } = await mount(
      <TooltipProvider>
        <ToastProvider>
          <PeopleProvider client={client}>
            <App client={client} onSignOut={() => {}} onKeyRefused={() => {}} />
          </PeopleProvider>
        </ToastProvider>
      </TooltipProvider>,
    );
    mounted.push(unmount);
    return container;
  }
  const sessionsListed = async (page: HTMLElement) => {
    const list = await sessionOptions(page);
    return until(() => {
      const rows = [...list.querySelectorAll<HTMLElement>('[role="option"]')];
      return rows.length ? rows : null;
    }, "the sessions listed");
  };

  test("opens on the attempt its Run names", async () => {
    const page = await app(restartedWith(conductor(2)), `#/session/${COND}`);
    await until(() => page.querySelector("[data-testid=attempt-picker]"), "the task page with its picker");
    await until(() => page.querySelector("[data-testid=abort]"), "the conductor's session, live");
    expect(shown(page)).toBe("2");
    expect(count(page, "[data-testid=earlier-bar]")).toBe(0);
  });

  test("on attempt 1, Sessions lists attempt 1's Runs alone, not attempt 2's conductor", async () => {
    const page = await app(restartedWith(conductor(2)), `#/task/${TASK_ID}/sessions?attempt=1`);
    await until(() => (shown(page) === "1" ? true : null), "attempt 1 from the URL");
    const list = await sessionsListed(page);
    expect(list.some((r) => r.textContent?.includes("Conductor"))).toBe(false);
    expect(list).toHaveLength(4);
    expect(tabCount(page, "Sessions")).toBe("4");
    const tab = [...page.querySelectorAll<HTMLElement>("[role=tab]")].find((t) => t.textContent?.startsWith("Sessions"))!;
    await act(async () => tab.focus());
    const tip = await until(() => document.querySelector<HTMLElement>("[role=tooltip]"), "the Sessions tooltip");
    expect(tip.textContent).toBe("Attempt 1's sessions");
  });

  test("Sessions lists the conductor first even when it is older than every Run of the attempt", async () => {
    // Created before attempt 1 was set aside, older than all of attempt 2's.
    const page = await app(restartedWith(conductor(2, { createdAt: at(10_000), startedAt: at(10_000) })), `#/task/${TASK_ID}/sessions`);
    const list = await sessionsListed(page);
    expect(shown(page)).toBe("2");
    expect(list[0]!.textContent).toContain("Conductor");
    expect(list).toHaveLength(5);
  });

  test("Overview picked on a task with a conductor stays picked once the URL says the task alone", async () => {
    const page = await app(restartedWith(conductor(2)), `#/task/${TASK_ID}/findings`);
    await until(() => page.querySelector("[data-testid=attempt-picker]"), "the task page");
    await openTab(page, "Overview");
    await until(() => (window.location.hash === `#/task/${TASK_ID}` ? true : null), "the task's own URL");
    await settle(100);
    expect(page.querySelector("[role=tab][aria-selected=true]")?.textContent).toBe("Overview");
    // Once there, the record is spent: a later return to the task's URL reads the default again.
    await act(async () => { window.location.hash = `#/task/${TASK_ID}/findings`; });
    await until(() => (page.querySelector("[role=tab][aria-selected=true]")?.textContent?.startsWith("Findings") ? true : null), "Findings");
    await act(async () => { window.location.hash = `#/task/${TASK_ID}`; });
    await settle(100);
    expect(page.querySelector("[role=tab][aria-selected=true]")?.textContent).toBe("Overview");
  });

  test("a URL naming attempt 1 and no tab opens on Overview of attempt 1, not Chat", async () => {
    const page = await app(restartedWith(conductor(2)), `#/task/${TASK_ID}?attempt=1`);
    await until(() => (shown(page) === "1" ? true : null), "attempt 1 from the URL");
    await settle(100);
    expect(page.querySelector("[role=tab][aria-selected=true]")?.textContent).toBe("Overview");
    expect(window.location.hash).toBe(`#/task/${TASK_ID}?attempt=1`);
  });

  test("the task's own URL on a started-over task with a conductor opens on Overview", async () => {
    const page = await app(restartedWith(conductor(2)), `#/task/${TASK_ID}`);
    await until(() => picker(page), "the task page with its picker");
    await settle(50);
    expect(shown(page)).toBe("2");
    expect(page.querySelector("[role=tab][aria-selected=true]")?.textContent).toBe("Overview");
  });

  test("a Chat link on a task with a conductor still opens Chat, and a session link its session", async () => {
    const page = await app(restartedWith(conductor(2)), `#/task/${TASK_ID}/chat`);
    await until(() => picker(page), "the task page with its picker");
    await settle(50);
    expect(page.querySelector("[role=tab][aria-selected=true]")?.textContent).toBe("Chat");
    await act(async () => { window.location.hash = `#/session/${COND}`; });
    await until(() => (page.querySelector("[role=tab][aria-selected=true]")?.textContent?.startsWith("Sessions") ? true : null), "Sessions");
  });

  test("the conductor's open question on Overview answers in Chat: the link keeps its destination", async () => {
    const client = restartedWith(conductor(2));
    const events = client.events.bind(client);
    const asked = { eventId: "evt_cond_q", cursor: 99_000, eventType: "question.asked", occurredAt: at(1), organizationId: "org", projectId: "prj",
      taskId: TASK_ID, runId: COND, sessionId: null, workflowRunId: null, actor: { type: "agent", id: COND }, source: "harness",
      correlationId: null, causationId: null, payload: { kind: "agent", questionId: "q_cond", prompt: "Ship behind a flag?", toName: "Ana Ribeiro" } } as PersistedEvent;
    client.events = async (params) => {
      const r = await events(params);
      return params.runId || (params.after ?? 0) >= asked.cursor ? r : { events: [...r.events, asked], nextCursor: Math.max(r.nextCursor, asked.cursor) };
    };
    const page = await app(client, `#/task/${TASK_ID}`);
    const answer = await until(() => page.querySelector<HTMLElement>("[data-testid=needs-you-answer]"), "the needs-you action");
    expect(answer.textContent).toBe("Answer in Chat");
    await click(answer);
    await until(() => (page.querySelector("[role=tab][aria-selected=true]")?.textContent === "Chat" ? true : null), "Chat");
    expect(window.location.hash).toBe(`#/task/${TASK_ID}/chat`);
  });

  test("a reviewer's question holding the task opens Findings from its notice", async () => {
    const client = restartedWith(conductor(2));
    const getTask = client.getTask.bind(client);
    client.getTask = async (id) => ({ ...(await getTask(id)), heldByQuestions: true });
    const reviewQuestions = client.reviewQuestions.bind(client);
    client.reviewQuestions = async (id) => {
      const r = await reviewQuestions(id);
      return r.questions.length ? r : { questions: [{ id: "rvq_x", taskId: TASK_ID, runId: "run_a2_fix", category: "correctness", findingId: null,
        question: "Is the legacy path still used?", context: "", status: "open", createdAt: at(5) } as unknown as typeof r.questions[number]] };
    };
    const page = await app(client, `#/task/${TASK_ID}`);
    const open = await until(() => page.querySelector<HTMLElement>("[data-testid=questions-hold-open]"), "Show the questions");
    await click(open);
    await until(() => (page.querySelector("[role=tab][aria-selected=true]")?.textContent?.startsWith("Findings") ? true : null), "Findings");
    expect(window.location.hash).toBe(`#/task/${TASK_ID}/findings`);
  });
});

describe("a conductor's session on an earlier attempt", () => {
  test("attempt 1's conductor, named with attempt 1, is read-only with the rest of attempt 1", async () => {
    const page = await taskPage(restartedWith(conductor(1, { status: "aborted", endedAt: at(96) })), { runId: COND, attempt: 1 });
    await until(() => (shown(page) === "1" ? true : null), "attempt 1");
    await until(() => page.querySelector("[data-testid=earlier-bar]"), "the earlier-attempt bar");
    expect(count(page, "[data-testid=abort]")).toBe(0);
    expect(count(page, "textarea")).toBe(0);
  });

  test("attempt 2's live conductor named with attempt 1 opens on attempt 2, writable", async () => {
    const page = await taskPage(restartedWith(conductor(2)), { runId: COND, attempt: 1 });
    await until(() => page.querySelector("[data-testid=abort]"), "Abort on the conductor");
    expect(shown(page)).toBe("2");
    expect(count(page, "[data-testid=earlier-bar]")).toBe(0);
    expect(count(page, "textarea")).toBe(1);
  });
});

describe("a task started over before each attempt had its own conductor", () => {
  // Its one conductor, recorded on attempt 1, still live and steering attempt 2.
  test("its live conductor is the current attempt's: its session opens on attempt 2, writable, with no earlier bar", async () => {
    const page = await taskPage(restartedWith(conductor(1)), { runId: COND });
    await until(() => page.querySelector("[data-testid=abort]"), "Abort on the live conductor");
    expect(shown(page)).toBe("2");
    expect(count(page, "[data-testid=earlier-bar]")).toBe(0);
    expect(count(page, "textarea")).toBe(1);
  });

  test("Sessions lists it with attempt 2's Runs", async () => {
    const page = await taskPage(restartedWith(conductor(1)), { tab: "sessions" });
    await until(() => (shown(page) === "2" ? true : null), "attempt 2");
    expect(tabCount(page, "Sessions")).toBe("5");
    const list = await sessionOptions(page);
    const rows = await until(() => {
      const r = [...list.querySelectorAll<HTMLElement>('[role="option"]')];
      return r.length ? r : null;
    }, "the sessions listed");
    expect(rows[0]!.textContent).toContain("Conductor");
  });
});


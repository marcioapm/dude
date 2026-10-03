/**
 * A task's conductor beside its attempts: the conductor and its Chat are
 * the task's, not an attempt's. Its Run makes, ends and begins no attempt;
 * its session is listed on every attempt and is never read-only; its URL
 * names no attempt, and neither does Chat's. Mounted in happy-dom on the
 * fixture client's started-over task (`dude.fixtures.run=restarted`).
 */

import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { act, click, mount, settle, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { RESTARTED_RUNS, TASK_ID } from "../src/fixtures/data.ts";
import type { PersistedEvent, Run } from "@dude/domain";
import { App } from "../src/App.tsx";
import { PeopleProvider } from "../src/people.tsx";
import { TaskScreen } from "../src/screens/TaskScreen.tsx";
import { attemptStarts, attemptStatus, attemptsOf, eventAttempts, setAsideOf } from "../src/attempts.ts";
import { formatPlace, inTree, parsePlace } from "../src/place.ts";

let mounted: Array<() => Promise<void>> = [];
beforeEach(() => localStorage.clear());
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  localStorage.clear();
  window.history.replaceState(null, "", " ");
});

const at = (min: number) => new Date(Date.now() - min * 60_000).toISOString();
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
  test("does not make an attempt", () => {
    expect(attemptsOf([...RESTARTED_RUNS, conductor(3)])).toEqual([2, 1]);
  });

  test("does not decide how an attempt ended", () => {
    // Attempt 1's fixer was aborted 97 minutes ago; its conductor failed after that.
    const runs = [...RESTARTED_RUNS, conductor(1, { status: "failed", error: "host lost", createdAt: at(100), endedAt: at(96) })];
    const task = { status: "running", runs } as unknown as Task;
    expect(attemptStatus(task, 1, 2)).toBe("aborted");
    const people = { you: null, me: null, all: [], byId: new Map(), names: new Map(), refresh: async () => people, seen: () => false } as never;
    expect(setAsideOf(1, runs, [], people).how).toBe("stopped at Fix, aborted");
  });

  test("does not say when an attempt began", () => {
    // Recorded on attempt 2 two hours ago, before attempt 2's first agent (90 minutes ago).
    const starts = attemptStarts([...RESTARTED_RUNS, conductor(2, { createdAt: at(120) })]);
    expect(starts.get(2)).toBe(RESTARTED_RUNS.find((r) => r.id === "run_attempt2")!.createdAt);
  });

  test("what happened on it is no attempt's", () => {
    const of = eventAttempts([...RESTARTED_RUNS, conductor(1)], [], 2);
    expect(of({ eventType: "chat.message", runId: COND, occurredAt: at(5), payload: {} } as unknown as PersistedEvent)).toBeNull();
    expect(of({ eventType: "run.steered", runId: "run_a1_fix", occurredAt: at(5), payload: {} } as unknown as PersistedEvent)).toBe(1);
  });
});

describe("the task page with a conductor", () => {
  test("a conductor on a later attempt column makes no attempt in the picker", async () => {
    const page = await taskPage(restartedWith(conductor(3)));
    await until(() => (shown(page) === "2" ? true : null), "attempt 2, the current one");
    const trigger = await until(() => picker(page), "the attempt picker");
    await act(async () => void trigger.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
    await until(() => document.querySelector('[role=option][data-value="1"]'), "the options");
    expect([...document.querySelectorAll("[role=option]")].map((o) => o.getAttribute("data-value"))).toEqual(["2", "1"]);
  });

  test("a conductor that failed after attempt 1's fixer is not where attempt 1 stopped", async () => {
    const page = await taskPage(restartedWith(conductor(1, { status: "failed", error: "host lost", createdAt: at(100), endedAt: at(96) })));
    await pick(page, 1);
    expect(page.querySelector("[data-testid=header-status]")?.getAttribute("data-status")).toBe("aborted");
    const bar = await until(() => page.querySelector<HTMLElement>("[data-testid=earlier-bar]"), "the earlier-attempt bar");
    expect(bar.textContent).toContain("It had stopped at Fix, aborted by Ana.");
  });

  test("a conductor recorded on attempt 2 before its first agent does not move when attempt 2 began", async () => {
    // The decision 112 minutes ago, while attempt 1 was at work, stays attempt 1's.
    const page = await taskPage(restartedWith(conductor(2, { status: "completed", createdAt: at(115), endedAt: at(113) })), { tab: "activity" });
    const line = await until(() => [...page.querySelectorAll<HTMLElement>("[data-testid=activity-item]")]
      .find((l) => l.textContent?.includes("decided how delivery goes on")), "the decision's line");
    expect(line.getAttribute("data-attempt")).toBe("1");
  });

  test("an Activity line about the conductor carries no attempt and no way to one", async () => {
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
    expect(line.hasAttribute("data-attempt")).toBe(false);
    expect(line.textContent).not.toContain("· attempt");
    // The fix's line beside it is still attempt 1's.
    const fix = [...page.querySelectorAll<HTMLElement>("[data-testid=activity-item]")].find((l) => l.textContent?.includes("aborted the fix"))!;
    expect(fix.getAttribute("data-attempt")).toBe("1");
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
  const sessionsListed = (page: HTMLElement) => until(() => {
    const l = [...page.querySelectorAll<HTMLElement>("[data-testid=sessions] [data-testid=session]")];
    return l.length > 0 ? l : null;
  }, "the sessions listed");

  test("opens on the current attempt, whatever attempt its Run names", async () => {
    const page = await app(restartedWith(conductor(1)), `#/session/${COND}`);
    await until(() => page.querySelector("[data-testid=attempt-picker]"), "the task page with its picker");
    await until(() => page.querySelector("[data-testid=abort]"), "the conductor's session, live");
    expect(shown(page)).toBe("2");
    expect(count(page, "[data-testid=earlier-bar]")).toBe(0);
  });

  test("on attempt 1, Sessions lists the conductor first and counts it", async () => {
    const page = await app(restartedWith(conductor(2)), `#/task/${TASK_ID}/sessions?attempt=1`);
    await until(() => (shown(page) === "1" ? true : null), "attempt 1 from the URL");
    const list = await sessionsListed(page);
    expect(list[0]!.textContent).toContain("Conductor");
    // Attempt 1's four Runs, and the conductor.
    expect(list).toHaveLength(5);
    expect(tabCount(page, "Sessions")).toBe("5");
    const tab = [...page.querySelectorAll<HTMLElement>("[role=tab]")].find((t) => t.textContent?.startsWith("Sessions"))!;
    await act(async () => tab.focus());
    const tip = await until(() => document.querySelector<HTMLElement>("[role=tooltip]"), "the Sessions tooltip");
    expect(tip.textContent).toBe("Attempt 1's sessions, and the task's conductor");
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
    expect(page.querySelector("[role=tab][aria-selected=true]")?.textContent).toBe("Chat");
  });

  test("a URL naming attempt 1 and no tab opens on Overview of attempt 1, not Chat", async () => {
    const page = await app(restartedWith(conductor(2)), `#/task/${TASK_ID}?attempt=1`);
    await until(() => (shown(page) === "1" ? true : null), "attempt 1 from the URL");
    await settle(100);
    expect(page.querySelector("[role=tab][aria-selected=true]")?.textContent).toBe("Overview");
    expect(window.location.hash).toBe(`#/task/${TASK_ID}?attempt=1`);
  });

  test("the task's own URL on a started-over task with a conductor opens on Chat", async () => {
    const page = await app(restartedWith(conductor(2)), `#/task/${TASK_ID}`);
    await until(() => picker(page), "the task page with its picker");
    await settle(50);
    expect(shown(page)).toBe("2");
    expect(page.querySelector("[role=tab][aria-selected=true]")?.textContent).toBe("Chat");
  });
});

describe("a conductor's session on an earlier attempt", () => {
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
  const rows = (page: HTMLElement) => [...page.querySelectorAll<HTMLElement>("[data-testid=sessions] [data-testid=session]")];
  const openRow = (page: HTMLElement) => until(() => rows(page).find((l) => l.querySelector("[aria-current=true]")), "the open session's row");
  const writable = (page: HTMLElement) => {
    expect(count(page, "[data-testid=abort]")).toBe(1);
    expect(count(page, "[data-testid=run-ended]")).toBe(0);
    expect(count(page, "textarea")).toBe(1);
  };
  // The tree lists the conductor's session with its task, as the server's does
  // for the attempts it keeps: the page then stays mounted when it is opened.
  const inTheTree = (client: FixtureClient) => {
    const navigation = client.navigation.bind(client);
    client.navigation = async () => {
      const nav = await navigation();
      return { projects: nav.projects.map((p) => ({ ...p, epics: p.epics.map((e) => ({ ...e, tasks: e.tasks.map((t) => t.id !== TASK_ID ? t
        : { ...t, runs: t.runs!.map((r, i) => i > 0 ? r : { ...r, sessions: [{ id: COND, role: "conductor", status: "running", title: "Conductor" }, ...r.sessions] }) }) })) })) };
    };
    return client;
  };

  test("stays on the attempt the place names, and keeps its composer and Abort", async () => {
    const page = await taskPage(restartedWith(conductor(2)), { runId: COND, attempt: 1 });
    await until(() => page.querySelector("[data-testid=abort]"), "Abort on the conductor");
    expect(shown(page)).toBe("1");
    expect(count(page, "[data-testid=earlier-bar]")).toBe(1);
    expect(count(page, "[data-testid=run-ended]")).toBe(0);
    expect(count(page, "textarea")).toBe(1);
  });

  test("Sessions on attempt 1 first opens one of attempt 1's, not the task's ended conductor", async () => {
    const page = await app(restartedWith(conductor(2, { status: "completed", endedAt: at(20) })), `#/task/${TASK_ID}/sessions`);
    expect((await openRow(page)).textContent).toContain("Fix");
    await pick(page, 1);
    const open = await openRow(page);
    expect(rows(page)[0]!.textContent).toContain("Conductor");
    expect(open.textContent).not.toContain("Conductor");
    // Attempt 1's newest: its aborted Fix.
    expect(open.textContent).toContain("Fix");
  });

  test("the live conductor opened from attempt 1's list keeps its composer and Abort", async () => {
    const page = await app(inTheTree(restartedWith(conductor(2))), `#/task/${TASK_ID}/sessions?attempt=1`);
    await until(() => (shown(page) === "1" ? true : null), "attempt 1 from the URL");
    expect((await openRow(page)).textContent).not.toContain("Conductor");
    const bar = () => page.querySelector<HTMLElement>("[data-testid=earlier-bar]")?.textContent ?? "";
    expect(bar()).toContain("nothing in it can be merged, resumed or steered.");
    await click(rows(page).find((l) => l.textContent?.includes("Conductor"))!.querySelector("button")!);
    await until(() => (window.location.hash === `#/session/${COND}` ? true : null), "the conductor's URL");
    await until(() => page.querySelector("[data-testid=abort]"), "Abort on the conductor");
    expect(shown(page)).toBe("1");
    expect(count(page, "[data-testid=earlier-bar]")).toBe(1);
    // The conductor under the bar is the task's, and can be steered: the bar does not say otherwise.
    expect(bar()).toContain("nothing in it can be merged or resumed.");
    expect(bar()).not.toContain("steered");
    writable(page);
  });

  test("the live conductor open on the current attempt stays writable once attempt 1 is picked", async () => {
    const page = await app(restartedWith(conductor(2)), `#/task/${TASK_ID}/sessions`);
    expect((await openRow(page)).textContent).toContain("Conductor");
    await pick(page, 1);
    expect(window.location.hash).toBe(`#/task/${TASK_ID}/sessions?attempt=1`);
    expect((await openRow(page)).textContent).toContain("Conductor");
    await until(() => page.querySelector("[data-testid=abort]"), "Abort on the conductor");
    expect(count(page, "[data-testid=earlier-bar]")).toBe(1);
    writable(page);
    // Off Sessions, the conductor is no longer under the bar: it speaks for the attempt alone.
    await openTab(page, "Overview");
    await until(() => (page.querySelector("[data-testid=earlier-bar]")?.textContent?.includes("resumed or steered.") ? true : null), "the bar's full sentence");
  });
});

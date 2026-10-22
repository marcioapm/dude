/**
 * Lifecycle in the web app (docs/design/tracking-work.md, Epic 1): the
 * Activity lines Close, Reopen, Archive and Unarchive write, and the one
 * line Close and archive's batch makes.
 */

import { describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { EventTypes, type PersistedEvent, type Run } from "@dude/domain";
import { activityLines } from "../src/screens/TaskScreen.tsx";
import { afterEach, beforeEach } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { act, mount, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { EPIC, PROJECT, PULL_REQUEST, TASK_ID } from "../src/fixtures/data.ts";
import { ArchiveEpicConfirm, epicRef } from "../src/screens/actions.tsx";
import { PeopleProvider } from "../src/people.tsx";
import { TaskScreen } from "../src/screens/TaskScreen.tsx";
import { ApiError, type TaskDetail } from "../src/api/client.ts";
import type { CloseDecision } from "@dude/design-system/components";

let mounted: Array<() => Promise<void>> = [];
beforeEach(() => localStorage.clear());
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  localStorage.clear();
});

async function taskPage(client: FixtureClient) {
  const { container, unmount } = await mount(
    <TooltipProvider><ToastProvider><PeopleProvider client={client}>
      <TaskScreen client={client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} />
    </PeopleProvider></ToastProvider></TooltipProvider>,
  );
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=task-screen]"), "the task page");
  return container;
}

/** A fixture client whose task is in `patch`'s state. */
async function clientWith(patch: Partial<TaskDetail>): Promise<FixtureClient> {
  const client = new FixtureClient("a");
  const base = await client.getTask(TASK_ID);
  let task: TaskDetail = { ...base, ...patch, runs: patch.status && patch.status !== "running" ? [] : base.runs };
  client.getTask = async () => task;
  // The writes act on this task, as the orchestrator would; it is read back through getTask.
  client.closeTask = async (_id, d) => {
    task = { ...task, status: "aborted", resolution: d.resolution, duplicateOf: d.duplicateOf ?? null };
    return {};
  };
  client.reopenTask = async () => {
    if (task.archivedAt) throw new ApiError(409, "archived", "archived");
    task = { ...task, status: "received", resolution: null, duplicateOf: null };
    return {};
  };
  client.archiveTask = async (_id, d) => {
    task = { ...task, ...(d ? { status: "aborted" as const, resolution: d.resolution } : {}), archivedAt: new Date().toISOString() };
    return {};
  };
  client.unarchiveTask = async () => {
    task = { ...task, archivedAt: null };
    return {};
  };
  return client;
}

const menuItems = async (page: HTMLElement) => {
  const trigger = page.querySelector<HTMLElement>('[aria-label="Task actions"]')!;
  await act(async () => {
    trigger.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" }));
  });
  const items = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')];
  return items;
};
const itemText = (el: HTMLElement) => el.textContent ?? "";

describe("the fixture client's lifecycle", () => {
  test("closes a running task as the orchestrator does, stopping what is live; refuses Reopen and Unarchive by code", async () => {
    const client = new FixtureClient("a");
    const code = async (p: Promise<unknown>) => p.then(() => "ok", (e: unknown) => (e as ApiError).code);
    expect(await code(client.reopenTask(TASK_ID))).toBe("conflict");
    expect(await code(client.unarchiveTask(TASK_ID))).toBe("conflict");
    expect(await code(client.closeTask(TASK_ID, { resolution: "wont_do", closePullRequest: true }))).toBe("ok");
    const task = await client.getTask(TASK_ID);
    expect(task.runs.every((r) => ["completed", "failed", "aborted"].includes(r.status))).toBe(true);
    const closed = (await client.events({ taskId: TASK_ID })).events.findLast((e) => e.eventType === "task.closed")!;
    expect((closed.payload.stopped as string[]).length).toBeGreaterThan(0);
    expect(closed.payload.pullRequestCloseRequested).toBe(true);
    expect(closed.payload).not.toHaveProperty("pullRequestClosed");
    expect((await client.archivedTasks(TASK_ID)).tasks).toEqual([]);
  });
});

describe("the epic archive dialog callback", () => {
  for (const archived of [false, true]) {
    test(`reports the API archived=${archived} result after success, not the subset request`, async () => {
      const client = new FixtureClient("a");
      const epic = (await client.navigation()).projects.find((p) => p.id === PROJECT.id)!.epics!.find((e) => e.id === EPIC.id)!;
      const response = Promise.withResolvers<{ archived: boolean; tasks: string[] }>();
      const requests: boolean[] = [];
      client.archiveEpic = (_id, withEndedTasks) => { requests.push(withEndedTasks ?? false); return response.promise; };
      const callbacks: Array<boolean | "closed"> = [];
      const { unmount } = await mount(<ToastProvider>
        <ArchiveEpicConfirm client={client} epic={epicRef(PROJECT.id, epic)} tasks={epic.tasks}
          onClose={() => callbacks.push("closed")} onArchived={(result) => callbacks.push(result)} />
      </ToastProvider>);
      mounted.push(unmount);
      const confirm = await until(() => document.querySelector<HTMLElement>("[data-testid=archive-ended]"), "the subset archive action");
      await act(async () => confirm.click());
      expect(requests).toEqual([true]);
      expect(callbacks).toEqual([]);
      await act(async () => response.resolve({ archived, tasks: ["t_211"] }));
      expect(callbacks).toEqual(["closed", archived]);
    });
  }
});

describe("the task page's lifecycle", () => {
  test("a task in progress: Close… and Archive… are there and enabled, after Stop…", async () => {
    const page = await taskPage(new FixtureClient("a"));
    const items = await menuItems(page);
    expect(itemText(items[0]!)).toContain("Stop…");
    const close = items.find((i) => itemText(i).startsWith("Close"))!;
    const archive = items.find((i) => itemText(i).startsWith("Archive"))!;
    for (const i of [close, archive]) {
      expect(i.getAttribute("aria-disabled")).toBeNull();
      expect(itemText(i)).not.toContain("Stop its agents first");
    }
  });

  test("Close… on a task in Backlog closes it; the closed notice offers Reopen, and Reopen puts it back", async () => {
    const client = await clientWith({ status: "received" });
    const page = await taskPage(client);
    const close = (await menuItems(page)).find((i) => itemText(i).startsWith("Close"))!;
    expect(close.getAttribute("aria-disabled")).toBeNull();
    await act(async () => close.click());
    const dialog = await until(() => document.querySelector<HTMLElement>('[role="dialog"]'), "the Close dialog");
    expect(dialog.textContent).toContain("Close WC-214");
    await act(async () => [...dialog.querySelectorAll<HTMLElement>('[role="radio"]')].find((r) => r.textContent?.startsWith("Won't do"))!.click());
    await act(async () => dialog.querySelector<HTMLElement>('[data-testid="close-confirm"]')!.click());
    const notice = await until(() => page.querySelector<HTMLElement>('[data-testid="closed-notice"]'), "the closed notice");
    expect(notice.textContent).toContain("WC-214 was closed as Won't do");
    expect(page.querySelector('[data-testid="task-header"] [data-testid="resolution-mark"]')!.textContent).toBe("Won't do");
    // No Resume on a closed task: the stopped notice gives way to Reopen.
    expect(page.querySelector('[data-testid="stopped"]')).toBeNull();
    await act(async () => notice.querySelector<HTMLElement>('[data-testid="reopen"]')!.click());
    await until(() => !page.querySelector('[data-testid="closed-notice"]'), "the notice to go");
    expect(page.querySelector('[data-testid="resolution-mark"]')).toBeNull();
  });

  test("Archive… on a task that hasn't ended opens Close and archive; archived, the notice offers Unarchive, and Reopen waits for it", async () => {
    const client = await clientWith({ status: "received" });
    const page = await taskPage(client);
    const archive = (await menuItems(page)).find((i) => itemText(i).startsWith("Archive"))!;
    await act(async () => archive.click());
    const dialog = await until(() => document.querySelector<HTMLElement>('[role="dialog"]'), "the dialog");
    expect(dialog.textContent).toContain("Close and archive WC-214");
    const confirm = dialog.querySelector<HTMLButtonElement>('[data-testid="close-confirm"]')!;
    expect(confirm.disabled).toBe(true);
    await act(async () => [...dialog.querySelectorAll<HTMLElement>('[role="radio"]')].find((r) => r.textContent?.startsWith("Won't do"))!.click());
    await act(async () => confirm.click());
    const notice = await until(() => page.querySelector<HTMLElement>('[data-testid="archived-notice"]'), "the archived notice");
    expect(notice.textContent).toContain("Unarchive it first to reopen it");
    expect(page.querySelector('[data-testid="archived-mark"]')).not.toBeNull();
    const reopen = (await menuItems(page)).find((i) => itemText(i).startsWith("Reopen"))!;
    expect(reopen.getAttribute("aria-disabled")).toBe("true");
    expect(itemText(reopen)).toContain("Unarchive it first, then reopen it");
  });

  test("a task marked done (Nothing to merge) is not closed: no resolution mark, no Reopen", async () => {
    const page = await taskPage(await clientWith({ status: "done", resolution: "nothing_to_merge" }));
    expect(page.querySelector('[data-testid="resolution-mark"]')).toBeNull();
    expect(page.querySelector('[data-testid="closed-notice"]')).toBeNull();
    const items = (await menuItems(page)).map(itemText);
    expect(items.some((t) => t.startsWith("Reopen"))).toBe(false);
    expect(items.find((t) => t.startsWith("Closed"))).toContain("Already ended");
  });

  test("a stopped task with its pull request still open: Close… and Archive… are enabled", async () => {
    const client = await clientWith({ status: "aborted" });
    client.listPullRequests = async () => ({ pullRequests: [{ ...PULL_REQUEST, state: "open" }] });
    const page = await taskPage(client);
    const items = (await menuItems(page)).filter((i) => /^(Close|Archive)/.test(itemText(i)));
    expect(items.map(itemText)).toEqual(["Close…", "Archive…"]);
    for (const i of items) expect(i.getAttribute("aria-disabled")).toBeNull();
  });

  test("a member sees the lifecycle disabled with whose decision it is", async () => {
    const client = await clientWith({ status: "received", owner: { id: "u_ana", name: "Ana Ribeiro", photoUrl: null, online: false } });
    const listed = await client.listPeople();
    client.listPeople = async () => ({ ...listed, people: listed.people.map((p) => (p.id === listed.you ? { ...p, role: "member" as const } : p)) });
    const page = await taskPage(client);
    for (const i of (await menuItems(page)).filter((x) => /^(Close|Archive)/.test(itemText(x)))) {
      expect(i.getAttribute("aria-disabled")).toBe("true");
      expect(itemText(i)).toContain("Only Ana, the owner, or an admin can do this");
    }
  });
});

describe("Close and Archive while live", () => {
  const pick = async (dialog: HTMLElement, label: string) =>
    act(async () => [...dialog.querySelectorAll<HTMLElement>('[role="radio"]')].find((r) => r.textContent?.startsWith(label))!.click());

  /** A running conductor task: its conductor between turns and an implementer at work; its PR as given; its closes recorded. */
  async function liveTask(prs: Array<typeof PULL_REQUEST>, source?: TaskDetail["source"]) {
    const client = new FixtureClient("a");
    const base = await client.getTask(TASK_ID);
    const conductor = { ...base.runs[0]!, id: "run_cond_live", role: "conductor" as const, phase: null, category: null, status: "paused" as const };
    const task: TaskDetail = { ...base, status: "running", decider: "conductor", runs: [conductor, ...base.runs], ...(source ? { source } : {}) };
    client.getTask = async () => task;
    client.listPullRequests = async () => ({ pullRequests: prs });
    const sent: Array<[string, CloseDecision]> = [];
    client.closeTask = async (_id, d) => { sent.push(["close", d]); return {}; };
    client.archiveTask = async (_id, d) => { sent.push(["archive", d!]); return {}; };
    return { client, sent };
  }
  const openFromMenu = async (page: HTMLElement, label: string) => {
    const item = (await menuItems(page)).find((i) => itemText(i).startsWith(label))!;
    expect(item.getAttribute("aria-disabled")).toBeNull();
    await act(async () => item.click());
    return until(() => document.querySelector<HTMLElement>('[role="dialog"]'), "the dialog");
  };

  test("on a running conductor task, Close… is enabled; the dialog lists what stops and says Stop and close", async () => {
    const { client, sent } = await liveTask([]);
    const page = await taskPage(client);
    const dialog = await openFromMenu(page, "Close");
    const live = dialog.querySelector<HTMLElement>('[data-testid="live-work"]')!;
    expect(live.textContent).toContain("Closing stops the conductor and an implementer. Nothing is kept to resume.");
    expect(dialog.querySelector('[data-testid="close-pull-request"]')).toBeNull();
    await pick(dialog, "Won't do");
    const confirm = dialog.querySelector<HTMLButtonElement>('[data-testid="close-confirm"]')!;
    expect(confirm.textContent).toBe("Stop and close");
    await act(async () => confirm.click());
    expect(sent).toEqual([["close", { resolution: "wont_do" }]]);
  });

  test("dude's open PR is ticked to close with the task; the decision says so", async () => {
    const { client, sent } = await liveTask([{ ...PULL_REQUEST, state: "open", number: 412 }]);
    const page = await taskPage(client);
    const dialog = await openFromMenu(page, "Close");
    const row = await until(() => dialog.querySelector<HTMLElement>('[data-testid="close-pull-request"]'), "the PR row");
    expect(row.getAttribute("data-opened-by")).toBe("dude");
    expect(row.querySelector('[role="checkbox"]')!.getAttribute("aria-checked")).toBe("true");
    await pick(dialog, "Won't do");
    await act(async () => dialog.querySelector<HTMLButtonElement>('[data-testid="close-confirm"]')!.click());
    expect(sent).toEqual([["close", { resolution: "wont_do", closePullRequest: true }]]);
  });

  test("Archive… on a live task with an adopted PR: Close and archive, nothing chosen, the PR unticked", async () => {
    const source = { kind: "pr" as const, number: 412, headBranch: "marcio/pause-split", baseBranch: "main", repositoryId: "repo_wc", repository: "web-console",
      title: "Pause split", mode: "review" as const };
    const { client, sent } = await liveTask([{ ...PULL_REQUEST, state: "open", number: 412 }], source);
    const page = await taskPage(client);
    const dialog = await openFromMenu(page, "Archive");
    expect(dialog.textContent).toContain("Close and archive WC-214");
    expect(dialog.querySelector('[role="radio"][aria-checked="true"]')).toBeNull();
    const row = await until(() => dialog.querySelector<HTMLElement>('[data-testid="close-pull-request"]'), "the PR row");
    expect(row.getAttribute("data-opened-by")).toBe("someone");
    expect(row.querySelector('[role="checkbox"]')!.getAttribute("aria-checked")).toBe("false");
    await pick(dialog, "Won't do");
    const confirm = dialog.querySelector<HTMLButtonElement>('[data-testid="close-confirm"]')!;
    expect(confirm.textContent).toBe("Stop, close and archive");
    await act(async () => confirm.click());
    expect(sent).toEqual([["archive", { resolution: "wont_do", closePullRequest: false }]]);
  });
});

const ANA = "per_ana";
let cursor = 0;
function ev(eventType: string, payload: Record<string, unknown>, extra: Partial<PersistedEvent> = {}): PersistedEvent {
  cursor++;
  return { eventId: `evt_l${cursor}`, cursor, eventType, occurredAt: new Date(Date.UTC(2026, 9, 17, 10, 0, cursor)).toISOString(),
    organizationId: "org", projectId: "prj", taskId: "wi_63", runId: null, sessionId: null, workflowRunId: null,
    actor: { type: "human", id: ANA }, source: "orchestrator", correlationId: null, causationId: null, payload, ...extra };
}
const people = { names: new Map([[ANA, "Ana Ribeiro"]]), byId: new Map(), you: ANA } as unknown as Parameters<typeof activityLines>[1];
const said = (lines: ReturnType<typeof activityLines>) =>
  lines.map((l) => renderToStaticMarkup(<>{l.text}</>).replace(/<[^>]+>/g, "").replace(/&#x27;/g, "'")).reverse();

describe("lifecycle in Activity", () => {
  test("each event has its line, both ways, with the handover said", () => {
    const lines = activityLines([
      ev(EventTypes.TaskClosed, { resolution: "duplicate", duplicateOf: "CP-62", handedOver: ["CP-47"] }),
      ev(EventTypes.TaskReopened, { was: "duplicate" }),
      ev(EventTypes.TaskClosed, { resolution: "wont_do", removedRelations: ["CP-47"], note: "Superseded by the new export." }),
      ev(EventTypes.TaskArchived, {}),
      ev(EventTypes.TaskUnarchived, {}),
      ev(EventTypes.TaskReopened, { was: "cant_reproduce" }),
    ], people, [], [], 1, "CP-63");
    expect(said(lines)).toEqual([
      "Ana Ribeiro closed CP-63 as a duplicate of CP-62 — CP-47, which waited on CP-63, now waits on CP-62",
      "Ana Ribeiro reopened CP-63 (was Duplicate)",
      "Ana Ribeiro closed CP-63 as Won't do · CP-47 no longer waits on it",
      "Ana Ribeiro archived CP-63",
      "Ana Ribeiro unarchived CP-63",
      "Ana Ribeiro reopened CP-63 (was Can't reproduce)",
    ]);
    expect(lines.find((l) => l.quote === "Superseded by the new export.")).toBeDefined();
  });

  test("Close and archive is one line, the note under it", () => {
    const closed = ev(EventTypes.TaskClosed, { resolution: "wont_do", andArchived: true, note: "Not this quarter." });
    const lines = activityLines([closed, ev(EventTypes.TaskArchived, { viaClose: true }, { causationId: closed.eventId })], people, [], [], 1, "CP-44");
    expect(said(lines)).toEqual(["Ana Ribeiro closed CP-44 as Won't do and archived it"]);
    expect(lines[0]!.quote).toBe("Not this quarter.");
  });

  test("an archive with its epic says so; a closed start when unblocked says why it ended", () => {
    const lines = activityLines([
      ev(EventTypes.TaskArchived, { viaEpic: "epc_1" }),
      ev(EventTypes.TaskAutoStartCancelled, { how: "deliver", why: "closed" }),
    ], people, [], [], 1, "CP-46");
    expect(said(lines)).toEqual(["Ana Ribeiro archived CP-46 with its epic", "Ana Ribeiro closed it, which ends start when unblocked"]);
  });
});

describe("Stop in Activity", () => {
  const run = (id: string, attempt: number, p: Partial<Run>): Run => ({ id, organizationId: "org", projectId: "prj", taskId: "wi_63", attempt,
    status: "aborted", workerId: null, workspacePath: null, error: null, kind: "agent", phase: null, role: null, category: null, parentRunId: null,
    baseRefs: {}, heads: {}, branch: null, harness: null, model: null, dudePause: null, tokens: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, context: 0 },
    machine: null, createdAt: new Date(Date.UTC(2026, 9, 17, 9)).toISOString(), startedAt: null, endedAt: null, ...p }) as Run;
  const runs = [run("run_c1", 1, { role: "conductor" }), run("run_i1", 1, { phase: "implement", role: "implementer" }),
    run("run_c2", 2, { role: "conductor", status: "running", createdAt: new Date(Date.UTC(2026, 9, 17, 11)).toISOString() })];
  const aborted = (runId: string, note: string) => ev(EventTypes.RunAborted, { reason: note, by: "stop" }, { runId });

  test("Stop here is one line naming what it stopped, the note under it; its Runs' aborts are not said again", () => {
    const note = "Wrong greeting: hold on.";
    const lines = activityLines([
      aborted("run_c1", note), aborted("run_i1", note),
      ev("task.stopped", { by: ANA, note, runIds: ["run_c1", "run_i1"], decider: "conductor" }),
    ], people, runs, [], 1, "GREE-2");
    expect(said(lines)).toEqual(["Ana Ribeiro stopped the task · stopped the conductor and the implementer"]);
    expect(lines[0]!.quote).toBe(note);
    expect(lines[0]!.attempt).toBe(1);
  });

  test("Stop and start over is one line: the attempt stopped, the one started and who decides it, never a pick-up", () => {
    const note = "Start fresh.";
    const lines = activityLines([
      aborted("run_c1", note), aborted("run_i1", note),
      ev("task.stopped", { by: ANA, note, runIds: ["run_c1", "run_i1"], decider: "conductor", andRestart: true, nextDecider: "policy" }),
      ev(EventTypes.TaskDeciderChanged, { from: "conductor", to: "policy", why: "start over" }),
      ev("task.recovered", { action: "restart", note, attempt: 2, runIds: [], decider: "policy", via: "stop" }),
      // Attempt 2's own decider change, after the start: its line stays.
      ev(EventTypes.TaskDeciderChanged, { from: "policy", to: "conductor", why: "a person wrote in Chat" }),
    ], people, runs, [], 2, "GREE-2");
    expect(said(lines)).toEqual(["Ana Ribeiro stopped attempt 1 and started attempt 2 · Deliver decides · stopped the conductor and the implementer",
      "Ana Ribeiro wrote in Chat, which gave the decisions to the conductor"]);
    const stopLine = lines.at(-1)!;
    expect(stopLine.quote).toBe(note);
    expect(stopLine.attempt).toBe(1);
  });

  test("a session's own Abort keeps its line, and a pick-up from the stopped notice is still a pick-up", () => {
    const lines = activityLines([
      ev(EventTypes.RunAborted, { reason: "Not this way." }, { runId: "run_i1" }),
      ev("task.recovered", { action: "resume", note: "Go on.", runIds: ["run_i1"] }),
    ], people, runs, [], 1, "GREE-2");
    expect(said(lines)).toEqual(["Ana Ribeiro aborted the implementer", "Ana Ribeiro picked the task back up: resumed where it stopped"]);
  });

  test("a Run a Stop named, resumed and later aborted from its session keeps that Abort's line", () => {
    const lines = activityLines([
      aborted("run_c1", "hold"), ev("task.stopped", { by: ANA, note: "hold", runIds: ["run_c1"], decider: "conductor" }),
      ev("task.recovered", { action: "resume", note: "go", runIds: ["run_c1"] }),
      ev(EventTypes.RunAborted, { reason: "Not this way." }, { runId: "run_c1" }),
    ], people, runs, [], 1, "GREE-2");
    expect(said(lines)).toEqual(["Ana Ribeiro stopped the task · stopped the conductor",
      "Ana Ribeiro picked the task back up: resumed where it stopped", "Ana Ribeiro aborted the conductor"]);
  });

  test("a Stop's abort of a Run no Stop names keeps its line", () => {
    const lines = activityLines([
      aborted("run_c1", "hold"), aborted("run_i1", "hold"),
      ev("task.stopped", { by: ANA, note: "hold", runIds: ["run_c1"], decider: "conductor" }),
    ], people, runs, [], 1, "GREE-2");
    expect(said(lines)).toEqual(["Ana Ribeiro aborted the implementer", "Ana Ribeiro stopped the task · stopped the conductor"]);
  });
});

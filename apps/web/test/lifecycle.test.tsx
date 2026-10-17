/**
 * Lifecycle in the web app (docs/design/tracking-work.md, Epic 1): the
 * Activity lines Close, Reopen, Archive and Unarchive write, and the one
 * line Close and archive's batch makes.
 */

import { describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { EventTypes, type PersistedEvent } from "@dude/domain";
import { activityLines } from "../src/screens/TaskScreen.tsx";
import { afterEach, beforeEach } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { act, mount, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PULL_REQUEST, TASK_ID } from "../src/fixtures/data.ts";
import { PeopleProvider } from "../src/people.tsx";
import { TaskScreen } from "../src/screens/TaskScreen.tsx";
import { ApiError, type TaskDetail } from "../src/api/client.ts";

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
  test("refuses a running task as the orchestrator does, by code", async () => {
    const client = new FixtureClient("a");
    const code = async (p: Promise<unknown>) => p.then(() => "ok", (e: unknown) => (e as ApiError).code);
    expect(await code(client.closeTask(TASK_ID, { resolution: "wont_do" }))).toBe("running");
    expect(await code(client.archiveTask(TASK_ID, { resolution: "wont_do" }))).toBe("running");
    expect(await code(client.reopenTask(TASK_ID))).toBe("conflict");
    expect(await code(client.unarchiveTask(TASK_ID))).toBe("conflict");
    expect((await client.archivedTasks(TASK_ID)).tasks).toEqual([]);
  });
});

describe("the task page's lifecycle", () => {
  test("a task in progress: Close… and Archive… are there, each disabled with Stop its agents first", async () => {
    const page = await taskPage(new FixtureClient("a"));
    const items = await menuItems(page);
    const close = items.find((i) => itemText(i).startsWith("Close"))!;
    const archive = items.find((i) => itemText(i).startsWith("Archive"))!;
    for (const i of [close, archive]) {
      expect(i.getAttribute("aria-disabled")).toBe("true");
      expect(itemText(i)).toContain("Stop its agents first");
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

  test("a stopped task with its pull request still open: Close… and Archive… are disabled, saying so", async () => {
    const client = await clientWith({ status: "aborted" });
    client.listPullRequests = async () => ({ pullRequests: [{ ...PULL_REQUEST, state: "open" }] });
    const page = await taskPage(client);
    const items = (await menuItems(page)).filter((i) => /^(Close|Archive)/.test(itemText(i)));
    expect(items.map(itemText)).toEqual(["Close…. It has a pull request open", "Archive…. It has a pull request open"]);
    for (const i of items) expect(i.getAttribute("aria-disabled")).toBe("true");
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

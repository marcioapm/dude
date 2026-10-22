/**
 * One Stop per task on its page: the header's Stop and the menu's Stop…
 * on a task in progress, whoever decides it; the dialog's two ways, what
 * each does to the task's pull request, and who decides the next attempt;
 * and session Abort's line pointing at it.
 */

import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { act, mount, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PULL_REQUEST, TASK_ID } from "../src/fixtures/data.ts";
import { PeopleProvider } from "../src/people.tsx";
import { TaskScreen } from "../src/screens/TaskScreen.tsx";
import { RunScreen } from "../src/screens/RunScreen.tsx";
import { ApiError, type StopRequest, type TaskDetail } from "../src/api/client.ts";
import { openPullRequestsOf } from "../src/screens/TaskStop.tsx";

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

/** A fixture client whose task is `patch`ed; its stops recorded. */
async function clientWith(patch: Partial<TaskDetail>, stops: StopRequest[] = []): Promise<FixtureClient> {
  const client = new FixtureClient("a");
  const base = await client.getTask(TASK_ID);
  const task: TaskDetail = { ...base, ...patch };
  client.getTask = async () => task;
  client.stopTask = async (_id, s) => {
    stops.push(s);
    return { taskId: task.id, status: s.andRestart ? "running" : "aborted" };
  };
  return client;
}

const menuItems = async (page: HTMLElement) => {
  const trigger = page.querySelector<HTMLElement>('[aria-label="Task actions"]')!;
  await act(async () => {
    trigger.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" }));
  });
  return [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')];
};

const pick = async (dialog: HTMLElement, label: string) =>
  act(async () => [...dialog.querySelectorAll<HTMLElement>('[role="radio"]')].find((r) => r.textContent?.startsWith(label))!.click());

describe("Stop on the task's header and menu", () => {
  for (const decider of ["policy", "conductor"] as const) {
    test(`a task in progress decided by ${decider}: Stop in the header, Stop… first in the menu`, async () => {
      const page = await taskPage(await clientWith({ status: "running", decider }));
      const stop = page.querySelector<HTMLButtonElement>('[data-testid="task-stop"]')!;
      expect(stop.textContent).toBe("Stop");
      // Quiet with danger ink, as the header's other actions are quiet.
      expect([stop.getAttribute("data-variant"), stop.getAttribute("data-weight")]).toEqual(["danger", "quiet"]);
      expect(stop.querySelector('svg[data-icon="stop"]')).not.toBeNull();
      // Before Edit, in the header's actions.
      expect(stop.compareDocumentPosition(page.querySelector('[data-testid="edit-task"]')!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
      const items = await menuItems(page);
      expect(items[0]!.textContent).toContain("Stop…");
      expect(items[0]!.getAttribute("aria-disabled")).toBeNull();
    });
  }

  test("not in progress: no Stop anywhere", async () => {
    const page = await taskPage(await clientWith({ status: "received", runs: [] }));
    expect(page.querySelector('[data-testid="task-stop"]')).toBeNull();
    expect((await menuItems(page)).some((i) => i.textContent?.includes("Stop…"))).toBe(false);
  });

  test("a member: Stop is there, disabled, with whose decision it is", async () => {
    const client = await clientWith({ status: "running", owner: { id: "u_ana", name: "Ana Ribeiro", photoUrl: null, online: false } });
    const listed = await client.listPeople();
    client.listPeople = async () => ({ ...listed, people: listed.people.map((p) => (p.id === listed.you ? { ...p, role: "member" as const } : p)) });
    const page = await taskPage(client);
    const stop = await until(() => page.querySelector<HTMLElement>('[data-testid="task-stop"][data-refused]'), "the refused Stop");
    expect(stop.getAttribute("aria-disabled")).toBe("true");
    expect(document.getElementById(stop.getAttribute("aria-describedby")!)!.textContent).toBe("Only Ana, the owner, or an admin can do this");
    const item = (await menuItems(page)).find((i) => i.textContent?.includes("Stop…"))!;
    expect(item.getAttribute("aria-disabled")).toBe("true");
    expect(item.textContent).toContain("Only Ana, the owner, or an admin can do this");
  });
});

describe("the Stop dialog", () => {
  const open = async (patch: Partial<TaskDetail>, prs: Array<typeof PULL_REQUEST> = [], stops: StopRequest[] = []) => {
    const client = await clientWith({ status: "running", ...patch }, stops);
    client.listPullRequests = async () => ({ pullRequests: prs });
    const page = await taskPage(client);
    await act(async () => page.querySelector<HTMLElement>('[data-testid="task-stop"]')!.click());
    return until(() => document.querySelector<HTMLElement>('[data-testid="stop-dialog"]'), "the Stop dialog");
  };
  const shownEffects = (dialog: HTMLElement) =>
    [...dialog.querySelectorAll('.pickUpWay[data-shown="true"] .pickUpEffects li')].map((li) => li.textContent ?? "");

  test("lists what is live; Stop here keeps it all and the PR open, and sends the note", async () => {
    const stops: StopRequest[] = [];
    const dialog = await open({ decider: "conductor" }, [{ ...PULL_REQUEST, state: "open", number: 412 }], stops);
    expect(dialog.closest('[role="dialog"]')!.textContent).toContain("Stop WC-214?");
    expect(dialog.querySelector('[data-testid="live-work"] [aria-label="Live now"]')!.children.length).toBeGreaterThan(0);
    const effects = shownEffects(dialog);
    expect(effects.some((e) => e.startsWith("Conductor") && e.includes("the same conductor goes on"))).toBe(true);
    expect(effects.some((e) => e.startsWith("Pull request") && e.includes("#412 stays open"))).toBe(true);
    await act(async () => {
      const note = dialog.querySelector<HTMLTextAreaElement>('[data-testid="stop-note"] textarea, textarea[data-testid="stop-note"]')!;
      const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
      set.call(note, "Wrong plan");
      note.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const confirm = document.querySelector<HTMLButtonElement>('[data-testid="stop-confirm"]')!;
    expect(confirm.textContent).toBe("Stop");
    await act(async () => confirm.click());
    expect(stops).toEqual([{ note: "Wrong plan" }]);
  });

  test("Stop here says every agent at work stops, the conductor included, under Deliver too", async () => {
    const dialog = await open({ decider: "policy" });
    const here = [...dialog.querySelectorAll<HTMLElement>('[role="radio"]')].find((r) => r.textContent?.startsWith("Stop here"))!;
    expect(here.textContent).toContain("Everything stops: every agent at work, the conductor included.");
  });

  test("a refused Stop keeps the dialog open and says why in its footer", async () => {
    const client = await clientWith({ status: "running" });
    client.stopTask = async () => {
      throw new ApiError(409, "not_in_progress", "task wi_1 is aborted: only a task in progress can be stopped");
    };
    const page = await taskPage(client);
    await act(async () => page.querySelector<HTMLElement>('[data-testid="task-stop"]')!.click());
    await until(() => document.querySelector('[data-testid="stop-dialog"]'), "the Stop dialog");
    await act(async () => document.querySelector<HTMLButtonElement>('[data-testid="stop-confirm"]')!.click());
    const said = await until(() => [...document.querySelectorAll<HTMLElement>('[role="dialog"] *')]
      .find((e) => e.children.length === 0 && e.textContent === "task wi_1 is aborted: only a task in progress can be stopped") ?? null, "the refusal in the footer");
    expect(said.closest('[role="dialog"]')).not.toBeNull();
    expect(document.querySelector('[data-testid="stop-dialog"]')).not.toBeNull();
  });

  test("Stop and start attempt 2: who decides defaults to now; dude's PR is closed; the decider is sent", async () => {
    const stops: StopRequest[] = [];
    const dialog = await open({ decider: "conductor" }, [{ ...PULL_REQUEST, state: "open", number: 412 }], stops);
    await pick(dialog, "Stop and start attempt 2");
    const field = dialog.querySelector('.pickUpWay[data-shown="true"] [data-testid="decider-field"]')!;
    expect(field.getAttribute("data-value")).toBe("conductor");
    expect(field.querySelector('[role="group"]')!.getAttribute("aria-label")).toBe("Who decides attempt 2");
    expect(shownEffects(dialog).some((e) => e.startsWith("Pull request") && e.includes("closed") && e.includes("#412, opened by dude"))).toBe(true);
    await act(async () => [...field.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.startsWith("Deliver"))!.click());
    const confirm = document.querySelector<HTMLButtonElement>('[data-testid="stop-confirm"]')!;
    expect(confirm.textContent).toBe("Stop and start attempt 2");
    await act(async () => confirm.click());
    expect(stops).toEqual([{ andRestart: true, decider: "policy", note: "" }]);
  });

  test("an adopted PR: attempt 2 continues on it, and it stays open", async () => {
    const source = { kind: "pr" as const, number: 412, headBranch: "marcio/pause-split", baseBranch: "main", repositoryId: "repo_wc", repository: "web-console",
      title: "Pause split", mode: "review" as const };
    const dialog = await open({ decider: "policy", source } as Partial<TaskDetail>, [{ ...PULL_REQUEST, state: "open", number: 412 }]);
    await pick(dialog, "Stop and start attempt 2");
    expect([...dialog.querySelectorAll('[role="radio"]')].find((r) => r.textContent?.startsWith("Stop and start"))!.textContent)
      .toContain("A fresh start on PR #412's branch");
    const effects = shownEffects(dialog);
    expect(effects.some((e) => e.startsWith("Pull request") && e.includes("#412 stays open — attempt 2 continues on it"))).toBe(true);
    expect(effects.some((e) => e.startsWith("Branch") && e.includes("marcio/pause-split"))).toBe(true);
    expect(dialog.querySelector('.pickUpWay[data-shown="true"] [data-testid="decider-field"]')!.getAttribute("data-value")).toBe("deliver");
  });

  test("two open PRs: Stop here keeps both; Stop and start closes dude's both, naming each", async () => {
    const dialog = await open({ decider: "conductor" }, [
      { ...PULL_REQUEST, id: "pr_a", state: "open", number: 412 }, { ...PULL_REQUEST, id: "pr_b", state: "draft", number: 415 }]);
    expect(shownEffects(dialog).filter((e) => e.startsWith("Pull request")))
      .toEqual(["Pull requestkept#412 and #415 stay open; Resume carries on with them."]);
    await pick(dialog, "Stop and start attempt 2");
    expect(shownEffects(dialog).filter((e) => e.startsWith("Pull request")))
      .toEqual(["Pull requestclosed#412 and #415, opened by dude on attempt 1's branch, are closed. Attempt 2 opens its own."]);
  });

  test("an adopted PR beside one dude opened: attempt 2 carries on the adopted one and closes dude's", async () => {
    const source = { kind: "pr" as const, number: 412, headBranch: "marcio/pause-split", baseBranch: "main", repositoryId: PULL_REQUEST.repositoryId,
      repository: "web-console", title: "Pause split", mode: "review" as const };
    const dialog = await open({ decider: "policy", source } as Partial<TaskDetail>, [
      { ...PULL_REQUEST, id: "pr_a", state: "open", number: 412 }, { ...PULL_REQUEST, id: "pr_b", state: "open", number: 415 }]);
    await pick(dialog, "Stop and start attempt 2");
    const prs = shownEffects(dialog).filter((e) => e.startsWith("Pull request"));
    expect(prs).toEqual([
      "Pull requestkept#412 stays open — attempt 2 continues on it. Its author opened it; dude never closes it.",
      "Pull requestclosed#415, opened by dude on attempt 1's branch, is closed. Attempt 2 opens its own.",
    ]);
  });

  test("no PR: no Pull request row either way", async () => {
    const dialog = await open({ decider: "policy" });
    expect(shownEffects(dialog).some((e) => e.startsWith("Pull request"))).toBe(false);
    await pick(dialog, "Stop and start attempt 2");
    expect(shownEffects(dialog).some((e) => e.startsWith("Pull request"))).toBe(false);
  });
});

describe("session Abort", () => {
  test("says Stop is on the task, and its link opens the task's Stop", async () => {
    const client = new FixtureClient("a");
    let opened = 0;
    const { unmount } = await mount(<TooltipProvider><ToastProvider><PeopleProvider client={client}>
      <RunScreen client={client} runId={(await client.getTask(TASK_ID)).runs[0]!.id} onBack={() => {}} stopTask={{ key: "WC-214", open: () => opened++ }} />
    </PeopleProvider></ToastProvider></TooltipProvider>);
    mounted.push(unmount);
    const abort = await until(() => document.querySelector<HTMLElement>('[data-testid="abort"]'), "Abort…");
    await act(async () => abort.click());
    const hint = await until(() => document.querySelector<HTMLElement>('[data-testid="abort-stop-hint"]'), "the hint");
    expect(hint.textContent).toContain("To stop the whole task, or start it over, use Stop on the task.");
    const link = hint.querySelector<HTMLElement>('[data-testid="abort-stop-task"]')!;
    expect(link.textContent).toBe("Stop WC-214…");
    await act(async () => link.click());
    expect(opened).toBe(1);
  });
});

describe("the pull requests a Close would touch", () => {
  const source = { kind: "pr", repositoryId: "repo_wc", number: 9 } as unknown as TaskDetail["source"];
  test("every open one, the adopted one by its repository and number", () => {
    const prs = [
      { ...PULL_REQUEST, id: "a", number: 12, state: "open" as const },
      { ...PULL_REQUEST, id: "b", number: 9, state: "draft" as const },
      // Another repository's #9 is not the one the task was started from.
      { ...PULL_REQUEST, id: "c", number: 9, repositoryId: "repo_other", state: "open" as const },
      { ...PULL_REQUEST, id: "d", number: 3, state: "merged" as const },
    ];
    expect(openPullRequestsOf({ source }, prs)).toEqual([
      { number: 12, openedBy: "dude" }, { number: 9, openedBy: "someone" }, { number: 9, openedBy: "dude" }]);
  });
});

/**
 * Before the pull request in the Overview rail's "This task" block: the
 * owner changes it while the task runs and is told what Deliver will now
 * do; once a pull request is open the Select is disabled with the reason in
 * its name; anyone else reads the value and whose it is. Mounted in
 * happy-dom against the fixture client.
 */

import { afterEach, beforeEach, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import type { PullRequestChoice } from "@dude/domain";
import { act, click, mount, settle, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { TASK_ID } from "../src/fixtures/data.ts";
import type { TaskDetail } from "../src/api/client.ts";
import { PeopleProvider } from "../src/people.tsx";
import { TaskScreen } from "../src/screens/TaskScreen.tsx";

let mounted: Array<() => Promise<void>> = [];
beforeEach(() => localStorage.clear());
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
});

class Running extends FixtureClient {
  set: Array<PullRequestChoice | null> = [];
  constructor(private patch: Partial<TaskDetail>) {
    super("a");
  }
  override async getTask(id: string): Promise<TaskDetail> {
    return { ...(await super.getTask(id)), ...this.patch } as TaskDetail;
  }
  override async setPullRequestChoice(taskId: string, choice: PullRequestChoice | null) {
    this.set.push(choice);
    this.patch = { ...this.patch, pullRequestChoice: choice };
    return { taskId, pullRequestChoice: choice };
  }
}

async function overview(client: FixtureClient) {
  const { container, unmount } = await mount(
    <TooltipProvider><ToastProvider><PeopleProvider client={client}>
      <TaskScreen client={client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} />
    </PeopleProvider></ToastProvider></TooltipProvider>,
  );
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=task-facts]"), "the task's facts");
  await settle();
  return container;
}

const fact = (page: HTMLElement) => page.querySelector<HTMLElement>("[data-testid=task-facts]")!.parentElement!.querySelector<HTMLElement>("[data-state]")!;

test("the owner changes it while the task runs and is told what Deliver will do", async () => {
  const client = new Running({ status: "running", pullRequestChoice: null, projectPullRequest: "open", pullRequestOpened: false });
  const page = await overview(client);
  const f = fact(page);
  expect(f.getAttribute("data-state")).toBe("editable");
  expect(f.textContent).toContain("Before the pull request");
  expect(f.textContent).toContain("Project default (Open it)");
  expect(f.textContent).toContain("Read when Deliver reaches the pull request.");
  const trigger = f.querySelector<HTMLElement>("[role=combobox], button")!;
  await act(async () => void trigger.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
  const options = await until(() => (document.querySelectorAll("[role=option]").length === 4 ? document.querySelectorAll<HTMLElement>("[role=option]") : null), "the four values");
  await click([...options].find((o) => o.getAttribute("data-value") === "ask")!);
  await until(() => (client.set.length === 1 ? true : null), "the save");
  expect(client.set).toEqual(["ask"]);
  await until(() => document.body.textContent?.includes("Deliver will ask before opening the pull request.") || null, "the toast");
});

test("once a pull request is open it is disabled, its reason in its name", async () => {
  const client = new Running({ status: "review", pullRequestChoice: "ask", projectPullRequest: "open", pullRequestOpened: true });
  const page = await overview(client);
  const f = fact(page);
  expect(f.getAttribute("data-state")).toBe("locked");
  const trigger = f.querySelector<HTMLElement>("[aria-label]")!;
  expect(trigger.getAttribute("aria-label")).toBe("Before the pull request: Ask me first. The pull request is already open.");
  expect(trigger.hasAttribute("disabled") || trigger.getAttribute("data-disabled") !== null).toBe(true);
  const note = f.lastElementChild!;
  expect([note.textContent, note.getAttribute("aria-hidden")]).toEqual(["The pull request is already open.", "true"]);
});

test("anyone else reads the value, and whose it is", async () => {
  const client = new Running({ status: "running", pullRequestChoice: "draft", projectPullRequest: "open", pullRequestOpened: false,
    owner: { id: "u_sam", name: "Sam Lee", photoUrl: null, online: false } });
  const page = await overview(client);
  const f = fact(page);
  expect(f.getAttribute("data-state")).toBe("read-only");
  expect(f.querySelectorAll("[role=combobox]").length).toBe(0);
  expect(f.textContent).toContain("Open as draft");
  expect(f.textContent).toContain("Only Sam can change it.");
});

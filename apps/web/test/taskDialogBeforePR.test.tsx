/**
 * The task dialog's Before the pull request: its project's default first
 * (named with the project's value), then the three choices, the picked
 * one's line under it; a new task is created with the pick, an edit saves a
 * change of it, and once a pull request is open it is disabled with why.
 * Mounted in happy-dom against the fixture client.
 */

import { afterEach, expect, test } from "bun:test";
import type { PullRequestChoice } from "@dude/domain";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { act, click, mount, until } from "./dom.ts";
import type { Epic, ProjectDetail, Task } from "../src/api/client.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PROJECT } from "../src/fixtures/data.ts";
import { TaskDialog, type ExistingTask } from "../src/screens/TaskDialog.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
});

class Recording extends FixtureClient {
  created: unknown[] = [];
  set: Array<[string, PullRequestChoice | null]> = [];
  override listEpics(): Promise<{ epics: Epic[] }> {
    return Promise.resolve({ epics: [] });
  }
  override getProject(id: string): Promise<ProjectDetail> {
    return super.getProject(id).then((p) => ({ ...p, deliveryPolicy: { ...p.deliveryPolicy, pullRequest: "draft" } }));
  }
  override async createTask(input: Parameters<FixtureClient["createTask"]>[0]): Promise<Task> {
    this.created.push(input);
    return { id: "tsk_new" } as Task;
  }
  override async updateTask(): Promise<Task> {
    return { id: "tsk_1" } as Task;
  }
  override async setPullRequestChoice(taskId: string, choice: PullRequestChoice | null) {
    this.set.push([taskId, choice]);
    return { taskId, pullRequestChoice: choice };
  }
}

async function open(client: Recording, existing?: ExistingTask) {
  const { unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <TaskDialog client={client} projectId={PROJECT.id} existing={existing} onClose={() => {}} onSaved={() => {}} />
      </ToastProvider>
    </TooltipProvider>,
  );
  mounted.push(unmount);
  return until(() => document.querySelector<HTMLElement>("[data-testid=task-before-pr]"), "the field");
}

async function pick(trigger: HTMLElement, value: string) {
  await act(async () => void trigger.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
  const options = await until(() => (document.querySelectorAll("[role=option]").length === 4 ? document.querySelectorAll<HTMLElement>("[role=option]") : null), "four choices");
  expect([...options].map((o) => o.getAttribute("data-value"))).toEqual(["project", "open", "draft", "ask"]);
  await click([...options].find((o) => o.getAttribute("data-value") === value)!);
}

async function type(el: Element, proto: object, text: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, text);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

const existingTask = (extra: Partial<ExistingTask> = {}): ExistingTask => ({
  id: "tsk_1", fixed: null, title: "Saved filters", goal: "Let people save the filters they use.", acceptanceCriteria: [],
  epicId: null, repositories: [], pullRequestChoice: null, ...extra,
});

test("a new task follows its project unless the person picks, and is created with the pick", async () => {
  const client = new Recording("a");
  const trigger = await open(client);
  expect(trigger.textContent).toContain("Project default (Open as draft)");
  expect(document.body.textContent).toContain("CI still runs on most repositories");
  await pick(trigger, "ask");
  expect(document.body.textContent).toContain("Deliver stops for your review before the pull request");
  await type(document.querySelector("[data-testid=task-title]")!, HTMLInputElement.prototype, "Saved filters");
  await type(document.querySelector("[data-testid=task-goal]")!, HTMLTextAreaElement.prototype, "Let people save the filters they use.");
  await click(document.querySelector<HTMLButtonElement>("[data-testid=task-save]")!);
  await until(() => client.created.length > 0 || null, "the create");
  expect(client.created[0]).toMatchObject({ title: "Saved filters", pullRequestChoice: "ask" });
});

test("an edit saves a change of it, back to the project's as null", async () => {
  const client = new Recording("a");
  const trigger = await open(client, existingTask({ pullRequestChoice: "ask" }));
  expect(trigger.textContent).toContain("Ask me first");
  await pick(trigger, "project");
  await click(document.querySelector<HTMLButtonElement>("[data-testid=task-save]")!);
  await until(() => client.set.length > 0 || null, "the change");
  expect(client.set).toEqual([["tsk_1", null]]);
});

test("once a pull request is open the field is disabled and says why", async () => {
  const client = new Recording("a");
  const trigger = await open(client, existingTask({ choiceLocked: "The pull request is already open." }));
  expect(trigger.hasAttribute("disabled") || trigger.getAttribute("data-disabled") !== null).toBe(true);
  expect(document.body.textContent).toContain("The pull request is already open.");
});

/**
 * The task dialog saves only with a goal of at least TASK_GOAL_MIN
 * characters, trimmed, and says how many more once the person types in it.
 * Mounted in happy-dom against the fixture client.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { act, mount, until } from "./dom.ts";
import type { Epic } from "../src/api/client.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PROJECT } from "../src/fixtures/data.ts";
import { TaskDialog, type ExistingTask } from "../src/screens/TaskDialog.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
});

class EpicsClient extends FixtureClient {
  override listEpics(): Promise<{ epics: Epic[] }> {
    return Promise.resolve({ epics: [] });
  }
}

async function open(existing?: ExistingTask) {
  const { unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <TaskDialog client={new EpicsClient("a")} projectId={PROJECT.id} existing={existing} onClose={() => {}} onSaved={() => {}} />
      </ToastProvider>
    </TooltipProvider>,
  );
  mounted.push(unmount);
  // The dialog is portalled to the body; it can save once the project's choices have arrived.
  await until(() => document.querySelector("[data-testid=task-goal]") && document.body.textContent?.includes(PROJECT.name), "the dialog, loaded");
  const field = <T extends Element>(id: string) => document.querySelector<T>(`[data-testid=${id}]`)!;
  return {
    save: () => field<HTMLButtonElement>("task-save"),
    deliver: () => document.querySelector<HTMLButtonElement>("[data-testid=task-create-deliver]"),
    goalLabel: () => document.querySelector(`label[for="${field("task-goal").id}"]`)?.textContent ?? "",
    // The message under the field, as the editor names it in aria-describedby.
    goalText: () => document.getElementById(`${field("task-goal").id}-error`)?.textContent ?? "",
    title: (text: string) => set(field("task-title"), HTMLInputElement.prototype, text),
    goal: (text: string) => set(field("task-goal"), HTMLTextAreaElement.prototype, text),
  };
}

/** Type into a field as a person would: React reads the value through the prototype's setter. */
async function set(el: Element, proto: object, text: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, text);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

const hint = (n: number) => `${n} more ${n === 1 ? "character" : "characters"} to save`;

describe("a new task's goal", () => {
  test("Create and Create and deliver stay disabled below 16 characters and are enabled at 16", async () => {
    const d = await open();
    await d.title("Show invoices in euros");
    expect(d.save().disabled).toBe(true);
    expect(d.deliver()!.disabled).toBe(true);
    await d.goal("a".repeat(15));
    expect(d.save().disabled).toBe(true);
    expect(d.deliver()!.disabled).toBe(true);
    // Whitespace around it does not count.
    await d.goal(`   ${"a".repeat(15)}\n\n `);
    expect(d.save().disabled).toBe(true);
    await d.goal("a".repeat(16));
    expect(d.save().disabled).toBe(false);
    expect(d.deliver()!.disabled).toBe(false);
  });

  test("the Goal says it is required, and how many more characters only after the person types", async () => {
    const d = await open();
    expect(d.goalLabel()).toBe("Goal · required");
    expect(d.goalText()).not.toContain("more character");
    await d.goal("Euros");
    expect(d.goalText()).toContain(hint(11));
    await d.goal("a".repeat(15));
    expect(d.goalText()).toContain(hint(1));
    await d.goal("a".repeat(16));
    expect(d.goalText()).not.toContain("more character");
    // Cleared after typing: still says how much is missing.
    await d.goal("");
    expect(d.goalText()).toContain(hint(16));
  });
});

describe("editing a task saved before the rule", () => {
  const old = (delivering: boolean): ExistingTask => ({
    id: "task_old", delivering, title: "Old task", goal: "", acceptanceCriteria: [], epicId: null, repositories: [],
  });

  test("opens without complaint, and saves once its goal is long enough", async () => {
    const d = await open(old(false));
    expect(d.goalText()).not.toContain("more character");
    expect(d.save().disabled).toBe(true);
    await d.goal("Keep invoices in euros for EU customers.");
    expect(d.save().disabled).toBe(false);
  });

  test("once delivery has started the goal is neither required nor checked", async () => {
    const d = await open(old(true));
    expect(d.goalLabel()).toBe("Goal");
    expect(d.save().disabled).toBe(false);
  });
});

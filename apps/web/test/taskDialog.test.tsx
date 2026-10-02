/**
 * The task dialog saves only with a goal of at least TASK_GOAL_MIN
 * characters, trimmed, and says how many more once the person types in it.
 * A new task's images hold Create and deliver until they are made; an edit
 * takes no images. Mounted in happy-dom against the fixture client.
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

const hint = (n: number) =>
  `${n} more ${n === 1 ? "character" : "characters"} to save: why it matters and what should change.`;

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
    expect(d.goalText()).toBe(hint(11));
    await d.goal("a".repeat(15));
    expect(d.goalText()).toBe(hint(1));
    await d.goal("a".repeat(16));
    expect(d.goalText()).not.toContain("more character");
    // Cleared after typing: still says how much is missing.
    await d.goal("");
    expect(d.goalText()).toBe(hint(16));
  });
});

describe("editing a task saved before the rule", () => {
  const old = (delivering: boolean): ExistingTask => ({
    id: "task_old", delivering, title: "Old task", goal: "", acceptanceCriteria: [], epicId: null, repositories: [],
  });

  test("opens without complaint, and saves once its goal is long enough", async () => {
    const d = await open(old(false));
    expect(d.goalLabel()).toBe("Goal · required");
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

  test("a file dragged over it shows no drop overlay: an edit takes no images", async () => {
    await open(old(false));
    await act(async () => void fileEvent("dragenter", PNG));
    expect(document.querySelectorAll('[data-testid="drop-overlay"]').length).toBe(0);
  });
});

const PNG = new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0, 0, 0, 0, 0])], "design.png", { type: "image/png" });

/** A file drag event on the goal, carrying `file`. */
function fileEvent(type: string, file: File) {
  const e = new Event(type, { bubbles: true, cancelable: true }) as Event & { dataTransfer: unknown };
  e.dataTransfer = { types: ["Files"], items: [file], files: [file], dropEffect: "" };
  document.querySelector("[data-testid=task-goal]")!.dispatchEvent(e);
  return e;
}

describe("a new task's images", () => {
  test("Create and deliver waits for a dropped image to be made, and is enabled once it is", async () => {
    // Decoding is held until released, so the chip stays "uploading"; the canvas encodes a small PNG.
    let decoded!: () => void;
    const held = new Promise<void>((r) => (decoded = r));
    const g = globalThis as { createImageBitmap?: unknown };
    const canvas = Object.getPrototypeOf(document.createElement("canvas")) as Record<string, unknown>;
    const saved = { bitmap: g.createImageBitmap, getContext: canvas["getContext"], toBlob: canvas["toBlob"] };
    g.createImageBitmap = async () => {
      await held;
      return { width: 40, height: 30, close() {} };
    };
    canvas["getContext"] = () => ({ fillRect() {}, drawImage() {} });
    canvas["toBlob"] = (done: (b: Blob) => void, type: string) => done(new Blob([new Uint8Array(1024)], { type }));
    try {
      const d = await open();
      await d.title("Show invoices in euros");
      await d.goal("Keep invoices in euros for EU customers.");
      expect(d.deliver()!.disabled).toBe(false);
      await act(async () => void fileEvent("drop", PNG));
      const chip = () => document.querySelector("[data-testid=attachment-chip]");
      expect(chip()?.getAttribute("data-state")).toBe("uploading");
      expect(d.deliver()!.disabled).toBe(true);
      await act(async () => decoded());
      await until(() => chip()?.getAttribute("data-state") === "ready", "the chip, ready");
      expect(d.deliver()!.disabled).toBe(false);
    } finally {
      g.createImageBitmap = saved.bitmap;
      canvas["getContext"] = saved.getContext;
      canvas["toBlob"] = saved.toBlob;
    }
  });
});

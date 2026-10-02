/**
 * The task dialog saves only with a goal of at least TASK_GOAL_MIN
 * characters, trimmed, and says how many more once the person types in it.
 * A dropped image goes into the text where it was dropped, as a reference
 * once it is made; while delivery runs none can be added. Mounted in
 * happy-dom against the fixture client.
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
    id: "task_old", fixed: delivering ? "all" : null, title: "Old task", goal: "", acceptanceCriteria: [], epicId: null, repositories: [],
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

  test("while delivery runs, a file dragged over it says images are fixed, and the Attach button is off", async () => {
    await open(old(true));
    await act(async () => void fileEvent("dragenter", PNG));
    const overlay = document.querySelector('[data-testid="drop-overlay"]');
    expect(overlay?.getAttribute("data-refused")).toBe("true");
    expect(overlay?.textContent).toContain("Delivery is running, so what the task asks for is fixed, and its images too.");
    expect(document.querySelector<HTMLButtonElement>("[data-testid=task-attach]")!.disabled).toBe(true);
  });
});

const PNG = new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0, 0, 0, 0, 0])], "design.png", { type: "image/png" });

/** A file drag event on `target` (the goal), carrying `file`. */
function fileEvent(type: string, file: File, target = "task-goal") {
  const e = new Event(type, { bubbles: true, cancelable: true }) as Event & { dataTransfer: unknown };
  e.dataTransfer = { types: ["Files"], items: [file], files: [file], dropEffect: "" };
  document.querySelector(`[data-testid=${target}]`)!.dispatchEvent(e);
  return e;
}

/** A canvas that encodes a small PNG, its decoding held until `decoded` is called. */
function heldCanvas() {
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
  return {
    decoded: () => decoded(),
    restore() {
      g.createImageBitmap = saved.bitmap;
      canvas["getContext"] = saved.getContext;
      canvas["toBlob"] = saved.toBlob;
    },
  };
}

const goalValue = () => document.querySelector<HTMLTextAreaElement>("[data-testid=task-goal]")!.value;

describe("a new task's images", () => {
  test("a drop on the goal puts a placeholder at the caret, held saves until the image is made, then its reference", async () => {
    const canvas = heldCanvas();
    try {
      const d = await open();
      await d.title("Show invoices in euros");
      await d.goal("Keep invoices in euros. Like this: and done.");
      const area = document.querySelector<HTMLTextAreaElement>("[data-testid=task-goal]")!;
      area.setSelectionRange(34, 34);
      expect(d.deliver()!.disabled).toBe(false);
      await act(async () => void fileEvent("drop", PNG));
      await until(() => goalValue().includes("![Uploading design.png…]()"), "the placeholder");
      expect(goalValue()).toBe("Keep invoices in euros. Like this:\n\n![Uploading design.png…]()\n\nand done.");
      expect(d.deliver()!.disabled).toBe(true);
      expect(d.save().disabled).toBe(true);
      await act(async () => canvas.decoded());
      await until(() => !goalValue().includes("Uploading"), "the reference");
      expect(goalValue()).toMatch(/^Keep invoices in euros\. Like this:\n\n!\[design\.png\]\(attachment:att_local\d+\)\n\nand done\.$/);
      expect(d.deliver()!.disabled).toBe(false);
      // Plain Create is not held back by images any more.
      expect(d.save().disabled).toBe(false);
      // The tray is gone.
      expect(document.querySelector("[data-testid=task-images]")).toBeNull();
      expect(document.querySelector("[data-testid=attachment-chip]")).toBeNull();
    } finally {
      canvas.restore();
    }
  });

  test("a file that is not an image leaves no placeholder, and says why", async () => {
    const d = await open();
    await d.goal("Keep invoices in euros for EU customers.");
    await act(async () => void fileEvent("drop", new File(["%PDF-1.7"], "spec.pdf", { type: "application/pdf" })));
    await until(() => document.querySelector("[data-testid=task-image-refused]"), "the warning");
    expect(goalValue()).toBe("Keep invoices in euros for EU customers.");
    expect(document.querySelector("[data-testid=task-image-refused]")!.textContent).toContain("spec.pdf: only PNG, JPEG, WebP and GIF can be sent");
  });
});

describe("editing a task whose delivery stopped", () => {
  test("what it asks for can change, its repositories cannot, and they are not sent", async () => {
    const sent: unknown[] = [];
    class Recording extends EpicsClient {
      override updateTask(id: string, fields: Parameters<FixtureClient["updateTask"]>[1]) {
        sent.push(fields);
        return super.updateTask(id, fields);
      }
    }
    const { unmount } = await mount(
      <TooltipProvider>
        <ToastProvider>
          <TaskDialog client={new Recording("a")} projectId={PROJECT.id} onClose={() => {}} onSaved={() => {}}
            existing={{ id: "task_stopped", fixed: "repositories", title: "Stopped task", goal: "Keep invoices in euros for EU customers.",
              acceptanceCriteria: [], epicId: null, repositories: [] }} />
        </ToastProvider>
      </TooltipProvider>,
    );
    mounted.push(unmount);
    await until(() => document.querySelector("[data-testid=task-goal]") && document.body.textContent?.includes(PROJECT.name), "the dialog, loaded");
    expect(document.body.textContent).toContain("Its repositories are fixed.");
    const title = document.querySelector<HTMLInputElement>("[data-testid=task-title]")!;
    expect(title.disabled).toBe(false);
    await set(title, HTMLInputElement.prototype, "Stopped task, reworded");
    await act(async () => document.querySelector<HTMLButtonElement>("[data-testid=task-save]")!.click());
    await until(() => sent.length > 0, "the save");
    expect(sent[0]).toMatchObject({ title: "Stopped task, reworded" });
    expect(sent[0]).not.toHaveProperty("repositories");
  });
});

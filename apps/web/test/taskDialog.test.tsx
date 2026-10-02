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
import { ApiError, type Epic } from "../src/api/client.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PROJECT } from "../src/fixtures/data.ts";
import { TaskDialog, type ExistingTask } from "../src/screens/TaskDialog.tsx";
import { criteriaFromMarkdown } from "../src/screens/criteria.ts";

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

async function open(existing?: ExistingTask, client: FixtureClient = new EpicsClient("a")) {
  const { unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <TaskDialog client={client} projectId={PROJECT.id} existing={existing} onClose={() => {}} onSaved={() => {}} />
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

/** A file drag event on `target` (the goal), carrying `file` (or several). */
function fileEvent(type: string, file: File | File[], target = "task-goal") {
  const files = Array.isArray(file) ? file : [file];
  const e = new Event(type, { bubbles: true, cancelable: true }) as Event & { dataTransfer: unknown };
  e.dataTransfer = { types: ["Files"], items: files, files, dropEffect: "" };
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

describe("laying out a task's images in Preview", () => {
  const IMG = "![design.png](attachment:att_d1)";
  const task = (fixed: ExistingTask["fixed"], goal: string, criteria: string[] = []): ExistingTask => ({
    id: "task_lay", fixed, title: "Lay out", goal, acceptanceCriteria: criteria, epicId: null, repositories: [],
  });
  const preview = async (field: "task-goal" | "task-criteria") => {
    const panel = document.querySelector<HTMLElement>(`[data-testid=${field}-preview]`)!;
    const tab = document.getElementById(panel.getAttribute("aria-labelledby")!) as HTMLButtonElement;
    if (tab.getAttribute("aria-selected") !== "true") await act(async () => tab.click());
    return panel;
  };
  const figure = (field: string) => document.querySelector<HTMLElement>(`[data-testid=${field}-preview] [data-image-n="0"]`);
  const press = (label: string) => act(async () => document.querySelector<HTMLButtonElement>(`[data-image-toolbar] button[aria-label="${label}"]`)!.click());
  const criteriaValue = () => document.querySelector<HTMLTextAreaElement>("[data-testid=task-criteria]")!.value;

  test("Wrap right and Small write \"small right\" into the goal's source, and Save is enabled", async () => {
    const d = await open(task(null, `Keep invoices in euros for EU customers.\n\n${IMG}`));
    await preview("task-goal");
    await until(() => figure("task-goal"), "the image");
    await act(async () => figure("task-goal")!.click());
    await press("Wrap right");
    await press("Small");
    expect(goalValue()).toBe('Keep invoices in euros for EU customers.\n\n![design.png](attachment:att_d1 "small right")');
    expect(d.save().disabled).toBe(false);
  });

  test("Move up swaps it with the paragraph above", async () => {
    await open(task(null, `First paragraph here.\n\nSecond paragraph here.\n\n${IMG}`));
    await preview("task-goal");
    await until(() => figure("task-goal"), "the image");
    await act(async () => figure("task-goal")!.click());
    await press("Move up");
    expect(goalValue()).toBe(`First paragraph here.\n\n${IMG}\n\nSecond paragraph here.`);
  });

  test("a drag from the goal lands in a criterion as its continuation, and the criteria keep their number", async () => {
    await open(task(null, `Keep invoices in euros for EU customers.\n\n${IMG}`, ["One", "Two", "Three"]));
    await preview("task-goal");
    const crit = await preview("task-criteria");
    await until(() => figure("task-goal") && crit.querySelector("li"), "both previews");
    const items = [...crit.querySelectorAll<HTMLElement>("li")];
    items.forEach((li, i) => (li.getBoundingClientRect = () => ({ top: i * 20, bottom: i * 20 + 20, left: 0, right: 100, width: 100, height: 20, x: 0, y: i * 20, toJSON() {} })));
    const store = new Map<string, string>();
    const dataTransfer = { get types() { return [...store.keys()]; }, setData: (t: string, v: string) => void store.set(t, v), getData: (t: string) => store.get(t) ?? "", effectAllowed: "", dropEffect: "" };
    const dnd = (type: string, el: Element, clientY = 0) => {
      const e = new Event(type, { bubbles: true, cancelable: true }) as Event & { dataTransfer: unknown; clientY: number };
      e.dataTransfer = dataTransfer;
      e.clientY = clientY;
      el.dispatchEvent(e);
    };
    await act(async () => dnd("dragstart", figure("task-goal")!));
    await act(async () => dnd("dragover", crit, 40));
    await act(async () => dnd("drop", crit, 40));
    expect(goalValue()).toBe("Keep invoices in euros for EU customers.");
    expect(criteriaValue()).toBe(`- [ ] One\n- [ ] Two\n  ${IMG}\n- [ ] Three`);
    const parsed = criteriaFromMarkdown(criteriaValue());
    expect(parsed.items).toEqual(["One", `Two\n${IMG}`, "Three"]);
    expect(parsed.stray).toBe(false);
    expect(document.querySelector("[data-testid=task-criteria-count]")!.textContent).toBe("3 criteria");
  });

  test("while delivery runs: no toolbar, no handles, and a click does not select", async () => {
    await open(task("all", `Keep invoices in euros for EU customers.\n\n${IMG}`));
    const goal = document.querySelector<HTMLElement>("[data-testid=task-goal-preview]")!;
    await until(() => goal.querySelector('[data-testid="markdown-figure"]'), "the image");
    expect(figure("task-goal")).toBeNull();
    await act(async () => goal.querySelector<HTMLElement>('[data-testid="markdown-figure"]')!.click());
    expect(document.querySelector("[data-image-toolbar]")).toBeNull();
    expect(goal.querySelector("[draggable=true]")).toBeNull();
  });
});

/** Records each save call in order; a call named in `failing` rejects that many times first. */
class SaveRecorder extends EpicsClient {
  calls: Array<{ call: string; detail?: unknown }> = [];
  failing: Record<string, number> = {};
  #uploads = 0;
  #fail(call: string) {
    if ((this.failing[call] ?? 0) === 0) return;
    this.failing[call]!--;
    throw new ApiError(500, "internal", `${call} failed`);
  }
  override async createTask(input: Parameters<FixtureClient["createTask"]>[0]) {
    this.calls.push({ call: "createTask", detail: input });
    this.#fail("createTask");
    return { id: "task_new" } as Awaited<ReturnType<FixtureClient["createTask"]>>;
  }
  override async uploadAttachment(taskId: string, image: Parameters<FixtureClient["uploadAttachment"]>[1]) {
    this.calls.push({ call: "uploadAttachment", detail: taskId });
    this.#fail("uploadAttachment");
    return { id: `att_real${++this.#uploads}`, name: image.name } as Awaited<ReturnType<FixtureClient["uploadAttachment"]>>;
  }
  override async updateTask(id: string, fields: Parameters<FixtureClient["updateTask"]>[1]) {
    this.calls.push({ call: "updateTask", detail: { id, ...fields } });
    this.#fail("updateTask");
    return { id } as Awaited<ReturnType<FixtureClient["updateTask"]>>;
  }
  override async deliver(taskId: string) {
    this.calls.push({ call: "deliver", detail: taskId });
    this.#fail("deliver");
    return { workflowRunId: "wf_new", alreadyRunning: false };
  }
  names() {
    return this.calls.map((c) => c.call);
  }
}

describe("creating a task with images", () => {
  const GOAL = "Keep invoices in euros. Like this:";

  /** New task with `count` images dropped at the end of the goal, each made. */
  async function withImages(client: SaveRecorder, count = 1) {
    const canvas = heldCanvas();
    try {
      const d = await open(undefined, client);
      await d.title("Show invoices in euros");
      await d.goal(GOAL);
      const area = document.querySelector<HTMLTextAreaElement>("[data-testid=task-goal]")!;
      area.setSelectionRange(GOAL.length, GOAL.length);
      const files = Array.from({ length: count }, (_, i) => new File([PNG], `design${i}.png`, { type: "image/png" }));
      await act(async () => void fileEvent("drop", files));
      await act(async () => canvas.decoded());
      await until(() => (goalValue().match(/attachment:att_local/g) ?? []).length === count && !goalValue().includes("Uploading"), "the references");
      return d;
    } finally {
      canvas.restore();
    }
  }
  const press = async (button: HTMLButtonElement) => act(async () => button.click());
  const sentGoal = (c: SaveRecorder) => (c.calls.findLast((x) => x.call === "updateTask")!.detail as { goal: string }).goal;

  test("Create and deliver: the task, its image uploaded to it, one PATCH with the real id and every field, then the delivery", async () => {
    const client = new SaveRecorder("a");
    const d = await withImages(client);
    await press(d.deliver()!);
    await until(() => client.names().includes("deliver"), "the delivery");
    expect(client.names()).toEqual(["createTask", "uploadAttachment", "updateTask", "deliver"]);
    const patch = client.calls[2]!.detail as Record<string, unknown>;
    expect(patch).toMatchObject({ id: "task_new", title: "Show invoices in euros", acceptanceCriteria: [], epicId: null });
    expect(patch).toHaveProperty("repositories");
    expect(patch["goal"]).toBe(`${GOAL}\n\n![design0.png](attachment:att_real1)`);
  });

  test("a PATCH that fails is sent again on the retry, with no second task and no second upload", async () => {
    const client = new SaveRecorder("a");
    client.failing = { updateTask: 1 };
    const d = await withImages(client);
    await press(d.deliver()!);
    await until(() => document.body.textContent?.includes("updateTask failed"), "the problem");
    expect(client.names()).toEqual(["createTask", "uploadAttachment", "updateTask"]);
    await press(d.deliver()!);
    await until(() => client.names().includes("deliver"), "the delivery");
    expect(client.names()).toEqual(["createTask", "uploadAttachment", "updateTask", "updateTask", "deliver"]);
    expect(sentGoal(client)).toBe(`${GOAL}\n\n![design0.png](attachment:att_real1)`);
  });

  test("after a failed upload, a Create without the image saves the text as it now is", async () => {
    const client = new SaveRecorder("a");
    client.failing = { uploadAttachment: 1 };
    const d = await withImages(client);
    await press(d.save());
    await until(() => document.body.textContent?.includes("uploadAttachment failed"), "the problem");
    expect(client.names()).toEqual(["createTask", "uploadAttachment"]);
    await d.goal("Keep invoices in euros for every EU customer.");
    await press(d.save());
    await until(() => client.names().includes("updateTask"), "the PATCH");
    expect(client.names()).toEqual(["createTask", "uploadAttachment", "updateTask"]);
    expect(sentGoal(client)).toBe("Keep invoices in euros for every EU customer.");
    expect(sentGoal(client)).not.toContain("att_local");
  });

  test("more images than a task shows are refused before anything is created", async () => {
    const client = new SaveRecorder("a");
    const d = await withImages(client, 7);
    await press(d.deliver()!);
    await until(() => document.body.textContent?.includes("A task shows at most 6 images; this one shows 7."), "the refusal");
    expect(client.calls).toEqual([]);
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
    // Its text can change, so its images can too.
    expect(document.querySelector<HTMLButtonElement>("[data-testid=task-attach]")!.disabled).toBe(false);
    await act(async () => void fileEvent("dragenter", PNG));
    const overlay = document.querySelector('[data-testid="drop-overlay"]');
    expect(overlay).not.toBeNull();
    expect(overlay!.getAttribute("data-refused")).not.toBe("true");
    await act(async () => void fileEvent("dragleave", PNG));
    const title = document.querySelector<HTMLInputElement>("[data-testid=task-title]")!;
    expect(title.disabled).toBe(false);
    await set(title, HTMLInputElement.prototype, "Stopped task, reworded");
    await act(async () => document.querySelector<HTMLButtonElement>("[data-testid=task-save]")!.click());
    await until(() => sent.length > 0, "the save");
    expect(sent[0]).toMatchObject({ title: "Stopped task, reworded" });
    expect(sent[0]).not.toHaveProperty("repositories");
  });
});

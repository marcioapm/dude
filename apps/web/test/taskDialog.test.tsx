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
import { imageDrag, stackRects } from "../../../packages/design-system/test/imageDrag.ts";

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

async function open(existing?: ExistingTask, client: FixtureClient = new EpicsClient("a"), onClose: () => void = () => {}) {
  const { unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <TaskDialog client={client} projectId={PROJECT.id} existing={existing} onClose={onClose} onSaved={() => {}} />
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
      expect(document.querySelectorAll("[data-testid=task-images]").length).toBe(0);
      expect(document.querySelectorAll("[data-testid=attachment-chip]").length).toBe(0);
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
    stackRects(crit.querySelectorAll("li"), 20, 20);
    const dnd = imageDrag();
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

  test("a drag from the goal to the criteria, listed as Files as Chrome does, raises no drop overlay", async () => {
    await open(task(null, `Keep invoices in euros for EU customers.\n\n${IMG}`, ["One", "Two", "Three"]));
    await preview("task-goal");
    const crit = await preview("task-criteria");
    await until(() => figure("task-goal") && crit.querySelector("li"), "both previews");
    stackRects(crit.querySelectorAll("li"), 20, 20);
    const overlays = () => document.querySelectorAll('[data-testid="drop-overlay"]').length;
    const dnd = imageDrag({ Files: "" });
    const goal = figure("task-goal")!;
    await act(async () => dnd("dragstart", goal));
    for (const [type, el] of [["dragenter", goal], ["dragleave", goal], ["dragenter", crit], ["dragover", crit]] as const) {
      await act(async () => dnd(type, el, 40));
      expect(overlays()).toBe(0);
    }
    await act(async () => dnd("drop", crit, 40));
    expect(criteriaValue()).toBe(`- [ ] One\n- [ ] Two\n  ${IMG}\n- [ ] Three`);
    expect(overlays()).toBe(0);
  });

  test("Esc on a selected image deselects it, and the dialog stays open", async () => {
    let closed = 0;
    await open(task(null, `Keep invoices in euros for EU customers.\n\n${IMG}`), new EpicsClient("a"), () => closed++);
    await preview("task-goal");
    await until(() => figure("task-goal"), "the image");
    await act(async () => figure("task-goal")!.click());
    expect(document.querySelector("[data-image-toolbar]")).not.toBeNull();
    await act(async () => void figure("task-goal")!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true })));
    expect(document.querySelectorAll("[data-image-toolbar]").length).toBe(0);
    expect(closed).toBe(0);
    expect(document.querySelectorAll("[role=alertdialog]").length).toBe(0);
    expect(document.querySelector("[data-testid=task-goal]")).not.toBeNull();
  });

  test("Open shows the viewer, and one Esc closes it", async () => {
    let closed = 0;
    class WithImage extends EpicsClient {
      override attachment(): Promise<Blob> {
        return Promise.resolve(new Blob([PNG], { type: "image/png" }));
      }
    }
    await open(task(null, `Keep invoices in euros for EU customers.\n\n${IMG}`), new WithImage("a"), () => closed++);
    await preview("task-goal");
    await until(() => figure("task-goal")?.querySelector("[data-testid=markdown-image]"), "the image");
    await act(async () => figure("task-goal")!.click());
    await press("Open");
    const viewer = await until(() => document.querySelector<HTMLElement>("[data-testid=image-viewer]"), "the viewer");
    await act(async () => void (document.activeElement ?? viewer).dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true })));
    await until(() => !document.querySelector("[data-testid=image-viewer]"), "the viewer closed");
    expect(closed).toBe(0);
  });

  test("while delivery runs: no toolbar, no handles, and a click does not select", async () => {
    await open(task("all", `Keep invoices in euros for EU customers.\n\n${IMG}`));
    const goal = document.querySelector<HTMLElement>("[data-testid=task-goal-preview]")!;
    await until(() => goal.querySelector('[data-testid="markdown-figure"]'), "the image");
    expect(document.querySelectorAll('[data-testid=task-goal-preview] [data-image-n="0"]').length).toBe(0);
    await act(async () => goal.querySelector<HTMLElement>('[data-testid="markdown-figure"]')!.click());
    expect(document.querySelectorAll("[data-image-toolbar]").length).toBe(0);
    expect(goal.querySelectorAll("[draggable=true]").length).toBe(0);
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

  test("created from a pull request, its upload failed, then switched to New work: the retry clears the stored source", async () => {
    /** Keeps the task's source as the API does: set by the POST, changed by a PATCH that names it, kept by one that does not. */
    class Stored extends SaveRecorder {
      source: unknown = undefined;
      lookups: Array<string | undefined> = [];
      override checkTaskSource(_projectId: string, source: Parameters<FixtureClient["checkTaskSource"]>[1], taskId?: string) {
        this.lookups.push(taskId);
        return Promise.resolve({ kind: "pr" as const, repositoryId: source.repositoryId, repository: "web-console", number: 12,
          title: "Greet in Portuguese", headBranch: "feature/greet", baseBranch: "main" });
      }
      override async createTask(input: Parameters<FixtureClient["createTask"]>[0]) {
        const out = await super.createTask(input);
        this.source = input.source ?? null;
        return out;
      }
      override async updateTask(id: string, fields: Parameters<FixtureClient["updateTask"]>[1]) {
        const out = await super.updateTask(id, fields);
        if ("source" in fields) this.source = fields.source;
        return out;
      }
    }
    const client = new Stored("a");
    client.failing = { uploadAttachment: 1 };
    const d = await withImages(client);
    const kind = (label: string) => act(async () => {
      [...document.querySelectorAll<HTMLButtonElement>("[data-testid=task-source-kind] button")].find((b) => b.textContent === label)!.click();
    });
    await kind("Existing PR");
    await set(document.querySelector("[data-testid=task-source-pr]")!, HTMLInputElement.prototype, "#12");
    await until(() => document.querySelector("[data-testid=task-source-found]") && !d.save().disabled, "the pull request found");
    await press(d.save());
    await until(() => document.body.textContent?.includes("uploadAttachment failed"), "the problem");
    expect(client.names()).toEqual(["createTask", "uploadAttachment"]);
    expect(client.source).toMatchObject({ kind: "pr", pullRequest: "#12" });
    // A lookup now is of the task just created, whose own pull request this is.
    await set(document.querySelector("[data-testid=task-source-pr]")!, HTMLInputElement.prototype, "12");
    await until(() => client.lookups.length > 1, "a second lookup");
    expect(client.lookups.at(-1)).toBe("task_new");
    await kind("New work");
    await press(d.save());
    await until(() => client.names().includes("updateTask"), "the PATCH");
    expect(client.names()).toEqual(["createTask", "uploadAttachment", "uploadAttachment", "updateTask"]);
    expect(client.source).toBeNull();
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

describe("where a task starts from", () => {
  const MERGED = "pull request #7 is merged: only an open one can be adopted";
  /** Answers lookups as the backend would, and records what is created. */
  class Sources extends EpicsClient {
    created: unknown[] = [];
    asked: unknown[] = [];
    override checkTaskSource(_projectId: string, source: Parameters<FixtureClient["checkTaskSource"]>[1]) {
      this.asked.push(source);
      if (source.kind === "pr" && source.pullRequest === "7") return Promise.reject(new ApiError(422, "pr_merged", MERGED));
      if (source.kind === "pr") {
        return Promise.resolve({ kind: "pr" as const, repositoryId: source.repositoryId, repository: "web-console",
          number: Number(source.pullRequest.replace("#", "")), title: "Greet in Portuguese", headBranch: "feature/greet", baseBranch: "main" });
      }
      return Promise.resolve({ kind: "branch" as const, repositoryId: source.repositoryId, repository: "web-console",
        headBranch: source.branch, baseBranch: source.base ?? "main" });
    }
    override createTask(input: Parameters<FixtureClient["createTask"]>[0]) {
      this.created.push(input);
      return super.createTask(input);
    }
  }
  const pick = (label: string) => act(async () => {
    const group = document.querySelector("[data-testid=task-source-kind]")!;
    [...group.querySelectorAll("button")].find((b) => b.textContent === label)!.click();
  });
  const fieldError = (id: string) => {
    const el = document.querySelector(`[data-testid=${id}]`)!;
    return el.getAttribute("aria-invalid") === "true" ? document.getElementById(`${el.id}-hint`)?.textContent ?? "" : "";
  };
  /** Types words into a branch picker's search, and picks the option named. */
  const pickBranch = async (id: string, words: string, branch: string) => {
    const search = document.querySelector<HTMLInputElement>(`[data-testid=${id}] [role=combobox]`)!;
    await set(search, HTMLInputElement.prototype, words);
    await until(() => [...document.querySelectorAll(`[data-testid=${id}] [role=option]`)].some((o) => o.textContent === branch),
      `${branch} offered`);
    const option = [...document.querySelectorAll(`[data-testid=${id}] [role=option]`)].find((o) => o.textContent === branch)!;
    await act(async () => option.dispatchEvent(new MouseEvent("mousedown", { bubbles: true })));
  };

  test("new work is the default, and creating sends no source", async () => {
    const client = new Sources("a");
    const d = await open(undefined, client);
    const pressed = document.querySelector("[data-testid=task-source-kind] [aria-pressed=true]");
    expect(pressed?.textContent).toBe("New work");
    await d.title("Show invoices in euros");
    await d.goal("Keep invoices in euros for EU customers.");
    await act(async () => d.save().click());
    await until(() => client.created.length > 0, "the create");
    expect(client.created[0]).not.toHaveProperty("source");
    expect(client.asked.length).toBe(0);
  });

  test("an existing pull request is looked up, shows its title and head → base, and is created with it", async () => {
    const client = new Sources("a");
    const d = await open(undefined, client);
    await d.title("Take over the greeting");
    await d.goal("Finish the Portuguese greeting already open.");
    await pick("Existing PR");
    expect(d.save().disabled).toBe(true);
    await set(document.querySelector("[data-testid=task-source-pr]")!, HTMLInputElement.prototype, "#12");
    await until(() => document.querySelector("[data-testid=task-source-found]"), "the pull request found");
    expect(document.querySelector("[data-testid=task-source-found]")!.textContent).toBe("#12 Greet in Portuguese · feature/greet → main");
    expect(d.save().disabled).toBe(false);
    await act(async () => d.save().click());
    await until(() => client.created.length > 0, "the create");
    expect(client.created[0]).toMatchObject({ source: { kind: "pr", pullRequest: "#12" } });
  });

  test("a refused pull request says why on its field, and Create stays off", async () => {
    const client = new Sources("a");
    const d = await open(undefined, client);
    await d.title("Take over the greeting");
    await d.goal("Finish the Portuguese greeting already open.");
    await pick("Existing PR");
    await set(document.querySelector("[data-testid=task-source-pr]")!, HTMLInputElement.prototype, "7");
    await until(() => fieldError("task-source-pr") === MERGED, "the refusal");
    expect(d.save().disabled).toBe(true);
    expect(document.querySelectorAll("[data-testid=task-source-found]").length).toBe(0);
  });

  test("an existing branch and its base are picked from GitHub's branches", async () => {
    const client = new Sources("a");
    const d = await open(undefined, client);
    await d.title("Take over the stack");
    await d.goal("Finish the stacked branch on top of the base one.");
    await pick("Existing branch");
    await pickBranch("task-source-branch", "top", "stack/top");
    await pickBranch("task-source-base", "base", "stack/base");
    expect(document.querySelector<HTMLInputElement>("[data-testid=task-source-branch-value]")!.value).toBe("stack/top");
    await until(() => document.querySelector("[data-testid=task-source-found]")?.textContent?.includes("stack/top → stack/base"), "the branch found");
    await act(async () => d.save().click());
    await until(() => client.created.length > 0, "the create");
    expect(client.created[0]).toMatchObject({ source: { kind: "branch", branch: "stack/top", base: "stack/base" } });
  });

  test("a repository dude may not push to is not offered, and none at all says why", async () => {
    class NoWrite extends Sources {
      override sourceRepositories() {
        return Promise.resolve({ repositories: [{ id: "repo_wc", name: "web-console", writable: false, reason: "Git push access denied" }] });
      }
    }
    const client = new NoWrite("a");
    const d = await open(undefined, client);
    await d.title("Take over the stack");
    await d.goal("Finish the stacked branch on top of the base one.");
    await pick("Existing branch");
    await until(() => document.querySelector("[data-testid=task-source-access]"), "the refusal");
    expect(document.querySelector("[data-testid=task-source-access]")!.textContent).toContain("Git push access denied");
    expect(document.querySelectorAll("[data-testid=task-source-branch]").length).toBe(0);
    expect(d.save().disabled).toBe(true);
  });

  test("a lookup answered after a newer one is dropped, and the name typed stays", async () => {
    let release: (() => void) | undefined;
    class Slow extends Sources {
      override checkTaskSource(projectId: string, source: Parameters<FixtureClient["checkTaskSource"]>[1]) {
        if (source.kind === "pr" && source.pullRequest === "12") {
          return new Promise<never>((_, reject) => {
            release = () => reject(new ApiError(422, "pr_merged", MERGED));
          });
        }
        return super.checkTaskSource(projectId, source);
      }
    }
    const client = new Slow("a");
    const d = await open(undefined, client);
    await d.title("Take over the greeting");
    await d.goal("Finish the Portuguese greeting already open.");
    await pick("Existing PR");
    const field = document.querySelector<HTMLInputElement>("[data-testid=task-source-pr]")!;
    await set(field, HTMLInputElement.prototype, "12");
    await until(() => release !== undefined, "the first lookup");
    await set(field, HTMLInputElement.prototype, "13");
    await until(() => document.querySelector("[data-testid=task-source-found]")?.textContent?.includes("#13"), "the second answer");
    await act(async () => release!());
    await act(async () => new Promise((r) => setTimeout(r, 20)));
    expect(fieldError("task-source-pr")).toBe("");
    expect(field.value).toBe("13");
    expect(document.querySelector("[data-testid=task-source-found]")!.textContent).toContain("#13");
  });

  test("once delivery has started, where it starts from is shown and cannot change", async () => {
    const client = new Sources("a");
    await open({ id: "task_adopted", fixed: "all", title: "Adopted", goal: "Finish the Portuguese greeting already open.",
      acceptanceCriteria: [], epicId: null, repositories: [{ id: "repo_wc", access: "write" }],
      savedSource: { kind: "pr", repositoryId: "repo_wc", repository: "web-console", number: 12, title: "Greet", headBranch: "feature/greet",
        baseBranch: "main" } }, client);
    const kind = document.querySelector("[data-testid=task-source-kind]")!;
    expect(kind.querySelector("[aria-pressed=true]")?.textContent).toBe("Existing PR");
    expect([...kind.querySelectorAll("button")].every((b) => b.disabled)).toBe(true);
    expect(document.querySelector<HTMLInputElement>("[data-testid=task-source-pr]")!.disabled).toBe(true);
    expect(document.body.textContent).toContain("where it started from is fixed");
    expect(document.querySelector("[data-testid=task-source-saved]")?.textContent).toBe("#12 Greet · feature/greet → main");
    expect(client.asked.length).toBe(0);
  });

  test("a started branch shows its saved head → base", async () => {
    const client = new Sources("a");
    await open({ id: "task_adopted", fixed: "all", title: "Adopted", goal: "Finish the stacked branch on top of the base one.",
      acceptanceCriteria: [], epicId: null, repositories: [{ id: "repo_wc", access: "write" }],
      savedSource: { kind: "branch", repositoryId: "repo_wc", repository: "web-console", headBranch: "stack/top", baseBranch: "stack/base" } }, client);
    expect(document.querySelector("[data-testid=task-source-saved]")?.textContent).toBe("stack/top → stack/base");
    expect(client.asked.length).toBe(0);
  });

  test("the source's repository set to Reads it says so, and Save stays off until it changes it again", async () => {
    class TwoRepos extends Sources {
      override getProject(id: string) {
        return super.getProject(id).then((p) => ({ ...p, repositories: [...p.repositories,
          { ...p.repositories[0]!, id: "repo_api", name: "api" }] }));
      }
      override sourceRepositories() {
        return Promise.resolve({ repositories: [{ id: PROJECT.repositories[0]!.id, name: PROJECT.repositories[0]!.name, writable: true },
          { id: "repo_api", name: "api", writable: true }] });
      }
    }
    const client = new TwoRepos("a");
    const repoId = PROJECT.repositories[0]!.id;
    const d = await open({ id: "task_draft", fixed: null, title: "Take over", goal: "Finish the Portuguese greeting already open.",
      acceptanceCriteria: [], epicId: null, repositories: [{ id: repoId, access: "write" }, { id: "repo_api", access: "write" }],
      savedSource: { kind: "pr", repositoryId: repoId, repository: PROJECT.repositories[0]!.name, number: 12, title: "Greet",
        headBranch: "feature/greet", baseBranch: "main" } }, client);
    await d.title("Take over the greeting");
    await until(() => !d.save().disabled, "Save on");
    const access = document.querySelector(`[aria-label="What the work does in ${PROJECT.repositories[0]!.name}"]`)!;
    const choose = async (label: string) => {
      await act(async () => access.dispatchEvent(new MouseEvent("click", { bubbles: true })));
      await act(async () => access.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true })));
      const option = await until(() => [...document.querySelectorAll("[role=option]")].find((o) => o.textContent === label), label);
      await act(async () => (option as HTMLElement).click());
    };
    await choose("Reads it");
    await until(() => document.querySelector("[data-testid=task-source-read]"), "the contradiction named");
    expect(document.querySelector("[data-testid=task-source-read]")!.textContent)
      .toBe(`This task only reads ${PROJECT.repositories[0]!.name}, which it starts from: set it to Changes it, or start from New work.`);
    expect(d.save().disabled).toBe(true);
    await choose("Changes it");
    await until(() => !document.querySelector("[data-testid=task-source-read]") && !d.save().disabled, "Save on again");
  });
});

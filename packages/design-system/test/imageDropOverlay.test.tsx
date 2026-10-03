/**
 * The drop zone's overlay is for files from outside the page: an image
 * repositioned in Preview never raises it, though Chrome lists its drag as
 * "Files" too, and once raised it goes however the drag ends.
 */

import { afterEach, expect, test } from "bun:test";
import { act, useState } from "react";
import { createRoot } from "react-dom/client";
import { AttachDropZone } from "../src/components/ImageAttachments.tsx";
import { MarkdownImage } from "../src/components/Markdown.tsx";
import { MarkdownEditor } from "../src/primitives/MarkdownEditor.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";
import { IMAGE_DRAG_TYPE } from "../src/util/imageDrag.ts";
import { imageDrag, stackRects } from "./imageDrag.ts";

const cleanups: Array<() => void> = [];
afterEach(() => {
  for (const c of cleanups.splice(0)) c();
});

const A = "![a.png](attachment:att_a)";

async function mount() {
  const got: File[][] = [];
  let goal = "";
  function Zone() {
    const [value, setValue] = useState(`One.\n\nTwo.\n\n${A}`);
    goal = value;
    return (
      <AttachDropZone onFiles={(f) => void got.push(f)}>
        <MarkdownEditor label="goal" value={value} onChange={setValue} defaultMode="preview" imageField="goal" data-testid="goal"
          attachmentImage={(id, alt) => <MarkdownImage src={`blob:${id}`} alt={alt} />} />
      </AttachDropZone>
    );
  }
  const container = document.createElement("div");
  document.body.appendChild(container);
  const root = createRoot(container);
  await act(async () => root.render(<TooltipProvider><Zone /></TooltipProvider>));
  cleanups.push(() => {
    act(() => root.unmount());
    container.remove();
  });
  return {
    got,
    goal: () => goal,
    overlay: () => container.querySelector('[data-testid="drop-overlay"]'),
    zone: container.querySelector<HTMLElement>("[class]")!,
    panel: container.querySelector<HTMLElement>('[data-testid="goal-preview"]')!,
    figure: () => container.querySelector<HTMLElement>('[data-testid="goal-preview"] [data-image-n="0"]')!,
  };
}

/** A drag event with `types` fixed, as Chrome reports them during dragenter (data unreadable). */
function fire(target: EventTarget, type: string, types: string[], extra: Record<string, unknown> = {}) {
  const e = new Event(type, { bubbles: true, cancelable: true }) as Event & Record<string, unknown>;
  e["dataTransfer"] = { types, items: types.map(() => ({})), files: [], dropEffect: "", getData: () => "" };
  Object.assign(e, extra);
  target.dispatchEvent(e);
}

test("an image dragged in Preview raises no drop overlay, during the drag or after the drop", async () => {
  const z = await mount();
  stackRects(z.panel.querySelector(":scope > div")!.children, 40, 30);
  const dnd = imageDrag({ Files: "" });
  await act(async () => dnd("dragstart", z.figure()));
  await act(async () => dnd("dragenter", z.figure()));
  await act(async () => dnd("dragenter", z.panel));
  await act(async () => dnd("dragover", z.panel, 38));
  expect(z.overlay()).toBeNull();
  await act(async () => dnd("drop", z.panel, 38));
  expect(z.goal()).toBe(`One.\n\n${A}\n\nTwo.`);
  expect(z.overlay()).toBeNull();
  await act(async () => void window.dispatchEvent(new Event("dragend")));
});

test("the image-move type alone keeps the overlay down", async () => {
  const z = await mount();
  await act(async () => fire(z.panel, "dragenter", ["Files", IMAGE_DRAG_TYPE]));
  expect(z.overlay()).toBeNull();
});

test("while an image drag runs, a drag listing only Files raises no overlay", async () => {
  const z = await mount();
  // Its dragstart marks the drag; types that do not carry the image-move type do not unmark it.
  await act(async () => fire(z.figure(), "dragstart", ["Files"], { dataTransfer: { types: ["Files"], setData() {}, effectAllowed: "" } }));
  await act(async () => fire(z.panel, "dragenter", ["Files"]));
  expect(z.overlay()).toBeNull();
  await act(async () => void z.figure().dispatchEvent(new Event("dragend", { bubbles: true })));
  await act(async () => fire(z.panel, "dragenter", ["Files"]));
  expect(z.overlay()).not.toBeNull();
});

test("a file from the desktop raises the overlay, and a dragend with no drop takes it down", async () => {
  const z = await mount();
  await act(async () => fire(z.zone, "dragenter", ["Files"]));
  await act(async () => fire(z.panel, "dragenter", ["Files"]));
  expect(z.overlay()?.textContent).toContain("Drop to attach");
  await act(async () => void window.dispatchEvent(new Event("dragend")));
  expect(z.overlay()).toBeNull();
});

test("a file dragged out of the window takes the overlay down; a leave to nothing inside it does not", async () => {
  const z = await mount();
  await act(async () => fire(z.zone, "dragenter", ["Files"]));
  await act(async () => fire(z.panel, "dragenter", ["Files"]));
  expect(z.overlay()).not.toBeNull();
  await act(async () => fire(z.panel, "dragleave", ["Files"], { relatedTarget: null, clientX: 50, clientY: 50 }));
  expect(z.overlay()).not.toBeNull();
  // Leaving the window: one dragleave, from the innermost element, to nothing, at the edge.
  await act(async () => fire(z.panel, "dragleave", ["Files"], { relatedTarget: null, clientX: 0, clientY: 0 }));
  expect(z.overlay()).toBeNull();
});

test("a file drop taken lower down (stopPropagation) still takes the overlay down", async () => {
  const z = await mount();
  await act(async () => fire(z.panel, "dragenter", ["Files"]));
  expect(z.overlay()).not.toBeNull();
  const stop = (e: Event) => e.stopPropagation();
  z.panel.addEventListener("drop", stop);
  cleanups.push(() => z.panel.removeEventListener("drop", stop));
  await act(async () => fire(z.panel, "drop", ["Files"]));
  expect(z.overlay()).toBeNull();
});

test("a file dropped on the zone still uploads", async () => {
  const z = await mount();
  const image = new File([new Uint8Array([1])], "a.png", { type: "image/png" });
  await act(async () => fire(z.panel, "dragenter", ["Files"]));
  const drop = new Event("drop", { bubbles: true, cancelable: true }) as Event & Record<string, unknown>;
  drop["dataTransfer"] = { types: ["Files"], files: [image], items: [image] };
  await act(async () => void z.panel.dispatchEvent(drop));
  expect(z.got).toEqual([[image]]);
  expect(z.overlay()).toBeNull();
});

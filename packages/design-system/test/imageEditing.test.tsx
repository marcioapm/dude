/**
 * Laying out an image in MarkdownEditor's Preview: the toolbar's buttons,
 * the keys, resize and drag each rewrite the source through onChange. A
 * locked field draws no toolbar. Mounted in happy-dom.
 */

import { afterEach, expect, test } from "bun:test";
import { act, useState } from "react";
import { createRoot } from "react-dom/client";
import { MarkdownImage } from "../src/components/Markdown.tsx";
import { MarkdownEditor } from "../src/primitives/MarkdownEditor.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";
import type { FieldKind } from "../src/util/imageLayout.ts";

const cleanups: Array<() => void> = [];
afterEach(() => {
  for (const c of cleanups.splice(0)) c();
});

const A = "![a.png](attachment:att_a)";

type Field = { kind: FieldKind; value: string; locked?: boolean; id: string };

async function mountFields(fields: Field[]) {
  const values = new Map(fields.map((f) => [f.id, f.value]));
  const opened: string[] = [];
  function Field({ f }: { f: Field }) {
    const [value, setValue] = useState(f.value);
    return (
      <MarkdownEditor label={f.id} value={value} onChange={(v) => {
        values.set(f.id, v);
        setValue(v);
      }} defaultMode="preview" locked={f.locked} imageField={f.kind} data-testid={f.id}
      attachmentImage={(id, alt) => <MarkdownImage src={`blob:${id}`} alt={alt} onOpen={() => opened.push(id)} />} />
    );
  }
  const container = document.createElement("div");
  document.body.appendChild(container);
  const root = createRoot(container);
  await act(async () => root.render(<TooltipProvider>{fields.map((f) => <Field key={f.id} f={f} />)}</TooltipProvider>));
  cleanups.push(() => {
    act(() => root.unmount());
    container.remove();
  });
  const figure = (id: string, n = 0) => container.querySelector<HTMLElement>(`[data-testid="${id}-preview"] [data-image-n="${n}"]`);
  return {
    container,
    opened,
    value: (id: string) => values.get(id)!,
    figure,
    select: async (id: string, n = 0) => act(async () => figure(id, n)!.click()),
    button: (label: string) => container.querySelector<HTMLButtonElement>(`[data-image-toolbar] button[aria-label="${label}"]`),
    press: async (label: string) => act(async () => container.querySelector<HTMLButtonElement>(`[data-image-toolbar] button[aria-label="${label}"]`)!.click()),
    key: async (el: Element, key: string, init: KeyboardEventInit = {}) => act(async () => void el.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init }))),
    panel: (id: string) => container.querySelector<HTMLElement>(`[data-testid="${id}-preview"]`)!,
    undo: async (id: string) => act(async () => void container.querySelector(`[data-testid="${id}-preview"]`)!.dispatchEvent(new KeyboardEvent("keydown", { key: "z", ctrlKey: true, bubbles: true, cancelable: true }))),
    // A change from outside the image editing: typing, or an upload replacing its placeholder.
    type: async (id: string, next: string) => act(async () => {
      const area = container.querySelector<HTMLTextAreaElement>(`textarea[data-testid="${id}"]`)!;
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(area, next);
      area.dispatchEvent(new Event("input", { bubbles: true }));
    }),
  };
}

test("Wrap right then Small write the title; Full disables the wraps and drops the alignment", async () => {
  const f = await mountFields([{ id: "goal", kind: "goal", value: `Intro.\n\n${A}\n\nOutro.` }]);
  expect(f.container.querySelector("[data-image-toolbar]")).toBeNull();
  await f.select("goal");
  expect(f.container.querySelectorAll("[data-image-toolbar] button")).toHaveLength(10);
  await f.press("Wrap right");
  expect(f.value("goal")).toBe('Intro.\n\n![a.png](attachment:att_a "right")\n\nOutro.');
  await f.press("Small");
  expect(f.value("goal")).toBe('Intro.\n\n![a.png](attachment:att_a "small right")\n\nOutro.');
  expect(f.figure("goal")!.style.width).toBe("200px");
  expect(f.figure("goal")!.getAttribute("data-align")).toBe("right");
  await f.press("Full width");
  expect(f.value("goal")).toBe('Intro.\n\n![a.png](attachment:att_a "full")\n\nOutro.');
  expect(f.button("Wrap left")!.disabled).toBe(true);
  expect(f.button("Wrap right")!.disabled).toBe(true);
  await f.press("Medium");
  expect(f.value("goal")).toBe(`Intro.\n\n${A}\n\nOutro.`);
});

test("Ctrl+Z in Preview takes back one edit at a time", async () => {
  const f = await mountFields([{ id: "goal", kind: "goal", value: `Intro.\n\n${A}\n\nOutro.` }]);
  await f.select("goal");
  await f.press("Wrap right");
  await f.press("Small");
  await f.undo("goal");
  expect(f.value("goal")).toBe('Intro.\n\n![a.png](attachment:att_a "right")\n\nOutro.');
  await f.undo("goal");
  expect(f.value("goal")).toBe(`Intro.\n\n${A}\n\nOutro.`);
});

test("after a change from outside, Ctrl+Z restores nothing, and later edits survive", async () => {
  const f = await mountFields([{ id: "goal", kind: "goal", value: `One.\n\n${A}` }]);
  await f.select("goal");
  await f.press("Small");
  const typed = 'One.\n\n![a.png](attachment:att_a "small")\n\nTyped in Write.';
  await f.type("goal", typed);
  await f.undo("goal");
  expect(f.value("goal")).toBe(typed);
  await f.select("goal");
  await f.press("Wrap left");
  expect(f.value("goal")).toBe('One.\n\n![a.png](attachment:att_a "small left")\n\nTyped in Write.');
  await f.undo("goal");
  expect(f.value("goal")).toBe(typed);
  await f.undo("goal");
  expect(f.value("goal")).toBe(typed);
});

test("Move up and down step past a paragraph, and past a criterion as a continuation", async () => {
  const f = await mountFields([
    { id: "goal", kind: "goal", value: `One.\n\nTwo.\n\n${A}` },
    { id: "crit", kind: "criteria", value: "- [ ] First\n  ![b.png](attachment:att_b)\n- [ ] Second" },
  ]);
  await f.select("goal");
  expect(f.button("Move down")!.disabled).toBe(true);
  await f.press("Move up");
  expect(f.value("goal")).toBe(`One.\n\n${A}\n\nTwo.`);
  await f.press("Move up");
  expect(f.value("goal")).toBe(`${A}\n\nOne.\n\nTwo.`);
  expect(f.button("Move up")!.disabled).toBe(true);
  await f.select("crit");
  await f.press("Move down");
  expect(f.value("crit")).toBe("- [ ] First\n- [ ] Second\n  ![b.png](attachment:att_b)");
});

test("keys: Alt+↑ moves, Delete removes, Esc deselects; Open opens the viewer", async () => {
  const f = await mountFields([{ id: "goal", kind: "goal", value: `One.\n\n${A}\n\nTwo.` }]);
  await f.select("goal");
  await f.press("Open");
  expect(f.opened).toEqual(["att_a"]);
  expect(f.container.querySelector("[data-image-toolbar]")).toBeNull();
  await f.key(f.figure("goal")!, "ArrowUp", { altKey: true });
  expect(f.value("goal")).toBe(`${A}\n\nOne.\n\nTwo.`);
  await f.select("goal");
  // Esc from outside the panel is not this field's.
  await f.key(document.body, "Escape");
  expect(f.container.querySelector("[data-image-toolbar]")).not.toBeNull();
  await f.key(f.figure("goal")!, "Escape");
  expect(f.container.querySelector("[data-image-toolbar]")).toBeNull();
  await f.select("goal");
  await f.key(f.figure("goal")!, "Delete");
  expect(f.value("goal")).toBe("One.\n\nTwo.");
});

test("locked: no toolbar, no handles, not draggable; a click opens the image", async () => {
  const f = await mountFields([{ id: "goal", kind: "goal", value: `One.\n\n${A}`, locked: true }]);
  expect(f.figure("goal")).toBeNull();
  const figure = f.container.querySelector<HTMLElement>('[data-testid="markdown-figure"]')!;
  expect(figure.getAttribute("draggable")).toBeNull();
  await act(async () => figure.querySelector<HTMLElement>("button")!.click());
  expect(f.opened).toEqual(["att_a"]);
  expect(f.container.querySelector("[data-image-toolbar]")).toBeNull();
  expect(f.container.querySelector('[data-testid="image-resize-right"]')).toBeNull();
});

test("a resize handle snaps to Small within 10 px and writes it", async () => {
  const f = await mountFields([{ id: "goal", kind: "goal", value: `One.\n\n![a.png](attachment:att_a "300 left")` }]);
  await f.select("goal");
  const fig = f.figure("goal")!;
  fig.getBoundingClientRect = () => ({ width: 300, height: 100, top: 0, left: 0, right: 300, bottom: 100, x: 0, y: 0, toJSON() {} });
  Object.defineProperty(fig.parentElement!, "clientWidth", { value: 700, configurable: true });
  const handle = f.container.querySelector<HTMLElement>('[data-testid="image-resize-right"]')!;
  const pe = (type: string, clientX: number) => new PointerEvent(type, { bubbles: true, cancelable: true, clientX, button: 0, pointerId: 1, pointerType: "pen" });
  await act(async () => void handle.dispatchEvent(pe("pointerdown", 300)));
  await act(async () => void handle.dispatchEvent(pe("pointermove", 205)));
  expect(f.container.querySelector('[data-testid="image-size-tip"]')?.textContent).toBe("Small");
  await act(async () => void handle.dispatchEvent(pe("pointerup", 205)));
  expect(f.value("goal")).toBe('One.\n\n![a.png](attachment:att_a "small left")');
});

test("a change from outside clears the selection, steals no focus, and Backspace elsewhere removes nothing", async () => {
  const B = "![b.png](attachment:att_b)";
  const f = await mountFields([{ id: "crit", kind: "criteria", value: `- [ ] One\n- [ ] Two ${A}` }]);
  const input = document.createElement("input");
  document.body.appendChild(input);
  cleanups.push(() => input.remove());
  await f.select("crit");
  expect(f.container.querySelector("[data-image-toolbar]")).not.toBeNull();
  input.focus();
  // An upload finishing in front of the selected image: reference 0 is now another image.
  await f.type("crit", `- [ ] One ${B}\n- [ ] Two ${A}`);
  expect(f.container.querySelector("[data-image-toolbar]")).toBeNull();
  expect(f.container.querySelector("[data-selected]")).toBeNull();
  expect(document.activeElement).toBe(input);
  await f.key(document.activeElement!, "Backspace");
  expect(f.value("crit")).toBe(`- [ ] One ${B}\n- [ ] Two ${A}`);
});

test("a drag from the goal drops under a criterion as its continuation line", async () => {
  const f = await mountFields([
    { id: "goal", kind: "goal", value: `Intro.\n\n${A}\n\nOutro.` },
    { id: "crit", kind: "criteria", value: "- [ ] One\n- [ ] Two\n- [ ] Three" },
  ]);
  const store = new Map<string, string>();
  const dataTransfer = {
    get types() {
      return [...store.keys()];
    },
    setData: (t: string, v: string) => void store.set(t, v),
    getData: (t: string) => store.get(t) ?? "",
    effectAllowed: "", dropEffect: "",
  };
  const dnd = (type: string, el: Element, clientY = 0) => {
    const e = new Event(type, { bubbles: true, cancelable: true }) as Event & { dataTransfer: unknown; clientY: number };
    e.dataTransfer = dataTransfer;
    e.clientY = clientY;
    el.dispatchEvent(e);
  };
  // happy-dom lays nothing out: give each criterion a place, 20 px tall.
  const items = [...f.container.querySelectorAll<HTMLElement>('[data-testid="crit-preview"] li')];
  items.forEach((li, i) => (li.getBoundingClientRect = () => ({ top: i * 20, bottom: i * 20 + 20, left: 0, right: 100, width: 100, height: 20, x: 0, y: i * 20, toJSON() {} })));
  const panel = f.container.querySelector('[data-testid="crit-preview"]')!;
  await act(async () => dnd("dragstart", f.figure("goal")!));
  await act(async () => dnd("dragover", panel, 41));
  expect(f.container.querySelector('[data-testid="image-slot"]')?.getAttribute("data-slot")).toBe("1");
  await act(async () => dnd("drop", panel, 41));
  expect(f.value("goal")).toBe("Intro.\n\nOutro.");
  expect(f.value("crit")).toBe(`- [ ] One\n- [ ] Two\n  ${A}\n- [ ] Three`);
  // Not an undo step: undoing in one field alone would leave the image in neither.
  await f.undo("crit");
  expect(f.value("crit")).toBe(`- [ ] One\n- [ ] Two\n  ${A}\n- [ ] Three`);
  expect(f.value("goal")).toBe("Intro.\n\nOutro.");
});

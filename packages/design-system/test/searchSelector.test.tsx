import { afterEach, expect, test } from "bun:test";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { SearchSelector } from "../src/components/SearchSelector.tsx";
import { SessionHeader } from "../src/components/ChatTranscript.tsx";

const OPTIONS = [
  { id: "c", label: "Conductor", model: "claude-sonnet-5", status: "Running" },
  { id: "r", label: "Review · correctness", model: "claude-opus-5", status: "Completed" },
];
let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
});

function Demo({ empty = false }: { empty?: boolean }) {
  const [current, setCurrent] = useState(OPTIONS[0]!);
  return <SessionHeader session={{ id: current.id, role: "conductor", title: current.label, status: "running", subtitle: <span title="machine">Large</span> }}
    titleSelector={(title) => <SearchSelector title={title} label="Select session" placeholder="Find a session…" options={empty ? [] : OPTIONS}
      optionKey={(o) => o.id} searchText={(o) => `${o.label} ${o.model} ${o.status}`} current={(o) => o.id === current.id}
      onPick={setCurrent} empty="No sessions yet" renderOption={(o) => o.label} />} />;
}

async function mount(empty = false) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<Demo empty={empty} />));
  return host;
}
const tick = () => act(async () => { await new Promise((r) => setTimeout(r, 10)); });
const press = (el: Element, key: string) => act(async () => { el.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true })); });
async function typeText(el: HTMLInputElement, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await tick();
}

test("the title opens a searchable listbox with the current session selected", async () => {
  const el = await mount();
  const trigger = el.querySelector<HTMLButtonElement>("button[data-testid=search-selector-trigger]")!;
  expect(el.querySelector("[title=machine]")?.closest("button")).toBeNull();
  await act(async () => trigger.click());
  await tick();
  const input = document.querySelector<HTMLInputElement>("input[role=combobox]")!;
  expect(document.activeElement).toBe(input);
  expect(document.querySelector("[role=option][aria-current=true]")?.textContent).toBe("Conductor");
  await typeText(input, "opus completed");
  expect([...document.querySelectorAll("[role=option]")].map((o) => o.textContent)).toEqual(["Review · correctness"]);
});

test("arrows and Enter choose; Escape closes and returns focus to the title", async () => {
  const el = await mount();
  const trigger = el.querySelector<HTMLButtonElement>("button[data-testid=search-selector-trigger]")!;
  await act(async () => trigger.click());
  await tick();
  let input = document.querySelector<HTMLInputElement>("input[role=combobox]")!;
  await press(input, "ArrowDown");
  await press(input, "Enter");
  await tick();
  expect(el.textContent).toContain("Review · correctness");
  expect(document.querySelector("input[role=combobox]")).toBeNull();
  await act(async () => trigger.click());
  await tick();
  input = document.querySelector<HTMLInputElement>("input[role=combobox]")!;
  await press(input, "Escape");
  await tick();
  expect(document.querySelector("input[role=combobox]")).toBeNull();
  expect(document.activeElement).toBe(trigger);
});

test("in a session header the list opens at the header's bottom-left, not over its subtitle", async () => {
  // happy-dom lays nothing out: a viewport with room below the header, and a popup with a size.
  const sized: Array<[object, string, number]> = [[document.documentElement, "clientWidth", 1280], [document.documentElement, "clientHeight", 800],
    [HTMLElement.prototype, "offsetWidth", 360], [HTMLElement.prototype, "offsetHeight", 200]];
  for (const [target, key, value] of sized) Object.defineProperty(target, key, { configurable: true, get: () => value });
  try {
    const el = await mount();
    const rect = (top: number, bottom: number, left: number, width: number) => () =>
      ({ x: left, y: top, top, bottom, left, right: left + width, width, height: bottom - top, toJSON: () => ({}) }) as DOMRect;
    el.querySelector("header")!.getBoundingClientRect = rect(100, 180, 20, 900);
    const trigger = el.querySelector<HTMLButtonElement>("button[data-testid=search-selector-trigger]")!;
    trigger.getBoundingClientRect = rect(110, 130, 80, 120);
    await act(async () => trigger.click());
    await tick();
    await tick();
    expect(document.querySelector<HTMLElement>("[data-radix-popper-content-wrapper]")!.style.transform).toMatch(/^translate\(20px, 180px\)/);
  } finally {
    for (const [target, key] of sized) delete (target as Record<string, unknown>)[key];
  }
});

test("the highlight stays on its row when the app hands over fresh options while open", async () => {
  const many = [...OPTIONS, { id: "f", label: "Fix", model: "m", status: "Running" }, { id: "i", label: "Implement", model: "m", status: "Completed" }];
  const render = () => root!.render(<SearchSelector title="Conductor" label="Select session" options={many.map((o) => ({ ...o }))}
    optionKey={(o) => o.id} searchText={(o) => o.label} current={(o) => o.id === "c"} onPick={() => {}} renderOption={(o) => o.label} />);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => render());
  await act(async () => host!.querySelector<HTMLButtonElement>("button")!.click());
  await tick();
  const input = document.querySelector<HTMLInputElement>("input[role=combobox]")!;
  await press(input, "ArrowDown");
  await press(input, "ArrowDown");
  const third = document.querySelectorAll("[role=option]")[2]!.id;
  expect(input.getAttribute("aria-activedescendant")).toBe(third);
  let scrolls = 0;
  const scroll = HTMLElement.prototype.scrollIntoView;
  HTMLElement.prototype.scrollIntoView = () => { scrolls++; };
  try {
    for (let i = 0; i < 3; i++) {
      await act(async () => render());
      await tick();
    }
  } finally {
    HTMLElement.prototype.scrollIntoView = scroll;
  }
  expect(input.getAttribute("aria-activedescendant")).toBe(third);
  expect(scrolls).toBe(0);
});

test("the open option is marked apart from the keyboard's row", async () => {
  const el = await mount();
  const trigger = el.querySelector<HTMLButtonElement>("button[data-testid=search-selector-trigger]")!;
  await act(async () => trigger.click());
  await tick();
  await press(document.querySelector("input[role=combobox]")!, "ArrowDown");
  await press(document.querySelector("input[role=combobox]")!, "Enter");
  await tick();
  await act(async () => trigger.click());
  await tick();
  const input = document.querySelector<HTMLInputElement>("input[role=combobox]")!;
  const [first, second] = [...document.querySelectorAll("[role=option]")];
  expect(input.getAttribute("aria-activedescendant")).toBe(first!.id);
  expect(first!.getAttribute("aria-current")).toBeNull();
  expect(second!.getAttribute("aria-current")).toBe("true");
  expect(second!.getAttribute("aria-selected")).toBe("true");
});

test("the trigger is named by the open session and says it opens the dialog it controls", async () => {
  const el = await mount();
  const trigger = el.querySelector<HTMLButtonElement>("button[data-testid=search-selector-trigger]")!;
  expect(trigger.getAttribute("aria-label")).toBeNull();
  expect(trigger.getAttribute("aria-labelledby")).toBeNull();
  expect(trigger.textContent).toBe("Conductor");
  await act(async () => trigger.click());
  await tick();
  const popup = document.getElementById(trigger.getAttribute("aria-controls") ?? "");
  expect(popup?.getAttribute("role")).toBe("dialog");
  expect(trigger.getAttribute("aria-haspopup")).toBe(popup?.getAttribute("role"));
});

test("no options says so", async () => {
  const el = await mount(true);
  await act(async () => el.querySelector<HTMLButtonElement>("button[data-testid=search-selector-trigger]")!.click());
  await tick();
  expect(document.body.textContent).toContain("No sessions yet");
});

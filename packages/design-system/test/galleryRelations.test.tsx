import { afterEach, expect, test } from "bun:test";
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { PaneDensityContext } from "../src/gallery/Frame.tsx";
import { RelationsSection } from "../src/gallery/sections/Relations.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
const realObserver = globalThis.IntersectionObserver;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
  globalThis.IntersectionObserver = realObserver;
  DrivenObserver.instances = [];
});

class DrivenObserver {
  static instances: DrivenObserver[] = [];
  target: Element | null = null;
  disconnected = false;
  constructor(readonly callback: IntersectionObserverCallback) { DrivenObserver.instances.push(this); }
  observe(target: Element) { this.target = target; this.emit(false); }
  emit(isIntersecting: boolean) {
    this.callback([{ isIntersecting, target: this.target } as IntersectionObserverEntry], this as unknown as IntersectionObserver);
  }
  disconnect() { this.disconnected = true; }
  unobserve() {}
  takeRecords() { return []; }
}

const expectedBlockIds = ["rel-icons", "rel-chip", "rel-board", "rel-board-lit", "rel-board-filter", "rel-startmenu", "rel-gate",
  "rel-auto", "rel-dialogs", "rel-list", "rel-picker", "rel-strip", "rel-proposal"];
function section() {
  return <StrictMode><TooltipProvider><PaneDensityContext.Provider value="both"><RelationsSection mode="both" /></PaneDensityContext.Provider></TooltipProvider></StrictMode>;
}
async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(section()));
}
function blockIds() {
  return [...host!.querySelectorAll("section#relations [id^='rel-']")].map((block) => block.id);
}

test("false waits, true mounts once in all themes and densities; pickers preserve focus with menu autofocus isolated", async () => {
  globalThis.IntersectionObserver = DrivenObserver as unknown as typeof IntersectionObserver;
  const sentinel = document.createElement("button");
  document.body.append(sentinel);
  sentinel.focus();
  // Cancel static menu autofocus to isolate the pickers; this is not a gallery-wide focus guarantee.
  const suppressMenuFocus = (event: Event) => {
    if ((event.target as HTMLElement).getAttribute("role") === "menu") event.preventDefault();
  };
  document.addEventListener("focusScope.autoFocusOnMount", suppressMenuFocus, true);
  try {
    await mount();
    expect(blockIds()).toEqual([]);
    expect(host!.querySelector("[data-testid='relations-pending']") !== null).toBe(true);
    const observer = DrivenObserver.instances.at(-1)!;
    expect(DrivenObserver.instances[0]!.disconnected).toBe(true);
    await act(async () => observer.emit(false));
    expect(blockIds()).toEqual([]);
    await act(async () => observer.emit(true));
    expect(blockIds()).toEqual(expectedBlockIds);
    expect(document.activeElement === sentinel).toBe(true);
    expect(observer.disconnected).toBe(true);
    for (const block of host!.querySelectorAll("section#relations [id^='rel-']")) {
      const panes = [...block.querySelectorAll("[data-theme][data-density]")];
      const themes = new Set(panes.map((p) => `${p.getAttribute("data-density")} ${p.getAttribute("data-theme")}`));
      expect([...themes].sort(), block.id).toEqual(["comfortable dark", "comfortable light", "compact dark", "compact light"]);
      for (const pane of panes) expect(pane.textContent!.length, block.id).toBeGreaterThan(5);
    }
    await act(async () => { observer.emit(false); root!.render(section()); });
    expect(blockIds()).toEqual(expectedBlockIds);
    expect(document.activeElement === sentinel).toBe(true);
  } finally {
    document.removeEventListener("focusScope.autoFocusOnMount", suppressMenuFocus, true);
    sentinel.remove();
  }
});

test("without IntersectionObserver all relation blocks render immediately", async () => {
  globalThis.IntersectionObserver = undefined as unknown as typeof IntersectionObserver;
  await mount();
  expect(blockIds()).toEqual(expectedBlockIds);
  expect(host!.querySelector("[data-testid='relations-pending']") === null).toBe(true);
});

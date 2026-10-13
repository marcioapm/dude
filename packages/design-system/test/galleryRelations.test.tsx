/**
 * The gallery's Task relations section draws every block in the gallery's
 * normal frames, dark and light in each density.
 */

import { afterEach, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { PaneDensityContext } from "../src/gallery/Frame.tsx";
import { RelationsSection } from "../src/gallery/sections/Relations.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
});

/** An IntersectionObserver that reports every target as on screen at once, as a scrolled-to section is. */
class OnScreen {
  readonly #callback: IntersectionObserverCallback;
  constructor(callback: IntersectionObserverCallback) { this.#callback = callback; }
  observe(target: Element) { this.#callback([{ isIntersecting: true, target } as IntersectionObserverEntry], this as unknown as IntersectionObserver); }
  unobserve() {}
  disconnect() {}
  takeRecords() { return []; }
}

/** An IntersectionObserver that never reports: the section is far off screen. */
class OffScreen {
  observe() {}
  unobserve() {}
  disconnect() {}
  takeRecords() { return []; }
}

const realObserver = globalThis.IntersectionObserver;
afterEach(() => { globalThis.IntersectionObserver = realObserver; });

test("the Task relations blocks wait until the section nears the screen", async () => {
  globalThis.IntersectionObserver = OffScreen as unknown as typeof IntersectionObserver;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(
    <TooltipProvider><PaneDensityContext.Provider value="both"><RelationsSection mode="both" /></PaneDensityContext.Provider></TooltipProvider>,
  ));
  expect(host.querySelector("section#relations [id^='rel-']")).toBeNull();
  expect(host.querySelector("[data-testid='relations-pending']")).not.toBeNull();
});

test("every Task relations block renders in dark and light, comfortable and compact", async () => {
  globalThis.IntersectionObserver = OnScreen as unknown as typeof IntersectionObserver;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(
    <TooltipProvider><PaneDensityContext.Provider value="both"><RelationsSection mode="both" /></PaneDensityContext.Provider></TooltipProvider>,
  ));
  const blocks = [...host.querySelectorAll("section#relations [id^='rel-']")];
  expect(blocks.map((b) => b.id)).toEqual(["rel-icons", "rel-chip", "rel-board", "rel-board-lit", "rel-board-filter", "rel-startmenu", "rel-gate",
    "rel-auto", "rel-dialogs", "rel-list", "rel-picker", "rel-strip", "rel-proposal"]);
  for (const block of blocks) {
    const panes = [...block.querySelectorAll("[data-theme][data-density]")];
    const themes = new Set(panes.map((p) => `${p.getAttribute("data-density")} ${p.getAttribute("data-theme")}`));
    expect([...themes].sort(), block.id).toEqual(["comfortable dark", "comfortable light", "compact dark", "compact light"]);
    for (const pane of panes) expect(pane.textContent!.length, `${block.id}`).toBeGreaterThan(5);
  }
});

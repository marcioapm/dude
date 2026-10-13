import { afterEach, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { PaneDensityContext } from "../src/gallery/Frame.tsx";
import { StartingSection } from "../src/gallery/sections/Starting.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
});

/** An IntersectionObserver that never reports: the section is far off screen. */
class OffScreen {
  observe() {}
  unobserve() {}
  disconnect() {}
  takeRecords() { return []; }
}

const realObserver = globalThis.IntersectionObserver;
afterEach(() => { globalThis.IntersectionObserver = realObserver; });

test("the Starting blocks, whose clocks tick, wait until the section nears the screen", async () => {
  globalThis.IntersectionObserver = OffScreen as unknown as typeof IntersectionObserver;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(
    <TooltipProvider><PaneDensityContext.Provider value="both"><StartingSection mode="both" /></PaneDensityContext.Provider></TooltipProvider>,
  ));
  // Counted, not toBeNull: a happy-dom element passes toBeNull here.
  expect(host.querySelectorAll("section#starting").length).toBe(1);
  expect(host.querySelectorAll("section#starting [id^='st-']").length).toBe(0);
  expect(host.querySelectorAll("section#starting [role=timer]").length).toBe(0);
});

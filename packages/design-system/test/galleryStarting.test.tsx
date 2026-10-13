import { afterEach, expect, test } from "bun:test";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { PaneDensityContext } from "../src/gallery/Frame.tsx";
import { WhenNear } from "../src/gallery/sections/Relations.tsx";
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
  // Its placeholder must not be counted as the Task relations placeholder.
  expect(host.querySelectorAll("[data-testid='starting-pending']").length).toBe(1);
  expect(host.querySelectorAll("[data-testid='relations-pending']").length).toBe(0);
});

/** Explicit entries pin delayed reveal and one-way retention without clock noise. */
class ControlledObserver {
  static latest: ControlledObserver;
  readonly callback: IntersectionObserverCallback;
  target: Element | null = null;
  constructor(callback: IntersectionObserverCallback) { this.callback = callback; ControlledObserver.latest = this; }
  observe(target: Element) { this.target = target; }
  disconnect() {}
  unobserve() {}
  takeRecords() { return []; }
  emit(isIntersecting: boolean) {
    this.callback([{ isIntersecting, target: this.target! } as IntersectionObserverEntry], this as unknown as IntersectionObserver);
  }
}
function StatefulExample() {
  const [count, setCount] = useState(0);
  return <button id="example-anchor" onClick={() => setCount(count + 1)}>{count}</button>;
}

test("delayed intersection reveals once and retains user state after leaving and returning", async () => {
  globalThis.IntersectionObserver = ControlledObserver as unknown as typeof IntersectionObserver;
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  await act(async () => root!.render(<WhenNear><StatefulExample /></WhenNear>));
  const observer = ControlledObserver.latest;
  await act(async () => observer.emit(false));
  expect(host.querySelectorAll("button").length).toBe(0);
  await act(async () => observer.emit(true));
  expect(host.querySelectorAll("button").length).toBe(1);
  const button = host.querySelector("button")!;
  await act(async () => button.click());
  expect(button.textContent).toBe("1");
  await act(async () => observer.emit(false));
  expect(host.querySelectorAll("button").length).toBe(1);
  await act(async () => observer.emit(true));
  expect(host.querySelector("button")).toBe(button);
  expect(button.textContent).toBe("1");
});

test("an unrelated hash leaves examples deferred; the matching hash reveals them", async () => {
  globalThis.IntersectionObserver = OffScreen as unknown as typeof IntersectionObserver;
  const previous = window.location.href;
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  try {
    await act(async () => root!.render(<WhenNear anchors={new Set(["example-anchor"])}><StatefulExample /></WhenNear>));
    await act(async () => { window.history.replaceState(null, "", "#other"); window.dispatchEvent(new Event("hashchange")); });
    expect(host.querySelectorAll("button").length).toBe(0);
    await act(async () => { window.history.replaceState(null, "", "#example-anchor"); window.dispatchEvent(new Event("hashchange")); });
    expect(host.querySelectorAll("button").length).toBe(1);
    await act(async () => root!.unmount());
    root = createRoot(host);
    await act(async () => root!.render(<WhenNear anchors={new Set(["different-anchor"])}><StatefulExample /></WhenNear>));
    await act(async () => window.dispatchEvent(new Event("hashchange")));
    expect(host.querySelectorAll("button").length).toBe(0);
  } finally {
    window.history.replaceState(null, "", previous);
  }
});

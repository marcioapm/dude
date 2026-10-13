/**
 * A tool call's child session in the transcript: the `subagent` role and
 * ChatThread's waiting state, and the gallery block that shows them, in
 * both themes.
 */

import { afterEach, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ChatThread } from "../src/components/ChatThread.tsx";
import { AgentAvatar } from "../src/components/AgentAvatar.tsx";
import { PaneDensityContext } from "../src/gallery/Frame.tsx";
import { ChatSection } from "../src/gallery/sections/Chat.tsx";

const realObserver = globalThis.IntersectionObserver;
let observer: ControlledObserver;
class ControlledObserver {
  readonly callback: IntersectionObserverCallback;
  readonly options: IntersectionObserverInit | undefined;
  target: Element | null = null;
  disconnects = 0;
  constructor(callback: IntersectionObserverCallback, options?: IntersectionObserverInit) {
    this.callback = callback;
    this.options = options;
    observer = this;
  }
  observe(target: Element) { this.target = target; }
  disconnect() { this.disconnects++; }
  intersect(isIntersecting: boolean) {
    this.callback([{ target: this.target!, isIntersecting } as IntersectionObserverEntry], this as unknown as IntersectionObserver);
  }
}

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
  globalThis.IntersectionObserver = realObserver;
});

async function mount(node: React.ReactNode): Promise<HTMLElement> {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(node));
  return host;
}

test("a subagent's face is a square with the fork glyph, labelled Subagent", async () => {
  const el = await mount(<AgentAvatar role="subagent" size="md" />);
  const face = el.querySelector("[data-role='subagent']")!;
  expect(face.getAttribute("aria-label")).toBe("Subagent");
  expect(face.querySelector("[data-icon='subagent']")).toBeTruthy();
});

test("a live thread waiting on a permission says so in its header and marks itself; a finished one does not", async () => {
  const since = Date.now() - 117 * 60_000;
  const el = await mount(
    <>
      <ChatThread sessionId="a" role="subagent" status="running" task="Inspect test capture setup · explore" waiting={{ on: "a permission", since, detail: "external_directory for /opt/*" }}>
        <p>inside</p>
      </ChatThread>
      <ChatThread sessionId="b" role="subagent" status="completed" task="Done" waiting={{ on: "a permission", since }} />
    </>,
  );
  const [live, done] = [...el.querySelectorAll("section")];
  expect(live!.getAttribute("data-waiting")).toBe("true");
  const header = live!.querySelector("[data-testid='chat-thread-waiting']")!;
  expect(header.textContent).toMatch(/^waiting 1h 57m on a permission$/);
  expect(header.querySelector("[data-icon='pause']")).toBeTruthy();
  expect(live!.textContent).toContain("external_directory for /opt/*");
  // Still live: it stays open with its turns inside.
  expect(live!.textContent).toContain("inside");
  expect(done!.getAttribute("data-waiting")).toBeNull();
  expect(done!.querySelector("[data-testid='chat-thread-waiting']")).toBeNull();
});

test("the gallery's child-session block draws a live, a waiting, a folded and two side-by-side children in dark and light", async () => {
  globalThis.IntersectionObserver = ControlledObserver as unknown as typeof IntersectionObserver;
  const el = await mount(
    <PaneDensityContext.Provider value="comfortable"><ChatSection mode="both" /></PaneDensityContext.Provider>,
  );
  const block = el.querySelector("#ch-children")!;
  expect(block.querySelector("h3")?.textContent).toContain("child session");
  expect(block.querySelector("[data-role='subagent']")).toBeNull();
  expect(block.querySelector("[data-theme]")).toBeNull();
  expect(observer.target).toBe(block.querySelector("[data-testid='child-examples-pending']"));
  expect(observer.options?.rootMargin).toBe("1500px 0px");
  await act(async () => observer.intersect(false));
  expect(block.querySelector("[data-role='subagent']")).toBeNull();
  expect(observer.disconnects).toBe(0);
  await act(async () => observer.intersect(true));
  expect(observer.disconnects).toBe(1);
  expect(block.querySelector("[data-testid='child-examples-pending']")).toBeNull();
  await act(async () => observer.intersect(false));
  const panes = [...block.querySelectorAll("[data-theme]")];
  expect(panes.map((p) => p.getAttribute("data-theme"))).toEqual(["dark", "light"]);
  for (const pane of panes) {
    const threads = [...pane.querySelectorAll("section[data-session-id]")];
    expect(threads.map((t) => `${t.getAttribute("data-status")}${t.getAttribute("data-waiting") ? " waiting" : ""}`))
      .toEqual(["running", "running waiting", "completed", "running", "completed"]);
    for (const t of threads) expect(t.querySelector("[data-role='subagent']"), pane.getAttribute("data-theme")!).toBeTruthy();
    expect(pane.textContent).toContain("Nothing in this Run can answer it.");
  }
});

test("the child examples disconnect their observer when unmounted before intersection", async () => {
  globalThis.IntersectionObserver = ControlledObserver as unknown as typeof IntersectionObserver;
  await mount(<PaneDensityContext.Provider value="comfortable"><ChatSection mode="both" /></PaneDensityContext.Provider>);
  expect(observer.disconnects).toBe(0);
  await act(async () => root!.unmount());
  root = null;
  expect(observer.disconnects).toBe(1);
});

test("without IntersectionObserver the child examples render immediately in both themes", async () => {
  Reflect.deleteProperty(globalThis, "IntersectionObserver");
  const el = await mount(<PaneDensityContext.Provider value="comfortable"><ChatSection mode="both" /></PaneDensityContext.Provider>);
  const block = el.querySelector("#ch-children")!;
  expect(block.querySelector("[data-testid='child-examples-pending']")).toBeNull();
  expect([...block.querySelectorAll("[data-theme]")].map((pane) => pane.getAttribute("data-theme"))).toEqual(["dark", "light"]);
  expect(block.querySelectorAll("section[data-session-id]").length).toBe(10);
});

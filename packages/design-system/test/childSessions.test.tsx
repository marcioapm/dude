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

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
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
  const el = await mount(
    <PaneDensityContext.Provider value="comfortable"><ChatSection mode="both" /></PaneDensityContext.Provider>,
  );
  const block = el.querySelector("#ch-children")!;
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

/**
 * The gallery's Brainstorm section draws every state in the gallery's
 * normal frames, dark and light in each density, and shows the
 * brainstorm's face beside the conductor's and beside needs-you.
 */

import { afterEach, expect, test } from "bun:test";
import { StrictMode, act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { PaneDensityContext } from "../src/gallery/Frame.tsx";
import { BrainstormSection } from "../src/gallery/sections/Brainstorm.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
});

async function mount(): Promise<HTMLElement> {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(
    <PaneDensityContext.Provider value="both"><BrainstormSection mode="both" /></PaneDensityContext.Provider>,
  ));
  return host;
}

const FRAMES = ["comfortable dark", "comfortable light", "compact dark", "compact light"];

test("every Brainstorm block renders in dark and light, comfortable and compact", async () => {
  const el = await mount();
  const blocks = [...el.querySelectorAll("section#brainstorm [id^='bs-']")];
  expect(blocks.map((b) => b.id)).toEqual(["bs-avatar", "bs-title", "bs-proposal", "bs-row", "bs-sidebar", "bs-rail", "bs-rail-model", "bs-files"]);
  for (const block of blocks) {
    const panes = [...block.querySelectorAll("[data-theme][data-density]")];
    expect(panes.map((p) => `${p.getAttribute("data-density")} ${p.getAttribute("data-theme")}`), block.id).toEqual(FRAMES);
    for (const pane of panes) expect(pane.textContent!.length, `${block.id} ${pane.getAttribute("data-theme")}`).toBeGreaterThan(20);
  }
});

test("SessionTitle: untitled reads New session, muted; one who can chat renames in place, Enter saves and Escape cancels; a reader cannot", async () => {
  const el = await mount();
  const pane = el.querySelector("#bs-title [data-theme]")!;
  const [untitled, named, reader] = [...pane.querySelectorAll("[data-testid='session-title']")];
  expect(untitled!.textContent).toBe("New session");
  expect(untitled!.querySelector("[data-untitled='true']")).toBeTruthy();
  expect(reader!.tagName).toBe("SPAN");
  expect(named!.tagName).toBe("BUTTON");
  const field = async () => pane.querySelector<HTMLInputElement>("[data-testid='session-title-input']");
  const key = async (input: HTMLInputElement, k: string) => act(async () => {
    input.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true }));
  });
  const typeIn = async (input: HTMLInputElement, text: string) => act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, text);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await act(async () => (named as HTMLElement).click());
  let input = (await field())!;
  expect(input.value).toBe("Usage-based billing");
  await typeIn(input, "Dropped");
  await key(input, "Escape");
  expect(await field()).toBeNull();
  expect(pane.querySelectorAll("[data-testid='session-title']")[1]!.textContent).toBe("Usage-based billing");
  await act(async () => (pane.querySelectorAll("[data-testid='session-title']")[1] as HTMLElement).click());
  input = (await field())!;
  await typeIn(input, "Billing v2");
  await key(input, "Enter");
  expect(await field()).toBeNull();
  expect(pane.querySelectorAll("[data-testid='session-title']")[1]!.textContent).toBe("Billing v2");
  const notices = [...pane.querySelectorAll("[data-kind='renamed']")].map((n) => n.textContent);
  expect(notices[0]).toContain("Brainstorm: Named it “Usage-based billing”");
  expect(notices[1]).toContain("Ana renamed it “Billing v2”");
});

test("ProposalCard, in every frame and under StrictMode: blocked for everyone stays open with its why; opened from folded is open", async () => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  // The gallery mounts under StrictMode, which runs effects twice: the opened state must not hang on one.
  await act(async () => root!.render(
    <StrictMode><PaneDensityContext.Provider value="both"><BrainstormSection mode="both" /></PaneDensityContext.Provider></StrictMode>,
  ));
  const panes = [...host.querySelectorAll("#bs-proposal [data-theme][data-density]")];
  expect(panes.length).toBe(4);
  for (const pane of panes) {
    const frame = `${pane.getAttribute("data-density")} ${pane.getAttribute("data-theme")}`;
    const labels = [...pane.querySelectorAll("*")].filter((n) => n.children.length === 0 && n.textContent?.startsWith("blocked for everyone"));
    expect(labels.length, frame).toBe(1);
    const cards = [...pane.querySelectorAll("[data-testid='proposal-card']")];
    const blocked = cards.find((c) => c.textContent?.includes("BL-55 has started"));
    expect(blocked?.getAttribute("data-folded"), frame).toBeNull();
    expect(blocked!.textContent, frame).not.toContain("for others to file");
    expect(pane.querySelectorAll("[data-testid='proposal-fold'][aria-expanded='true']").length, frame).toBe(1);
  }
});

test("PublishedFiles: each file by its own name and kind, a version mark when published again, its description under it, the rest as N more", async () => {
  const el = await mount();
  const pane = el.querySelector("#bs-files [data-theme]")!;
  const [full, capped] = [...pane.querySelectorAll("[data-testid='published-files']")];
  const names = [...full!.querySelectorAll("button")].map((b) => {
    const description = b.querySelector("[data-testid='published-file-description']");
    return [b.getAttribute("data-name"), b.getAttribute("title"), description?.textContent ?? null, description?.getAttribute("title") ?? null];
  });
  expect(names).toEqual([
    ["design/metering.md", "design/metering.md", "How usage is metered and billed", "How usage is metered and billed"],
    ["usage-by-kind.csv", "usage-by-kind.csv", "Usage per kind, last 30 days", "Usage per kind, last 30 days"],
    ["flow.svg", "flow.svg", null, null],
    ["rollup.json", "rollup.json", null, null],
  ]);
  const versionMark = (b: Element) => [...b.querySelectorAll("span")].filter((s) => s.children.length === 0 && /^v\d+$/.test(s.textContent ?? "")).map((s) => s.textContent);
  expect([...full!.querySelectorAll("button")].map(versionMark)).toEqual([["v3"], [], [], []]);
  expect(full!.querySelectorAll("svg").length).toBe(4);
  expect(capped!.querySelectorAll("button")).toHaveLength(2);
  expect(capped!.textContent).toContain("1 more");
});

// `awaiting_input` is the vocabulary's needs-you state ("Needs you", solid).
test("each frame shows the brainstorm's face beside the conductor's and a needs-you mark", async () => {
  const el = await mount();
  const panes = [...el.querySelectorAll("#bs-avatar [data-theme]")];
  expect(panes).toHaveLength(4);
  for (const pane of panes) {
    const first = pane.querySelector("[data-role='brainstorm']")!.closest("div")!;
    expect(first.querySelector("[data-role='conductor'][aria-label='Conductor']")).toBeTruthy();
    expect(first.querySelector("[data-role='brainstorm'][aria-label='Brainstorm']")).toBeTruthy();
    expect(first.querySelector("[data-status='awaiting_input']")).toBeTruthy();
  }
});

test("the rail's Model: the owner's chip, chosen, read only and no longer fitting, each with when a change applies", async () => {
  const el = await mount();
  const panes = [...el.querySelectorAll("#bs-rail-model [data-theme][data-density]")];
  expect(panes.length).toBe(FRAMES.length);
  for (const pane of panes) {
    const chosen = pane.querySelector("[data-testid=rail-model-chosen]")!;
    expect(chosen.querySelector("button[data-testid=model-picker]")!.getAttribute("aria-label")).toBe("Model: Sol on Codex");
    const readOnly = pane.querySelector("[data-testid=rail-model-readonly]")!;
    expect(readOnly.querySelectorAll("button").length).toBe(0);
    expect(readOnly.textContent).toContain("Model: Coder on Claude Code");
    const misfit = pane.querySelector("[data-testid=rail-model-misfit] [data-testid=model-picker]")!;
    expect(document.getElementById(misfit.getAttribute("aria-describedby") ?? "")?.textContent).toContain("Choose another harness or tier in the session's Model.");
    for (const block of [chosen, readOnly]) expect(block.textContent).toContain("Applies the next time the agent starts.");
  }
  for (const pane of el.querySelectorAll("#bs-rail [data-theme][data-density]")) {
    expect(pane.querySelector("[data-testid=rail-model]")!.textContent).toContain("Applies the next time the agent starts.");
  }
});

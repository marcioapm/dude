/**
 * The gallery's Welcome section draws the shell with the welcome, the first
 * time, and the collapsed rail as full-width frames, and the parts' blocks
 * in panes, each in dark and light and in both densities; compact lists two
 * more recent sessions.
 */

import { afterEach, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { PaneDensityContext } from "../src/gallery/Frame.tsx";
import { WelcomeSection } from "../src/gallery/sections/Welcome.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";

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
    <TooltipProvider><PaneDensityContext.Provider value="both"><WelcomeSection mode="both" /></PaneDensityContext.Provider></TooltipProvider>,
  ));
  return host;
}

const FRAMES = ["comfortable dark", "comfortable light", "compact dark", "compact light"];

test("every Welcome block renders in dark and light, comfortable and compact", async () => {
  const el = await mount();
  const blocks = [...el.querySelectorAll("section#welcome [id^='wl-']")];
  expect(blocks.map((b) => b.id)).toEqual(["wl-shell", "wl-first", "wl-rail", "wl-starters", "wl-links", "wl-model", "wl-recent"]);
  for (const block of blocks) {
    const panes = [...block.querySelectorAll(":scope [data-theme][data-density]")].filter((p) => !p.parentElement!.closest("[data-theme]"));
    expect(panes.map((p) => `${p.getAttribute("data-density")} ${p.getAttribute("data-theme")}`), block.id).toEqual(FRAMES);
  }
});

test("the shell frames: the welcome beside the full sidebar, the first-time line, and the rail in place of the sidebar", async () => {
  const el = await mount();
  for (const frame of el.querySelectorAll("#wl-shell [data-shot]")) {
    expect(frame.querySelector("h1")!.textContent).toBe("Afternoon, Márcio");
    expect(frame.querySelector("[data-testid=sidebar-rail]") === null).toBe(true);
    const rows = frame.querySelectorAll("[data-testid=recent-session]").length;
    expect(rows).toBe(frame.getAttribute("data-density") === "compact" ? 6 : 4);
  }
  for (const frame of el.querySelectorAll("#wl-first [data-shot]")) {
    expect(frame.querySelector("[data-testid=welcome-note]")!.textContent).toContain("It changes nothing");
    expect(frame.querySelector("[data-testid=recent-session]") === null).toBe(true);
  }
  for (const frame of el.querySelectorAll("#wl-rail [data-shot]")) {
    expect(frame.querySelector("[data-testid=sidebar-rail]")).not.toBeNull();
    expect(frame.querySelector("[role=tree]") === null).toBe(true);
  }
});

test("ComposerLinks: none, two, read only, and the menu drawn open inside its pane", async () => {
  const el = await mount();
  const pane = el.querySelector("#wl-links [data-theme]")!;
  const links = [...pane.querySelectorAll("[data-testid=composer-links]")];
  expect(links.length).toBe(4);
  expect(links[0]!.textContent).toContain("Reads memory only");
  expect(links[1]!.querySelectorAll("[data-project]").length).toBe(2);
  expect(links[2]!.querySelectorAll("button").length).toBe(0);
  expect(pane.querySelector("[role=menu]")).not.toBeNull();
});

test("ModelPicker: default, chosen, read only, and the menu open in its pane with a refused pair saying why", async () => {
  const el = await mount();
  const panes = [...el.querySelectorAll("#wl-model [data-theme][data-density]")];
  expect(panes.length).toBe(FRAMES.length);
  for (const pane of panes) {
    expect(pane.querySelector("[data-testid=model-default] [data-testid=model-picker]")!.getAttribute("aria-label"))
      .toBe("Model: Claude (High) on Claude Code (organisation default)");
    expect(pane.querySelector("[data-testid=model-chosen] [data-testid=model-picker]")!.getAttribute("aria-label")).toBe("Model: Sol on OpenCode");
    const readOnly = pane.querySelector("[data-testid=model-readonly]")!;
    expect(readOnly.querySelectorAll("button").length).toBe(0);
    expect(readOnly.textContent).toContain("Coder· Claude Code");
    const menu = pane.querySelector("[role=menu]")!;
    const sol = menu.querySelector("[data-testid=rowmenu-mtr_sol]")!;
    expect(sol.hasAttribute("data-disabled")).toBe(true);
    expect(sol.textContent).toContain("Claude Code takes an Anthropic model");
  }
  // The welcome's own composer carries the picker after "To Brainstorm".
  const shell = el.querySelector("#wl-shell [data-shot]")!;
  expect(shell.querySelector("[data-testid=composer-to] + [data-testid=model-picker]")).not.toBeNull();
});

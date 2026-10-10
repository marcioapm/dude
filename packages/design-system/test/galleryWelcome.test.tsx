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
  expect(blocks.map((b) => b.id)).toEqual(["wl-shell", "wl-first", "wl-rail", "wl-starters", "wl-links", "wl-recent"]);
  for (const block of blocks) {
    const panes = [...block.querySelectorAll(":scope [data-theme][data-density]")].filter((p) => !p.parentElement!.closest("[data-theme]"));
    expect(panes.map((p) => `${p.getAttribute("data-density")} ${p.getAttribute("data-theme")}`), block.id).toEqual(FRAMES);
  }
});

test("the shell frames: the welcome beside the full sidebar, the first-time line, and the rail in place of the sidebar", async () => {
  const el = await mount();
  for (const frame of el.querySelectorAll("#wl-shell [data-shot]")) {
    expect(frame.querySelector("h1")!.textContent).toBe("Afternoon, Márcio");
    expect(frame.querySelector("[data-testid=sidebar-rail]")).toBeNull();
    const rows = frame.querySelectorAll("[data-testid=recent-session]").length;
    expect(rows).toBe(frame.getAttribute("data-density") === "compact" ? 6 : 4);
  }
  for (const frame of el.querySelectorAll("#wl-first [data-shot]")) {
    expect(frame.querySelector("[data-testid=welcome-note]")!.textContent).toContain("It changes nothing");
    expect(frame.querySelector("[data-testid=recent-session]")).toBeNull();
  }
  for (const frame of el.querySelectorAll("#wl-rail [data-shot]")) {
    expect(frame.querySelector("[data-testid=sidebar-rail]")).not.toBeNull();
    expect(frame.querySelector("[role=tree]")).toBeNull();
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

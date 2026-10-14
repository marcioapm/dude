/**
 * The gallery's Accounts section: every block renders in dark and light,
 * comfortable and compact, and the switcher's open menus are drawn inside
 * their own pane, so each takes that pane's theme.
 */

import { afterEach, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { PaneDensityContext } from "../src/gallery/Frame.tsx";
import { AccountsSection } from "../src/gallery/sections/Accounts.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
});

test("every Accounts block renders in each theme and density, with the switcher's menus open in their panes", async () => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(
    <TooltipProvider><PaneDensityContext.Provider value="both"><AccountsSection mode="both" /></PaneDensityContext.Provider></TooltipProvider>,
  ));
  await act(async () => new Promise((r) => setTimeout(r, 20)));
  const blocks = [...host.querySelectorAll("section#accounts [id^='ac-']")];
  expect(blocks.map((b) => b.id)).toEqual(["ac-face", "ac-switcher", "ac-chips", "ac-links", "ac-key", "ac-door"]);
  for (const block of blocks) {
    const panes = [...block.querySelectorAll("[data-theme][data-density]")].map((p) => `${p.getAttribute("data-density")} ${p.getAttribute("data-theme")}`);
    expect(panes, block.id).toEqual(["comfortable dark", "comfortable light", "compact dark", "compact light"]);
  }
  // Two open menus per pane (band and rail), each inside the pane that themes it.
  const switcherPanes = [...host.querySelector("#ac-switcher")!.querySelectorAll("[data-theme][data-density]")];
  for (const pane of switcherPanes) expect(pane.querySelectorAll("[role=menu]").length).toBe(2);
  // Every chip state is shown.
  const states = new Set([...host.querySelectorAll("#ac-chips li[data-state]")].map((c) => c.getAttribute("data-state")));
  expect([...states].sort()).toEqual(["duplicate", "invalid", "invited", "member", "renew", "valid"]);
});

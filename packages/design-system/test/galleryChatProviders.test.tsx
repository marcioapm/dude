/**
 * The gallery's chat providers section mounts in every theme and density,
 * has every block its menu names, and shows each piece with Slack and with
 * the placeholder provider.
 */

import { afterEach, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { PaneDensityContext } from "../src/gallery/Frame.tsx";
import { ChatProvidersSection } from "../src/gallery/sections/ChatProviders.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
});

test("every block mounts in all four panes, with both providers", async () => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(
    <TooltipProvider><PaneDensityContext.Provider value="both"><ChatProvidersSection mode="both" /></PaneDensityContext.Provider></TooltipProvider>));
  const ids = [...host.querySelectorAll("section#chat-providers [id^='cp-']")].map((b) => b.id);
  expect(ids).toEqual(["cp-glyph", "cp-route", "cp-origin", "cp-thread", "cp-routing", "cp-facts", "cp-connection", "cp-session-row", "cp-setting-fill",
    "cp-prompt", "cp-stack", "cp-providers", "cp-settings", "cp-board"]);
  const panes = [...host.querySelectorAll("#cp-glyph [data-theme]")].map((p) => `${p.getAttribute("data-theme")}·${p.getAttribute("data-density")}`);
  expect(panes.sort()).toEqual(["dark·comfortable", "dark·compact", "light·comfortable", "light·compact"]);
  expect(host.querySelectorAll('#cp-glyph [data-icon="slack"]').length).toBeGreaterThan(0);
  expect(host.querySelectorAll('#cp-glyph [data-icon="integration"]').length).toBeGreaterThan(0);
  // The board's lead on the three chat-born cards, in each of the four panes.
  expect(host.querySelectorAll('#cp-board [data-board-key] [data-icon="slack"]').length).toBe(12);
});

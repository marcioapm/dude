/**
 * The gallery's Navigation section: the sidebar's room and the guide's
 * column render in each theme and density, with the states the README
 * names — sessions folded, unfolded and over three, the others online in
 * the brand row, the profile menu's places, and a guide search.
 */

import { afterEach, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { PaneDensityContext } from "../src/gallery/Frame.tsx";
import { NavigationSection } from "../src/gallery/sections/Navigation.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
});

test("the sidebar's and the guide's blocks render in every pane with their states", async () => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(
    <TooltipProvider><PaneDensityContext.Provider value="both"><NavigationSection mode="both" /></PaneDensityContext.Provider></TooltipProvider>,
  ));
  const panes = (id: string) => [...host!.querySelector(`#${id}`)!.querySelectorAll<HTMLElement>("[data-theme][data-density]")];
  for (const id of ["nav-sidebar", "nav-sidebar-sessions", "nav-guide", "nav-tree-menus"]) {
    expect(panes(id).map((p) => `${p.dataset["density"]} ${p.dataset["theme"]}`), id).toEqual(["comfortable dark", "comfortable light", "compact dark", "compact light"]);
  }
  for (const pane of panes("nav-sidebar")) {
    expect(pane.querySelector("header [data-testid=online]")!.getAttribute("aria-label")).toBe("Online: Ana Ribeiro, Tom Okafor, Lin Zhao, Kai Nakamura");
    expect(pane.querySelectorAll("[data-testid=waiting-on-others]").length).toBe(0);
    expect(pane.querySelectorAll("[data-testid=sidebar-sessions] [data-session]").length).toBe(3);
  }
  for (const pane of panes("nav-sidebar-sessions")) {
    expect([...pane.querySelectorAll("[data-testid=sidebar-sessions]")].map((s) => s.querySelectorAll("[data-session]").length)).toEqual([3, 0, 5]);
  }
  for (const pane of panes("nav-guide")) {
    expect(pane.querySelectorAll("[data-testid=help-section-link]").length).toBe(4);
    expect([...pane.querySelectorAll("[data-testid=help-search-count]")].map((c) => c.textContent)).toEqual(["2 sections", "0 sections"]);
  }
});

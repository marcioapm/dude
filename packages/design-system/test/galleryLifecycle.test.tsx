import { afterEach, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { PaneDensityContext } from "../src/gallery/Frame.tsx";
import { LifecycleSection } from "../src/gallery/sections/Lifecycle.tsx";
import { NavigationSection } from "../src/gallery/sections/Navigation.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
});

async function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<TooltipProvider><PaneDensityContext.Provider value="both">{node}</PaneDensityContext.Provider></TooltipProvider>));
  return host;
}

test("the Lifecycle section draws every block in both themes and densities", async () => {
  const el = await mount(<LifecycleSection mode="both" />);
  const blocks = ["lifecycle-marks", "lifecycle-menu", "lifecycle-close", "lifecycle-close-archive", "lifecycle-archive", "lifecycle-board", "lifecycle-relations"];
  expect([...el.querySelectorAll("section#lifecycle [id^='lifecycle-']")].map((b) => b.id)).toEqual(blocks);
  for (const id of blocks) {
    const panes = [...el.querySelectorAll(`#${id} [data-theme][data-density]`)].map((p) => `${p.getAttribute("data-theme")} ${p.getAttribute("data-density")}`);
    expect(new Set(panes)).toEqual(new Set(["dark comfortable", "light comfortable", "dark compact", "light compact"]));
  }
  // Every resolution, in each of the four panes.
  expect(el.querySelectorAll('#lifecycle-marks [data-testid="resolution-mark"]').length).toBe(5 * 4);
  // The open menus say their reasons.
  expect(el.querySelector("#lifecycle-menu")!.textContent).toContain("Unarchive it first, then reopen it");
});

test("#nav-tree-epics: every epic row leads with one layers glyph; no task row has one", async () => {
  const el = await mount(<NavigationSection mode="dark" />);
  const block = el.querySelector("#nav-tree-epics")!;
  const rows = [...block.querySelectorAll('[role="treeitem"]')];
  const epics = rows.filter((r) => r.getAttribute("data-kind") === "epic");
  const tasks = rows.filter((r) => r.getAttribute("data-kind") === "task");
  expect(epics.length).toBeGreaterThan(4);
  expect(tasks.length).toBeGreaterThan(0);
  for (const r of epics) expect(r.querySelectorAll('svg[data-icon="layers"]').length).toBe(1);
  for (const r of tasks) expect(r.querySelectorAll('svg[data-icon="layers"]').length).toBe(0);
});

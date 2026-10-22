import { afterEach, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { PaneDensityContext } from "../src/gallery/Frame.tsx";
import { LifecycleSection, StoppingSection } from "../src/gallery/sections/Lifecycle.tsx";
import { ChatSection } from "../src/gallery/sections/Chat.tsx";
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
  expect(el.querySelector("#lifecycle-menu")!.textContent).not.toContain("Stop its agents first");
  expect(el.querySelector("#lifecycle-menu")!.textContent).not.toContain("It has a pull request open");
});

test("the Stopping section draws LiveWork in both themes and densities, and at 390px", async () => {
  const el = await mount(<StoppingSection mode="both" />);
  const panes = [...el.querySelectorAll("#stopping-live [data-theme][data-density]")].map((p) => `${p.getAttribute("data-theme")} ${p.getAttribute("data-density")}`);
  expect(new Set(panes)).toEqual(new Set(["dark comfortable", "light comfortable", "dark compact", "light compact"]));
  const first = el.querySelector('#stopping-live [data-testid="live-work"]')!;
  expect(first.textContent).toContain("Stopping stops the conductor, 2 reviewers and a queued fixer.");
  expect(el.querySelectorAll('#stopping-live [data-testid="live-work"]').length).toBe(4 * 4);
});

test("Chat's Who decides and Attempts blocks draw in every pane", async () => {
  const el = await mount(<ChatSection mode="both" />);
  for (const id of ["ch-decider-field", "ch-attempts"]) {
    const panes = [...el.querySelectorAll(`#${id} [data-theme][data-density]`)].map((p) => `${p.getAttribute("data-theme")} ${p.getAttribute("data-density")}`);
    expect(new Set(panes)).toEqual(new Set(["dark comfortable", "light comfortable", "dark compact", "light compact"]));
  }
  expect(el.querySelectorAll('#ch-decider-field [data-testid="decider-field"]').length).toBe(4 * 4);
  expect(el.querySelector('#ch-attempts hr[aria-label]')!.getAttribute("aria-label")).toBe("Attempt 2, started by Ana");
  expect(el.querySelector('#ch-attempts [role="group"]')!.getAttribute("aria-label")).toBe("Attempt 1's conversation");
  // The earlier Run line is read, not opened: no chevron in any pane's group.
  const groups = [...el.querySelectorAll('#ch-attempts [role="group"]')];
  expect(groups.length).toBeGreaterThan(0);
  for (const g of groups) {
    expect(g.querySelector('[data-role]')).not.toBeNull();
    expect(g.querySelector('svg[data-icon="chevron-right"]')).toBeNull();
  }
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

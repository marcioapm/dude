/**
 * "Mine" narrows the tree to your tasks but leaves it yours to fold: its
 * projects and epics start open and their chevrons still work. A search
 * is different: it holds every row with a hit open, chevrons inert.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Sidebar } from "../src/components/Sidebar.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";
import { flattenNav, type NavProject } from "../src/util/navModel.ts";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
});

async function mount(el: React.ReactElement): Promise<HTMLElement> {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<TooltipProvider>{el}</TooltipProvider>));
  return host;
}

const ME = { id: "me", name: "Me" };
const THEM = { id: "them", name: "Them" };

// Nothing here is counted (all done), so without a filter both rows start closed.
const PROJECT: NavProject = {
  id: "p",
  name: "Web",
  epics: [{ id: "e", title: "Login", tasks: [
    { id: "t1", title: "Mine one", status: "done", people: [ME] },
    { id: "t2", title: "Theirs", status: "done", people: [THEM] },
  ] }],
};

const keys = (rows: ReturnType<typeof flattenNav>) => rows.map((r) => r.key);

describe("flattenNav with Mine", () => {
  test("opens projects and epics by default, without forcing them", () => {
    const rows = flattenNav([PROJECT], new Map(), { person: "me", you: "me" });
    expect(keys(rows)).toEqual(["project:p", "epic:e", "task:t1"]);
    expect(rows.filter((r) => r.expandable).map((r) => [r.key, r.expanded, r.forced])).toEqual([
      ["project:p", true, false],
      ["epic:e", true, false],
    ]);
  });

  test("a folded epic or project stays folded", () => {
    expect(keys(flattenNav([PROJECT], new Map([["epic:e", false]]), { person: "me", you: "me" }))).toEqual(["project:p", "epic:e"]);
    expect(keys(flattenNav([PROJECT], new Map([["project:p", false]]), { person: "me", you: "me" }))).toEqual(["project:p"]);
  });

  test("a search still forces its hits open over a fold", () => {
    const rows = flattenNav([PROJECT], new Map([["project:p", false], ["epic:e", false]]), { query: "mine", person: "me", you: "me" });
    expect(keys(rows)).toEqual(["project:p", "epic:e", "task:t1"]);
    expect(rows.filter((r) => r.expandable).every((r) => r.forced)).toBe(true);
  });
});

describe("Sidebar with Mine on", () => {
  const rowOf = (el: HTMLElement, key: string) => el.querySelector<HTMLElement>(`[data-nav-key="${key}"]`);
  const chevronOf = (el: HTMLElement, key: string) => rowOf(el, key)?.querySelector<HTMLButtonElement>('button[aria-label="Collapse"], button[aria-label="Expand"]') ?? null;

  test("the epic and project chevrons fold and unfold", async () => {
    const el = await mount(<Sidebar projects={[PROJECT]} you="me" mine />);
    expect(rowOf(el, "task:t1")).not.toBeNull();
    expect(rowOf(el, "task:t2")).toBeNull();

    await act(async () => chevronOf(el, "epic:e")!.click());
    expect(rowOf(el, "task:t1")).toBeNull();
    expect(rowOf(el, "epic:e")!.getAttribute("aria-expanded")).toBe("false");
    await act(async () => chevronOf(el, "epic:e")!.click());
    expect(rowOf(el, "task:t1")).not.toBeNull();

    await act(async () => chevronOf(el, "project:p")!.click());
    expect(rowOf(el, "epic:e")).toBeNull();
    expect(rowOf(el, "project:p")!.getAttribute("aria-expanded")).toBe("false");
    await act(async () => chevronOf(el, "project:p")!.click());
    expect(rowOf(el, "epic:e")).not.toBeNull();
  });
});

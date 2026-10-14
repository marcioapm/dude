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
import { flattenNav, type NavEpic, type NavProject } from "../src/util/navModel.ts";
import { allByRole, byRole } from "./queries.ts";

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
const LOGIN: NavEpic = { id: "e", title: "Login", tasks: [
  { id: "t1", title: "Mine one", status: "done", people: [ME] },
  { id: "t2", title: "Theirs", status: "done", people: [THEM] },
] };
const PROJECT: NavProject = { id: "p", name: "Web", epics: [LOGIN] };

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

  test("an epic or project with none of your tasks is not shown", () => {
    const theirs: NavEpic = { id: "other", title: "Other", tasks: [LOGIN.tasks[1]!] };
    const projects: NavProject[] = [{ ...PROJECT, epics: [LOGIN, theirs] }, { id: "q", name: "Other", epics: [theirs] }];
    expect(keys(flattenNav(projects, new Map(), { person: "me", you: "me" }))).toEqual(["project:p", "epic:e", "task:t1"]);
  });

  test("a search still forces its hits open over a fold", () => {
    const rows = flattenNav([PROJECT], new Map([["project:p", false], ["epic:e", false]]), { query: "mine", person: "me", you: "me" });
    expect(keys(rows)).toEqual(["project:p", "epic:e", "task:t1"]);
    expect(rows.filter((r) => r.expandable).every((r) => r.forced)).toBe(true);
  });
});

describe("Sidebar with Mine on", () => {
  const row = (el: HTMLElement, name: string) => byRole(el, "treeitem", name);
  const shown = (el: HTMLElement, name: string) => allByRole(el, "treeitem", name).length > 0;
  const press = (el: HTMLElement, rowName: string, button: "Collapse" | "Expand") =>
    act(async () => byRole(row(el, rowName), "button", button).click());
  const key = (el: HTMLElement, k: string) =>
    act(async () => void el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true })));

  test("the epic and project chevrons fold and unfold", async () => {
    const el = await mount(<Sidebar projects={[PROJECT]} you="me" mine />);
    expect(shown(el, "Mine one")).toBe(true);
    expect(shown(el, "Theirs")).toBe(false);

    await press(el, "Login", "Collapse");
    expect(shown(el, "Mine one")).toBe(false);
    expect(row(el, "Login").getAttribute("aria-expanded")).toBe("false");
    await press(el, "Login", "Expand");
    expect(shown(el, "Mine one")).toBe(true);

    await press(el, "Web", "Collapse");
    expect(shown(el, "Login")).toBe(false);
    expect(row(el, "Web").getAttribute("aria-expanded")).toBe("false");
    await press(el, "Web", "Expand");
    expect(shown(el, "Login")).toBe(true);
  });

  test("ArrowLeft folds an epic and ArrowRight unfolds it, focus staying on it", async () => {
    const el = await mount(<Sidebar projects={[PROJECT]} you="me" mine />);
    await act(async () => row(el, "Login").focus());

    await key(row(el, "Login"), "ArrowLeft");
    expect(shown(el, "Mine one")).toBe(false);
    expect(document.activeElement).toBe(row(el, "Login"));

    await key(row(el, "Login"), "ArrowRight");
    expect(shown(el, "Mine one")).toBe(true);
    expect(document.activeElement).toBe(row(el, "Login"));
  });

  test("a fold made under Mine is still there after Everyone and back", async () => {
    const el = await mount(<Sidebar projects={[PROJECT]} you="me" />);
    await act(async () => byRole(el, "button", "Mine").click());
    await press(el, "Login", "Collapse");

    await act(async () => byRole(el, "button", "Everyone").click());
    expect(shown(el, "Theirs")).toBe(false);
    await act(async () => byRole(el, "button", "Mine").click());
    expect(row(el, "Login").getAttribute("aria-expanded")).toBe("false");
    expect(shown(el, "Mine one")).toBe(false);
  });
});

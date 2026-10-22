/**
 * What the projects tree lists. Unsearched (and under Mine) only work in
 * progress — the board's In progress, In review and Ready to merge — plus
 * the selection; a query or a triage filter lists every status. A task is
 * a leaf: no session rows, and a selected session highlights its task.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { TaskStatus } from "@dude/domain";
import { NavTree } from "../src/components/NavTree.tsx";
import { Sidebar } from "../src/components/Sidebar.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";
import { flattenNav, type NavFilter, type NavOverrides, type NavProject, type NavRef, type NavTask } from "../src/util/navModel.ts";
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

const task = (id: string, status: TaskStatus, extra: Partial<NavTask> = {}): NavTask => ({ id, key: id.toUpperCase(), title: `Task ${id}`, status, ...extra });

const keys = (projects: ReadonlyArray<NavProject>, filter: NavFilter = {}, overrides: NavOverrides = new Map()) =>
  flattenNav(projects, overrides, filter).map((r) => r.key);

const BACKLOG: TaskStatus[] = ["received", "intake", "awaiting_confirmation", "queued"];
const CLOSED: TaskStatus[] = ["done", "failed", "aborted"];
const IN_PROGRESS: TaskStatus[] = ["running", "awaiting_input", "review", "ready_to_merge"];

// Open by hand, so the default open state never hides what the trim keeps.
const OPEN: NavOverrides = new Map([["project:p", true], ["epic:e", true]]);

describe("the unsearched tree lists only work in progress", () => {
  test.each([...BACKLOG, ...CLOSED])("a %s task is not listed", (status) => {
    const p: NavProject = { id: "p", name: "Web", tasks: [task("t", status), task("live", "running")] };
    expect(keys([p], {}, OPEN)).toEqual(["project:p", "task:live"]);
  });

  test.each(IN_PROGRESS)("a %s task is listed", (status) => {
    const p: NavProject = { id: "p", name: "Web", tasks: [task("t", status)] };
    expect(keys([p], {}, OPEN)).toEqual(["project:p", "task:t"]);
  });

  test("Mine is not a search: it trims too", () => {
    const me = { id: "me", name: "Me" };
    const p: NavProject = { id: "p", name: "Web", tasks: [task("done", "done", { people: [me] }), task("live", "review", { people: [me] })] };
    expect(keys([p], { person: "me", you: "me" })).toEqual(["project:p", "task:live"]);
  });

  test("the roll-up counts still count every task", () => {
    const p: NavProject = { id: "p", name: "Web", tasks: [task("ask", "awaiting_confirmation"), task("broke", "failed")] };
    const row = flattenNav([p], new Map())[0]!;
    expect(row.counts).toMatchObject({ needs_you: 1, failed: 1 });
  });
});

describe("a search lists Backlog and Done", () => {
  test("a query finds a done task", () => {
    const p: NavProject = { id: "p", name: "Web", tasks: [task("shipped", "done", { title: "Ship the invoice export" }), task("live", "running")] };
    expect(keys([p], { query: "invoice" })).toEqual(["project:p", "task:shipped"]);
  });

  test("the needs-you filter finds a plan awaiting confirmation", () => {
    const p: NavProject = { id: "p", name: "Web", tasks: [task("plan", "awaiting_confirmation"), task("live", "running")] };
    expect(keys([p], { triage: "needs_you" })).toEqual(["project:p", "task:plan"]);
  });

  test("the active filter finds a task in intake", () => {
    const p: NavProject = { id: "p", name: "Web", tasks: [task("shape", "intake"), task("live", "running"), task("idle", "queued")] };
    expect(keys([p], { triage: "active" })).toEqual(["project:p", "task:shape", "task:live"]);
  });

  test("a query that hits an epic's title lists its done tasks", () => {
    const p: NavProject = { id: "p", name: "Web", epics: [{ id: "e", title: "Billing", tasks: [task("a", "done"), task("b", "queued")] }] };
    expect(keys([p], { query: "billing" })).toEqual(["project:p", "epic:e", "task:a", "task:b"]);
  });
});

describe("empty shells", () => {
  const p: NavProject = { id: "p", name: "Web", epics: [{ id: "e", title: "Billing", tasks: [task("a", "done"), task("b", "failed")] }], tasks: [task("c", "queued")] };

  test("an epic with only done work is not listed, and its project has no chevron", () => {
    const rows = flattenNav([p], OPEN);
    expect(rows.map((r) => r.key)).toEqual(["project:p"]);
    expect(rows[0]).toMatchObject({ expandable: false, expanded: false, forced: false });
  });

  test("the same epic is listed, open, while searching", () => {
    const rows = flattenNav([p], new Map(), { query: "task a" });
    expect(rows.map((r) => r.key)).toEqual(["project:p", "epic:e", "task:a"]);
    expect(rows.slice(0, 2).map((r) => [r.expandable, r.expanded])).toEqual([[true, true], [true, true]]);
  });

  test("an epic with work in progress keeps its chevron", () => {
    const live: NavProject = { ...p, epics: [{ id: "e", title: "Billing", tasks: [task("a", "done"), task("r", "running")] }] };
    const rows = flattenNav([live], new Map());
    expect(rows.map((r) => r.key)).toEqual(["project:p", "epic:e", "task:r"]);
    expect(rows[1]).toMatchObject({ expandable: true, expanded: true });
  });

  test("a project with nothing in progress draws its header and no Expand button", async () => {
    const el = await mount(<NavTree projects={[p]} />);
    const header = byRole(el, "treeitem", "Web");
    expect(allByRole(el, "treeitem").length).toBe(1);
    expect(header.hasAttribute("aria-expanded")).toBe(false);
    expect(allByRole(header, "button").length).toBe(0);
  });

  test("the selected epic keeps its row, with nothing under it", () => {
    const rows = flattenNav([p], OPEN, { selected: { kind: "epic", id: "e" } });
    expect(rows.map((r) => r.key)).toEqual(["project:p", "epic:e"]);
    expect(rows[1]).toMatchObject({ expandable: false, expanded: false });
  });
});

describe("a task is a leaf", () => {
  const asking: NavTask = task("t", "running", {
    runs: [{ id: "r", attempt: 1, status: "running", sessions: [
      { id: "orc", role: "conductor", status: "running", children: [{ id: "imp", role: "implementer", status: "running", activity: "Running bun test" }] },
      { id: "rev", role: "reviewer", status: "awaiting_input" },
    ] }],
  });
  const p: NavProject = { id: "p", name: "Web", tasks: [asking] };

  test("no session rows, not expandable; its triage still rolls up its sessions", () => {
    for (const filter of [{}, { triage: "needs_you" as const }, { query: "task" }]) {
      const rows = flattenNav([p], new Map([["task:t", true]]), filter);
      expect(rows.map((r) => r.key)).toEqual(["project:p", "task:t"]);
      expect(rows[1]).toMatchObject({ expandable: false, expanded: false, forced: false, triage: "needs_you" });
    }
  });

  test("→ on a task row does nothing", async () => {
    const el = await mount(<NavTree projects={[p]} />);
    const row = byRole(el, "treeitem", "T Task t");
    expect(row.hasAttribute("aria-expanded")).toBe(false);
    await act(async () => row.focus());
    await act(async () => {
      row.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true, cancelable: true }));
    });
    expect(allByRole(el, "treeitem").length).toBe(2);
    expect(document.activeElement).toBe(row);
  });
});

describe("the selection always has a row", () => {
  // Two attempts: r1 failed, r (the current one) completed.
  const shipped = task("shipped", "done", {
    runs: [
      { id: "r1", attempt: 1, status: "failed", sessions: [{ id: "orc1", role: "conductor", status: "failed" }] },
      { id: "r", attempt: 2, status: "completed", sessions: [{ id: "orc", role: "conductor", status: "completed", children: [{ id: "imp", role: "implementer", status: "completed" }] }] },
    ],
  });
  const live = task("live", "running", { runs: [{ id: "r2", attempt: 1, status: "running", sessions: [{ id: "work", role: "implementer", status: "running" }] }] });
  const p: NavProject = { id: "p", name: "Web", epics: [{ id: "e", title: "Billing", tasks: [shipped, task("also", "done"), live] }] };

  const selectedRow = (el: HTMLElement) => allByRole(el, "treeitem").filter((r) => r.getAttribute("aria-selected") === "true").map((r) => r.getAttribute("data-nav-key"));

  test("a selected done task is listed, and only it", () => {
    expect(keys([p], { selected: { kind: "task", id: "shipped" } })).toEqual(["project:p", "epic:e", "task:shipped", "task:live"]);
    expect(keys([p])).toEqual(["project:p", "epic:e", "task:live"]);
  });

  test("a done task opened from the board is highlighted", async () => {
    const el = await mount(<NavTree projects={[p]} selected={{ kind: "task", id: "shipped" }} />);
    expect(selectedRow(el)).toEqual(["task:shipped"]);
  });

  test.each([
    ["a running task's session", { kind: "session", id: "work" }, "task:live"],
    ["a done task's nested session", { kind: "session", id: "imp" }, "task:shipped"],
    ["a done task's earlier attempt", { kind: "run", id: "r1" }, "task:shipped"],
  ] as const)("selecting %s lists and highlights its task", async (_, ref, key) => {
    expect(keys([p], { selected: ref as NavRef })).toContain(key);
    const el = await mount(<NavTree projects={[p]} selected={ref as NavRef} />);
    expect(selectedRow(el)).toEqual([key]);
    expect(el.querySelector('[data-kind="session"]')).toBeNull();
    expect(el.querySelector('[data-kind="run"]')).toBeNull();
  });

  test("a project whose only listed row is the selected done task opens to it", async () => {
    const lone: NavProject = { id: "p", name: "Web", tasks: [task("d", "done")] };
    const el = await mount(<NavTree projects={[lone]} selected={{ kind: "task", id: "d" }} />);
    expect(selectedRow(el)).toEqual(["task:d"]);
    expect(byRole(el, "treeitem", "Web").getAttribute("aria-expanded")).toBe("true");
  });

  test("a session in a folded project and epic unfolds them to its task", async () => {
    let overrides: NavOverrides = new Map([["project:p", false], ["epic:e", false]]);
    const tree = (o: NavOverrides) => <NavTree projects={[p]} selected={{ kind: "session", id: "imp" }} expanded={o} onExpandedChange={(next) => (overrides = next)} />;
    await mount(tree(overrides));
    expect([...overrides]).toEqual([["project:p", true], ["epic:e", true]]);
    await act(async () => root!.render(<TooltipProvider>{tree(overrides)}</TooltipProvider>));
    expect(selectedRow(host!)).toEqual(["task:shipped"]);
  });
});

describe("a search drops the pin: the selection is listed only if it matches", () => {
  const p: NavProject = {
    id: "p",
    name: "Web",
    epics: [
      { id: "e", title: "Billing", tasks: [task("shipped", "done"), task("live", "running")] },
      { id: "quiet", title: "Docs", tasks: [task("old", "done")] },
    ],
    tasks: [task("plan", "awaiting_confirmation")],
  };

  test("a query", () => {
    expect(keys([p], { query: "task live", selected: { kind: "task", id: "shipped" } })).toEqual(["project:p", "epic:e", "task:live"]);
    expect(keys([p], { query: "task live", selected: { kind: "epic", id: "quiet" } })).toEqual(["project:p", "epic:e", "task:live"]);
  });

  test("the needs-you filter", () => {
    expect(keys([p], { triage: "needs_you", selected: { kind: "task", id: "shipped" } })).toEqual(["project:p", "task:plan"]);
    expect(keys([p], { triage: "needs_you", selected: { kind: "epic", id: "quiet" } })).toEqual(["project:p", "task:plan"]);
  });
});

describe("Mine keeps its own rule over the pin", () => {
  const me = { id: "me", name: "Me" };
  const them = { id: "them", name: "Them" };
  const p: NavProject = {
    id: "p",
    name: "Web",
    epics: [
      { id: "e", title: "Billing", tasks: [task("mine-done", "done", { people: [me] }), task("theirs-done", "done", { people: [them] }), task("mine-live", "running", { people: [me] })] },
      { id: "theirs", title: "Docs", tasks: [task("doc", "done", { people: [them] })] },
    ],
  };
  const mine = (selected: NavRef): NavFilter => ({ person: "me", you: "me", selected });

  test("a selected done task that is not yours is not listed", () => {
    expect(keys([p], mine({ kind: "task", id: "theirs-done" }), OPEN)).toEqual(["project:p", "epic:e", "task:mine-live"]);
  });

  test("a selected epic with nothing of yours is not listed", () => {
    expect(keys([p], mine({ kind: "epic", id: "theirs" }), OPEN)).toEqual(["project:p", "epic:e", "task:mine-live"]);
  });

  test("a selected done task that is yours is listed", () => {
    expect(keys([p], mine({ kind: "task", id: "mine-done" }), OPEN)).toEqual(["project:p", "epic:e", "task:mine-done", "task:mine-live"]);
  });
});

describe("the sidebar's no-matches state", () => {
  const me = { id: "me", name: "Me" };
  const p: NavProject = { id: "p", name: "Web", tasks: [task("old", "done", { people: [me], title: "Old invoice work" })] };

  test("Mine with nothing of yours in progress says so", async () => {
    const el = await mount(<Sidebar projects={[p]} you="me" mine />);
    expect(el.textContent).toContain("Nothing of yours in progress.");
    expect(allByRole(el, "treeitem").length).toBe(0);
  });

  test("Mine with your only task done and selected lists it, not the empty state", async () => {
    const el = await mount(<Sidebar projects={[p]} you="me" mine selected={{ kind: "task", id: "old" }} />);
    expect(allByRole(el, "treeitem").map((r) => r.getAttribute("data-nav-key"))).toEqual(["project:p", "task:old"]);
    expect(el.textContent).not.toContain("Nothing of yours in progress.");
  });

  test("a query finds the done task the default view leaves out", async () => {
    const el = await mount(<Sidebar projects={[p]} query="invoice" />);
    expect(allByRole(el, "treeitem").map((r) => r.getAttribute("data-nav-key"))).toEqual(["project:p", "task:old"]);
  });
});

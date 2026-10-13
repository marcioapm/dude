/**
 * The board's relations model (docs/design/task-relations.md, "View
 * model"): which cards are blocked, what each filter keeps, and an epic's
 * order in steps.
 */
import { describe, expect, test } from "bun:test";
import { boardColumns, boardFilter, boardFilterCounts, dependencySteps, isBlocked, isStartable, neighbourKeys, scopeTasks } from "../src/util/boardModel.ts";
import type { NavEpic, NavProject, NavTask } from "../src/util/navModel.ts";

const t = (key: string, status: NavTask["status"], more: Partial<NavTask> = {}): NavTask => ({ id: key, key, title: key, status, ...more });

const meter = t("CP-41", "review", { blocks: [{ key: "CP-44", title: "Billing", status: "received" }], edges: { blockedBy: ["CP-40"], blocks: ["CP-42", "CP-44"], related: [] } });
const billing = t("CP-44", "received", { blockedBy: [{ key: "CP-41", title: "Meter", status: "review" }], startable: false,
  edges: { blockedBy: ["CP-41"], blocks: [], related: ["CP-50"] } });
const free = t("CP-47", "received", { startable: true });
const spikeBlocked = t("CP-51", "queued", { blockedBy: [{ key: "CP-37", title: "Spike", status: "done", unmerged: true }] });
const running = t("CP-42", "running", { blockedBy: [{ key: "CP-41", title: "Meter", status: "review" }] });
const project: NavProject = { id: "p", name: "CP", tasks: [meter, billing, free, spikeBlocked, running] };

describe("blocked and startable", () => {
  test("a not-started task with an open blocker is blocked; a started one never is", () => {
    expect(isBlocked(billing)).toBe(true);
    expect(isBlocked(spikeBlocked)).toBe(true);
    expect(isBlocked(running)).toBe(false);
    expect(isBlocked(free)).toBe(false);
    const cards = boardColumns(project).flatMap((c) => c.cards);
    expect(cards.filter((c) => c.blocked).map((c) => c.task.key)).toEqual(["CP-44", "CP-51"]);
  });
  test("startable is the server's when it says, else not started and not blocked", () => {
    expect(isStartable(free)).toBe(true);
    expect(isStartable(billing)).toBe(false);
    expect(isStartable(t("CP-9", "received"))).toBe(true);
    expect(isStartable(t("CP-9", "running"))).toBe(false);
  });
});

describe("boardFilter", () => {
  test("All keeps every card; Can start and Blocked keep theirs, columns and counts recounted", () => {
    const cols = boardColumns(project);
    expect(boardFilter(cols, "all").flatMap((c) => c.cards)).toHaveLength(5);
    const start = boardFilter(cols, "startable");
    expect(start.flatMap((c) => c.cards.map((x) => x.task.key))).toEqual(["CP-47"]);
    expect(start.map((c) => c.kind)).toEqual(["backlog", "running", "review", "ready", "closed"]);
    expect(boardFilter(cols, "blocked").find((c) => c.kind === "backlog")!.cards.map((x) => x.task.key)).toEqual(["CP-44", "CP-51"]);
    expect(boardFilterCounts(scopeTasks(project))).toEqual({ all: 5, startable: 1, blocked: 2 });
  });
});

test("a card's neighbours are every task it relates to, open or merged", () => {
  expect([...neighbourKeys(meter)].sort()).toEqual(["CP-40", "CP-42", "CP-44"]);
  expect([...neighbourKeys(billing)].sort()).toEqual(["CP-41", "CP-50"]);
});

describe("dependencySteps", () => {
  test("each task one step after the last of its in-epic blockers; an outside blocker places nothing", () => {
    const epic: NavEpic = { id: "e", title: "Billing", tasks: [
      t("CP-40", "done", { edges: { blockedBy: [], blocks: ["CP-41"], related: [] } }),
      t("CP-41", "review", { edges: { blockedBy: ["CP-40"], blocks: ["CP-44"], related: [] } }),
      t("CP-43", "running", { edges: { blockedBy: ["WEB-12"], blocks: ["CP-44"], related: [] } }),
      t("CP-44", "received", { edges: { blockedBy: ["CP-41", "CP-43"], blocks: [], related: [] } }),
      t("CP-45", "received", { edges: { blockedBy: ["CP-44"], blocks: [], related: [] } }),
    ] };
    expect(dependencySteps(epic).map((s) => s.map((x) => x.key))).toEqual([["CP-40", "CP-43"], ["CP-41"], ["CP-44"], ["CP-45"]]);
  });
  test("a loop among an epic's tasks goes in the last step", () => {
    const epic: NavEpic = { id: "e", title: "Loop", tasks: [
      t("A-1", "received"),
      t("A-2", "received", { edges: { blockedBy: ["A-3"], blocks: [], related: [] } }),
      t("A-3", "received", { edges: { blockedBy: ["A-2"], blocks: [], related: [] } }),
    ] };
    const steps = dependencySteps(epic).map((s) => s.map((x) => x.key));
    expect(steps[0]).toEqual(["A-1"]);
    expect(steps.at(-1)!.sort()).toEqual(["A-2", "A-3"]);
  });
});

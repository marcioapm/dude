/**
 * Whose asks are whose: given the viewer, only what they can answer (their
 * tasks, and tasks nobody owns) needs them; what waits on someone else is
 * calm everywhere — the tree, the chips, the board — and listed apart.
 */

import { describe, expect, test } from "bun:test";
import { boardColumns } from "../src/util/boardModel.ts";
import { attentionItems, globalCounts, isYours, taskTriage, waitingSplit, type NavProject, type NavTask } from "../src/util/navModel.ts";

const ana = { id: "per_ana", name: "Ana" };
const bo = { id: "per_bo", name: "Bo" };

const asking = (id: string, people: NavTask["people"], statusSince: string): NavTask => ({
  id, title: id, status: "running", people, statusSince,
  runs: [{ id: `r-${id}`, attempt: 1, status: "running", sessions: [{ id: `s-${id}`, role: "implementer", status: "awaiting_input", activity: "Which?" }] }],
});

const projects: NavProject[] = [{
  id: "p", name: "P",
  tasks: [
    asking("bos", [bo, ana], "2026-01-01T00:00:00Z"),
    asking("anas", [ana], "2026-01-02T00:00:00Z"),
    asking("nobodys", [], "2026-01-03T00:00:00Z"),
    { id: "calm", title: "calm", status: "running", people: [bo] },
  ],
}];

describe("the viewer", () => {
  test("a task waits on its owner, the first of its people; nobody's is anyone's", () => {
    const [bos, anas, nobodys] = projects[0]!.tasks!;
    expect(isYours(bos!, ana.id)).toBe(false);
    expect(isYours(anas!, ana.id)).toBe(true);
    expect(isYours(nobodys!, ana.id)).toBe(true);
    // Being on a task is not owning it: Ana is Bo's second.
    expect(taskTriage(bos!, ana.id)).toBe("waiting");
    expect(taskTriage(bos!, bo.id)).toBe("needs_you");
  });

  test("with no viewer, every ask needs you, as before people", () => {
    expect(globalCounts(projects).needs_you).toBe(3);
    expect(waitingSplit(attentionItems(projects), null).others).toEqual([]);
  });

  test("the chips count only yours", () => {
    expect(globalCounts(projects, ana.id).needs_you).toBe(2);
    expect(globalCounts(projects, bo.id).needs_you).toBe(2);
  });

  test("waiting on you, then on others, each oldest first", () => {
    const { yours, others } = waitingSplit(attentionItems(projects), ana.id);
    expect(yours.map((it) => it.task.id)).toEqual(["anas", "nobodys"]);
    expect(others.map((it) => it.task.id)).toEqual(["bos"]);
  });

  test("the board's needs-you cards are yours alone", () => {
    const cards = boardColumns(projects[0]!, null, ana.id).flatMap((c) => c.cards);
    expect(cards.filter((c) => c.triage === "needs_you").map((c) => c.task.id).sort()).toEqual(["anas", "nobodys"]);
  });
});

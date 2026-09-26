import { describe, expect, test } from "bun:test";
import { boardSwimlanes, NO_EPIC_LANE } from "../src/util/boardModel.ts";
import { countFindings, sortFindings } from "../src/components/FindingRow.tsx";
import { elideMiddle } from "../src/components/Breadcrumb.tsx";
import { textareaHeight } from "../src/primitives/Textarea.tsx";
import { chain, focusIsFree, isContextMenuKey, rowMenuOpeners } from "../src/primitives/RowMenu.tsx";
import type { NavProject } from "../src/util/navModel.ts";

describe("textareaHeight", () => {
  test("never below `rows` lines", () => {
    expect(textareaHeight(0, 3, 12)).toBe(3 * 20 + 12);
  });
  test("grows with content", () => {
    expect(textareaHeight(5 * 20 + 12, 3, 12)).toBe(5 * 20 + 12);
  });
  test("stops at `maxRows`", () => {
    expect(textareaHeight(40 * 20 + 12, 3, 12)).toBe(12 * 20 + 12);
  });
  test("maxRows below rows is treated as rows", () => {
    expect(textareaHeight(999, 4, 2)).toBe(4 * 20 + 12);
  });
});

describe("elideMiddle", () => {
  test("short text passes through", () => {
    expect(elideMiddle("Webhook reliability", 32)).toBe("Webhook reliability");
  });
  test("keeps head and tail", () => {
    const out = elideMiddle("Webhook reliability and retries", 16);
    expect(out).toContain("…");
    expect(out.startsWith("Webhook")).toBe(true);
    expect(out.endsWith("retries")).toBe(true);
    expect(Array.from(out).length).toBeLessThanOrEqual(16);
  });
  test("counts code points, not UTF-16 units", () => {
    const out = elideMiddle("😀".repeat(20), 9);
    expect(Array.from(out).length).toBe(9);
  });
  test("budget too small to elide returns the text", () => {
    expect(elideMiddle("abcdef", 2)).toBe("abcdef");
  });
});

describe("sortFindings", () => {
  const f = (id: string, severity: "blocking" | "high" | "medium" | "low" | "note", status: "open" | "resolved" | "superseded" | "accepted") => ({ id, severity, status });
  test("open first, then by severity, then by the caller's order", () => {
    const sorted = sortFindings([f("a", "low", "resolved"), f("b", "note", "open"), f("c", "blocking", "resolved"), f("d", "high", "open"), f("e", "high", "open")]);
    expect(sorted.map((x) => x.id)).toEqual(["d", "e", "b", "c", "a"]);
  });
  test("does not mutate the input", () => {
    const input = [f("a", "low", "open"), f("b", "blocking", "open")];
    sortFindings(input);
    expect(input.map((x) => x.id)).toEqual(["a", "b"]);
  });
  test("counts open, settled and blocking (severity blocking only, open only)", () => {
    expect(countFindings([f("a", "blocking", "open"), f("b", "high", "open"), f("c", "blocking", "resolved"), f("d", "note", "open")])).toEqual({ open: 3, settled: 1, blocking: 1 });
  });
});

describe("boardSwimlanes", () => {
  const project: NavProject = {
    id: "p",
    name: "p",
    epics: [
      { id: "e2", title: "Second", tasks: [{ id: "w1", title: "one", status: "running" }] },
      { id: "e1", title: "First", tasks: [] },
    ],
    tasks: [{ id: "w2", title: "loose", status: "done" }, { id: "w3", title: "loose 2", status: "queued" }],
  };
  test("one lane per epic in the project's order, then No epic", () => {
    const lanes = boardSwimlanes(project);
    expect(lanes.map((l) => l.key)).toEqual(["epic:e2", "epic:e1", NO_EPIC_LANE]);
    expect(lanes.map((l) => l.title)).toEqual(["Second", "First", "No epic"]);
    expect(lanes.map((l) => l.count)).toEqual([1, 0, 2]);
  });
  test("each lane has all five columns", () => {
    for (const lane of boardSwimlanes(project)) expect(lane.columns.map((c) => c.kind)).toEqual(["intake", "queued", "running", "review", "closed"]);
  });
  test("cards land in their column within the lane", () => {
    const none = boardSwimlanes(project)[2]!;
    expect(none.columns.find((c) => c.kind === "closed")!.cards.map((c) => c.task.id)).toEqual(["w2"]);
    expect(none.columns.find((c) => c.kind === "queued")!.cards.map((c) => c.task.id)).toEqual(["w3"]);
  });
  test("no loose tasks: no No-epic lane", () => {
    expect(boardSwimlanes({ ...project, tasks: [] }).map((l) => l.key)).toEqual(["epic:e2", "epic:e1"]);
  });
});

describe("row menu openers", () => {
  test("Shift+F10 and the ContextMenu key open; plain F10 does not", () => {
    expect(isContextMenuKey({ key: "F10", shiftKey: true })).toBe(true);
    expect(isContextMenuKey({ key: "ContextMenu", shiftKey: false })).toBe(true);
    expect(isContextMenuKey({ key: "F10", shiftKey: false })).toBe(false);
    expect(isContextMenuKey({ key: "ArrowDown", shiftKey: true })).toBe(false);
  });
  test("handlers open only on their keys and leave the rest to the row", () => {
    let opened = 0;
    const h = rowMenuOpeners(() => (opened += 1));
    const ev = (key: string, shiftKey = false) => {
      let prevented = false;
      let stopped = false;
      return {
        key,
        shiftKey,
        preventDefault: () => (prevented = true),
        stopPropagation: () => (stopped = true),
        get prevented() {
          return prevented;
        },
        get stopped() {
          return stopped;
        },
      };
    };
    const arrow = ev("ArrowDown");
    h.onKeyDown(arrow as never);
    expect(opened).toBe(0);
    expect(arrow.prevented).toBe(false);
    expect(arrow.stopped).toBe(false);
    const f10 = ev("F10", true);
    h.onKeyDown(f10 as never);
    expect(opened).toBe(1);
    expect(f10.prevented).toBe(true);
    expect(f10.stopped).toBe(true);
    const ctx = ev("contextmenu");
    h.onContextMenu(ctx as never);
    expect(opened).toBe(2);
    expect(ctx.prevented).toBe(true);
  });
});

describe("focus return after a row menu closes", () => {
  const body = {} as Element;
  test("free when nothing or the body has focus", () => {
    expect(focusIsFree({ activeElement: null, body })).toBe(true);
    expect(focusIsFree({ activeElement: body, body })).toBe(true);
  });
  test("taken when another element holds it (a second row's trigger was clicked)", () => {
    expect(focusIsFree({ activeElement: {} as Element, body })).toBe(false);
  });
});

describe("chain", () => {
  test("runs the slot's handler first, then ours; tolerates a missing one", () => {
    const order: string[] = [];
    chain(
      () => order.push("theirs"),
      () => order.push("ours"),
    )(null);
    chain(undefined, () => order.push("alone"))(null);
    expect(order).toEqual(["theirs", "ours", "alone"]);
  });
});

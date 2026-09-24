/**
 * Swimlanes on the board: the model and the markup for the two shapes that
 * bit in production — a lane whose cards all sit in one column, and a
 * "No epic" lane that follows an empty epic. The lane's CSS used to size
 * itself to max-content, so one long card title stretched all five tracks
 * of that lane to its width and pushed the cards off the screen; the
 * markup was right and the width was not. The layout is CSS, which a DOM-
 * less test cannot see, so this pins the structure and the stylesheet's
 * width rule.
 */
import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { renderToStaticMarkup } from "react-dom/server";
import { Board } from "../src/components/Board.tsx";
import { boardSwimlanes } from "../src/util/boardModel.ts";
import type { NavProject } from "../src/util/navModel.ts";

const LONG = "Create an .md file with an example table some bullet points, some title and some emojis... maybe a code block too";

/** Two epics (the second empty), six loose items all in Review — textkit's shape. */
const textkit: NavProject = {
  id: "p",
  name: "textkit",
  epics: [
    { id: "e_util", title: "Text utilities", workItems: [1, 2, 3, 4].map((n) => ({ id: `u${n}`, key: `TEXT-${n}`, title: `util ${n}`, status: "review" })) },
    { id: "e_cli", title: "Command line", workItems: [] },
  ],
  workItems: [5, 6, 7, 8, 9, 10].map((n) => ({ id: `l${n}`, key: `TEXT-${n}`, title: n === 6 ? LONG : `loose ${n}`, status: "review" })),
};

const laneOf = (html: string, key: string) => {
  const start = html.indexOf(`data-lane="${key}"`);
  expect(start).toBeGreaterThan(-1);
  const rest = html.slice(start);
  const next = rest.indexOf("data-lane=", 12);
  return next === -1 ? rest : rest.slice(0, next);
};
const cardsIn = (html: string) => [...html.matchAll(/data-board-key="workItem:([^"]+)"/g)].map((m) => m[1]);

describe("boardSwimlanes: cards in one column only", () => {
  test("the lane keeps all five columns; the one with cards has them, the rest are empty", () => {
    const lane = boardSwimlanes(textkit)[0]!;
    expect(lane.columns.map((c) => c.kind)).toEqual(["intake", "queued", "running", "review", "closed"]);
    expect(lane.columns.map((c) => c.cards.length)).toEqual([0, 0, 0, 4, 0]);
    expect(lane.count).toBe(4);
  });
  test("No epic comes after the empty epic and holds every loose item", () => {
    const lanes = boardSwimlanes(textkit);
    expect(lanes.map((l) => [l.key, l.count])).toEqual([
      ["epic:e_util", 4],
      ["epic:e_cli", 0],
      ["none", 6],
    ]);
    expect(lanes[2]!.columns.find((c) => c.kind === "review")!.cards.map((c) => c.workItem.id)).toEqual(["l5", "l6", "l7", "l8", "l9", "l10"]);
  });
});

describe("Board groupBy=epic markup", () => {
  const html = renderToStaticMarkup(<Board project={textkit} groupBy="epic" hideHeader />);

  test("every lane is rendered, in order, with its count", () => {
    const order = [...html.matchAll(/data-lane="([^"]+)"/g)].map((m) => m[1]);
    expect(order).toEqual(["epic:e_util", "epic:e_cli", "none"]);
  });
  test("a lane with cards in one column draws five cells: four empty, one with the cards", () => {
    const lane = laneOf(html, "epic:e_util");
    expect((lane.match(/data-column="/g) ?? []).length).toBe(5);
    expect(cardsIn(lane)).toEqual(["u1", "u2", "u3", "u4"]);
    const review = lane.slice(lane.indexOf('data-column="review"'), lane.indexOf('data-column="closed"'));
    expect(cardsIn(review)).toHaveLength(4);
  });
  test("the empty epic says so and draws no columns", () => {
    const lane = laneOf(html, "epic:e_cli");
    expect(lane).toContain("Nothing in this epic yet.");
    expect(lane).not.toContain("data-column=");
  });
  test("the No epic lane after an empty epic still renders all six cards", () => {
    const lane = laneOf(html, "none");
    expect(lane).toContain("No epic");
    expect((lane.match(/data-column="/g) ?? []).length).toBe(5);
    expect(cardsIn(lane)).toEqual(["l5", "l6", "l7", "l8", "l9", "l10"]);
  });
  test("cards in swimlanes do not repeat the epic name", () => {
    expect(laneOf(html, "epic:e_util")).not.toContain(">Text utilities</span></span>");
  });
});

describe("Board swimlane CSS", () => {
  const css = readFileSync(`${import.meta.dir}/../src/components/Board.module.css`, "utf8");
  test("no lane sizes itself to its content (that is what stretched the tracks)", () => {
    const rules = [...css.matchAll(/\.(lane|laneHead|laneColumns|columnInLane)\s*\{[^}]*\}/g)].map((m) => m[0]);
    expect(rules.length).toBeGreaterThanOrEqual(3);
    for (const r of rules) expect(r).not.toMatch(/max-content|min-content|fit-content/);
  });
  test("a lane column clips, so a long title cannot widen its track", () => {
    expect(css).toMatch(/\.columnInLane\s*\{[^}]*overflow:\s*(hidden|clip)/);
  });
});

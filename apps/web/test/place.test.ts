import { describe, expect, test } from "bun:test";
import { formatPlace, parsePlace } from "../src/place.ts";

describe("places", () => {
  test("a task's link reads back as the task", () => {
    const place = parsePlace("#/task/wi_1");
    expect(place).toEqual({ view: "tree", ref: { kind: "task", id: "wi_1" } });
    expect(formatPlace(place)).toBe("#/task/wi_1");
  });

  test("a link from before tasks were work items still opens the task", () => {
    expect(parsePlace("#/workItem/wi_1")).toEqual({ view: "tree", ref: { kind: "task", id: "wi_1" } });
  });
});

import { describe, expect, test } from "bun:test";
import { formatPlace, inTree, parsePlace } from "../src/place.ts";

describe("places", () => {
  test("a task's link reads back as the task", () => {
    const place = parsePlace("#/task/wi_1");
    expect(place).toEqual({ view: "tree", ref: { kind: "task", id: "wi_1" } });
    expect(formatPlace(place)).toBe("#/task/wi_1");
  });

  test("a link from before tasks were work items still opens the task", () => {
    expect(parsePlace("#/workItem/wi_1")).toEqual({ view: "tree", ref: { kind: "task", id: "wi_1" } });
  });

  test("a link to a task's Servers tab reads back as the task on that tab", () => {
    const place = parsePlace("#/task/wi_1/servers");
    expect(place).toEqual({ view: "tree", ref: { kind: "task", id: "wi_1" }, tab: "servers" });
    expect(formatPlace(place)).toBe("#/task/wi_1/servers");
    expect(formatPlace(inTree({ kind: "task", id: "wi_1" }, "servers"))).toBe("#/task/wi_1/servers");
    // Only a task has tabs a URL names; anything else after the id is ignored.
    expect(parsePlace("#/task/wi_1/nonsense")).toEqual({ view: "tree", ref: { kind: "task", id: "wi_1" } });
    expect(parsePlace("#/session/run_1/servers")).toEqual({ view: "tree", ref: { kind: "session", id: "run_1" } });
  });

  test("a settings page is part of its place", () => {
    for (const hash of ["#/org/settings", "#/org/settings/reviewer", "#/project/prj_1/settings", "#/project/prj_1/settings/delivery"]) {
      expect(formatPlace(parsePlace(hash))).toBe(hash);
    }
    expect(parsePlace("#/project/prj_1/settings/fixer")).toEqual({ view: "projectSettings", projectId: "prj_1", page: "fixer" });
  });

  test("an image, its tab, and a build are deeper than the Images page, and read back", () => {
    for (const hash of ["#/org/settings/images/img_1", "#/org/settings/images/img_1/history", "#/org/settings/images/builds/imb_1"]) {
      expect(formatPlace(parsePlace(hash))).toBe(hash);
    }
    expect(parsePlace("#/org/settings/images/img_1/history")).toEqual({ view: "orgSettings", page: "images", sub: "img_1/history" });
    expect(parsePlace("#/org/settings/images")).toEqual({ view: "orgSettings", page: "images" });
  });
});

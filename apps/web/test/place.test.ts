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

  test("a task's tab and an earlier attempt read back; Activity and Servers never carry an attempt", () => {
    const task = { kind: "task" as const, id: "wi_1" };
    expect(parsePlace("#/task/wi_1/findings?attempt=1")).toEqual({ view: "tree", ref: task, tab: "findings", attempt: 1 });
    expect(parsePlace("#/task/wi_1?attempt=2")).toEqual({ view: "tree", ref: task, attempt: 2 });
    for (const hash of ["#/task/wi_1?attempt=1", "#/task/wi_1/findings?attempt=1", "#/task/wi_1/sessions?attempt=3", "#/task/wi_1/files", "#/task/wi_1/activity"]) {
      expect(formatPlace(parsePlace(hash))).toBe(hash);
    }
    expect(formatPlace(inTree(task, "findings", 1))).toBe("#/task/wi_1/findings?attempt=1");
    expect(formatPlace(inTree(task, undefined, undefined))).toBe("#/task/wi_1");
    expect(formatPlace(inTree(task, "activity", 1))).toBe("#/task/wi_1/activity");
    expect(formatPlace(inTree(task, "servers", 1))).toBe("#/task/wi_1/servers");
    expect(parsePlace("#/task/wi_1/activity?attempt=1")).toEqual({ view: "tree", ref: task, tab: "activity" });
    // Nothing but a positive whole number is an attempt.
    for (const bad of ["0", "-1", "x", "1.5", ""]) expect(parsePlace(`#/task/wi_1?attempt=${bad}`)).toEqual({ view: "tree", ref: task });
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

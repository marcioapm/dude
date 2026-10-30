import { describe, expect, test } from "bun:test";
import { formatPlace, inTree, parsePlace } from "../src/place.ts";

describe("places", () => {
  test("Waiting keeps the original link and names the others tab, including tenant links", () => {
    expect(parsePlace("#/waiting")).toEqual({ view: "inbox" });
    expect(formatPlace({ view: "inbox" })).toBe("#/waiting");
    expect(formatPlace({ view: "inbox", tab: "others" })).toBe("#/waiting/others");
    expect(parsePlace("#/o/example/waiting/others")).toEqual({ view: "inbox", tab: "others" });
  });
  test("nothing in the URL is the welcome, and the welcome's URL is #/", () => {
    for (const hash of ["", "#", "#/", "# "]) expect(parsePlace(hash)).toEqual({ view: "welcome" });
    expect(formatPlace({ view: "welcome" })).toBe("#/");
    expect(parsePlace(formatPlace({ view: "welcome" }))).toEqual({ view: "welcome" });
    // Something that is not a place is still none, not the welcome.
    expect(parsePlace("#/nonsense")).toBeNull();
  });

  test("a task's link reads back as the task", () => {
    const place = parsePlace("#/task/wi_1");
    expect(place).toEqual({ view: "tree", ref: { kind: "task", id: "wi_1" } });
    expect(formatPlace(place)).toBe("#/task/wi_1");
  });

  test("a task's Changes tab has its own link, kept with an earlier attempt", () => {
    const task = { kind: "task" as const, id: "wi_1" };
    expect(parsePlace("#/task/wi_1/changes?attempt=1")).toEqual({ view: "tree", ref: task, tab: "changes", attempt: 1 });
    expect(formatPlace(inTree(task, "changes", 1))).toBe("#/task/wi_1/changes?attempt=1");
  });

  test("an epic's Coordination view has its own link; the board is the epic's own", () => {
    const place = parsePlace("#/epic/ep_1/coordination");
    expect(place).toEqual({ view: "tree", ref: { kind: "epic", id: "ep_1" }, epicView: "coordination" });
    expect(formatPlace(place)).toBe("#/epic/ep_1/coordination");
    expect(formatPlace({ view: "tree", ref: { kind: "epic", id: "ep_1" }, epicView: "board" })).toBe("#/epic/ep_1");
    expect(parsePlace("#/epic/ep_1")).toEqual({ view: "tree", ref: { kind: "epic", id: "ep_1" } });
    // Only an epic has the view.
    expect(parsePlace("#/project/p_1/coordination")).toEqual({ view: "tree", ref: { kind: "project", id: "p_1" } });
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

  test("an automation, its runs and one run are deeper than Automations, in the organisation's settings and a project's", () => {
    for (const hash of ["#/org/settings/automations/aut_1", "#/org/settings/automations/aut_1/runs/arn_2",
      "#/project/prj_1/settings/automations/aut_1", "#/project/prj_1/settings/automations/aut_1/runs/arn_2"]) {
      expect(formatPlace(parsePlace(hash))).toBe(hash);
    }
    expect(parsePlace("#/project/prj_1/settings/automations/aut_1/runs"))
      .toEqual({ view: "projectSettings", projectId: "prj_1", page: "automations", sub: "aut_1/runs" });
  });

  test("a project's Servers page reads back, in an organisation too; it is not the project's board", () => {
    expect(parsePlace("#/project/prj_1/servers")).toEqual({ view: "projectServers", projectId: "prj_1" });
    expect(formatPlace({ view: "projectServers", projectId: "prj_1" })).toBe("#/project/prj_1/servers");
    expect(parsePlace("#/o/acme/project/prj%201/servers")).toEqual({ view: "projectServers", projectId: "prj 1" });
    expect(parsePlace(formatPlace({ view: "projectServers", projectId: "p/2" }))).toEqual({ view: "projectServers", projectId: "p/2" });
    // Anything deeper is not a page of its own.
    expect(parsePlace("#/project/prj_1/servers/x")).toEqual({ view: "tree", ref: { kind: "project", id: "prj_1" } });
  });

  test("help, a guide page and a section of it read back, in an organisation as the guide links them", () => {
    for (const hash of ["#/help", "#/help/settings", "#/help/settings/delivery"]) {
      expect(formatPlace(parsePlace(hash))).toBe(hash);
    }
    expect(parsePlace("#/help")).toEqual({ view: "help" });
    expect(parsePlace("#/o/acme/help/settings/delivery")).toEqual({ view: "help", page: "settings", section: "delivery" });
    expect(parsePlace("#/o/acme/help/memory")).toEqual({ view: "help", page: "memory" });
    expect(formatPlace({ view: "help", page: "a b", section: "c/d" })).toBe("#/help/a%20b/c%2Fd");
    expect(parsePlace("#/help/a%20b/c%2Fd")).toEqual({ view: "help", page: "a b", section: "c/d" });
  });

  test("a preview's Connect to dude lands on its own place, apart from a chat provider's connect", () => {
    expect(formatPlace({ view: "previewConnect" })).toBe("#/preview-connect");
    expect(parsePlace("#/preview-connect")).toEqual({ view: "previewConnect" });
    expect(parsePlace("#/o/acme/preview-connect")).toEqual({ view: "previewConnect" });
    expect(parsePlace(formatPlace({ view: "previewConnect" }))).toEqual({ view: "previewConnect" });
    expect(parsePlace("#/connect/slack?state=s")).toEqual({ view: "connect", provider: "slack", state: "s" });
    expect(parsePlace("#/preview-connect/extra")).toBeNull();
  });
});

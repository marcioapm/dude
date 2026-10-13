import { describe, expect, test } from "bun:test";
import { START_STAGES, shownRunStatus } from "../src/index.ts";

describe("a Run's status as shown while it starts", () => {
  test("scheduled reads Scheduled while it waits for a machine, Starting once lux has placed it", () => {
    expect(shownRunStatus("scheduled", null)).toBe("scheduled");
    expect(shownRunStatus("scheduled", "waiting")).toBe("scheduled");
    for (const stage of ["image", "volumes", "repositories", "container", "running"] as const) {
      expect(shownRunStatus("scheduled", stage)).toBe("starting");
    }
  });

  test("a move's park is not a placement; any other status is its own, whatever the stage", () => {
    expect(shownRunStatus("scheduled", "park")).toBe("scheduled");
    for (const status of ["pending", "running", "paused", "failed", "completed", "aborted"] as const) {
      for (const stage of START_STAGES) expect(shownRunStatus(status, stage)).toBe(status);
    }
    expect(shownRunStatus("scheduled", "booting")).toBe("scheduled");
  });
});

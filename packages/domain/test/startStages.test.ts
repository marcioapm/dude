import { describe, expect, test } from "bun:test";
import { FRESH_START_STAGES, MOVE_STAGES, OPTIONAL_START_STAGES, RESUME_STAGES, START_STAGES, START_STAGE_WORDS, isStartStage, setupStageWords, shownRunStatus } from "../src/index.ts";

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

describe("a repository's setup script as a start stage", () => {
  test("setup sits between the container and the agent in every start's stages, drawn only when it occurs", () => {
    for (const stages of [FRESH_START_STAGES, RESUME_STAGES, MOVE_STAGES]) {
      expect(stages.indexOf("setup")).toBe(stages.indexOf("container") + 1);
      expect(stages.indexOf("running")).toBe(stages.indexOf("setup") + 1);
    }
    expect(START_STAGES.indexOf("setup")).toBe(START_STAGES.indexOf("container") + 1);
    expect(OPTIONAL_START_STAGES).toEqual(["setup"]);
    expect(isStartStage("setup")).toBe(true);
  });

  test("its words name the repository when the payload does", () => {
    expect(setupStageWords("abs")).toBe("Running abs\u2019s setup script");
    expect(setupStageWords(null)).toBe("Running the setup script");
    expect(START_STAGE_WORDS.setup.done).toBe("Setup script ran");
  });
});

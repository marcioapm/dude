import { describe, expect, test } from "bun:test";
import { attemptOfPr, attemptOfWork, attemptsOf } from "../src/attempts.ts";
import { PULL_REQUEST, RESTARTED_RUNS, run } from "../src/fixtures/data.ts";

describe("what belongs to which attempt", () => {
  const runs = RESTARTED_RUNS;

  test("a task's attempts are its agent Runs', newest first; a preview adds none", () => {
    const preview = run({ id: "run_prev", attempt: 7, kind: "preview", phase: null, role: null, status: "running" });
    expect(attemptsOf([...runs, preview])).toEqual([2, 1]);
  });

  test("a finding or a file is its Run's attempt", () => {
    expect(attemptOfWork("run_a1_review", runs, 2)).toBe(1);
    expect(attemptOfWork("run_a2_review", runs, 2)).toBe(2);
  });

  test("one whose Run is unknown — gone, or never recorded — is the current attempt's", () => {
    expect(attemptOfWork(null, runs, 2)).toBe(2);
    expect(attemptOfWork("run_deleted", runs, 2)).toBe(2);
  });

  test("one a branch preview made is the current attempt's", () => {
    const preview = run({ id: "run_prev", attempt: 1, kind: "preview", phase: null, role: null, status: "running" });
    expect(attemptOfWork("run_prev", [...runs, preview], 2)).toBe(2);
  });

  test("a pull request is the attempt of the Run that opened it", () => {
    expect(attemptOfPr({ ...PULL_REQUEST, runId: "run_a1_simplify" }, runs)).toBe(1);
    expect(attemptOfPr({ ...PULL_REQUEST, runId: "run_a2_simplify" }, runs)).toBe(2);
  });
});

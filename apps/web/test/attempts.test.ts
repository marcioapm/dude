import { describe, expect, test } from "bun:test";
import { attemptOfPr, attemptOfWork, attemptsOf, closedAtStartOver, prOfEvent, runsById } from "../src/attempts.ts";
import { PULL_REQUEST, RESTARTED_RUNS, run } from "../src/fixtures/data.ts";
import type { PersistedEvent } from "@dude/domain";

describe("what belongs to which attempt", () => {
  const runs = runsById(RESTARTED_RUNS);

  test("a task's attempts are its agent Runs', newest first; a preview adds none", () => {
    const preview = run({ id: "run_prev", attempt: 7, kind: "preview", phase: null, role: null, status: "running" });
    expect(attemptsOf([...RESTARTED_RUNS, preview])).toEqual([2, 1]);
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
    expect(attemptOfWork("run_prev", runsById([...RESTARTED_RUNS, preview]), 2)).toBe(2);
  });

  test("a pull request is the attempt of the Run that opened it", () => {
    expect(attemptOfPr({ ...PULL_REQUEST, runId: "run_a1_simplify" }, runs, 2)).toBe(1);
    expect(attemptOfPr({ ...PULL_REQUEST, runId: "run_a2_simplify" }, runs, 2)).toBe(2);
  });

  test("one whose Run is unknown is the attempt its branch names", () => {
    expect(attemptOfPr({ ...PULL_REQUEST, runId: null, headBranch: "dude/task_wc214/attempt-1" }, runs, 3)).toBe(1);
    expect(attemptOfPr({ ...PULL_REQUEST, runId: "run_deleted", headBranch: "dude/task_wc214/attempt-2" }, runs, 3)).toBe(2);
  });

  test("one whose Run is unknown and whose branch names no attempt is the current one's", () => {
    expect(attemptOfPr({ ...PULL_REQUEST, runId: null, headBranch: "feature/checkout" }, runs, 3)).toBe(3);
    expect(attemptOfPr({ ...PULL_REQUEST, runId: "run_deleted", headBranch: "dude/task_wc214/attempt-" }, runs, 3)).toBe(3);
  });
});

describe("who closed an earlier attempt's pull request", () => {
  const pr = { ...PULL_REQUEST, number: 478, repositoryName: "example/web-console" };
  const restartAt = "2026-10-01T10:00:00.000Z";
  const closed = (occurredAt: string, actor: PersistedEvent["actor"], number = 478) =>
    ({ eventType: "pull_request.closed", occurredAt, payload: { number, repo: "example/web-console" }, actor }) as unknown as PersistedEvent;
  const forge = { type: "system", id: "forge" } as PersistedEvent["actor"];

  test("closed by the sync at or after the start over: dude's close, when it happened", () => {
    expect(closedAtStartOver(pr, [closed("2026-10-01T10:00:05.000Z", forge)], restartAt)).toBe("2026-10-01T10:00:05.000Z");
  });

  test("closed before the start over: someone else's", () => {
    expect(closedAtStartOver(pr, [closed("2026-10-01T09:30:00.000Z", forge)], restartAt)).toBeNull();
  });

  test("closed by a person here, or never recorded, or another pull request's: not dude's", () => {
    expect(closedAtStartOver(pr, [closed("2026-10-01T10:00:05.000Z", { type: "human", id: "u_ana" } as PersistedEvent["actor"])], restartAt)).toBeNull();
    expect(closedAtStartOver(pr, [], restartAt)).toBeNull();
    expect(closedAtStartOver(pr, [closed("2026-10-01T10:00:05.000Z", forge, 479)], restartAt)).toBeNull();
    expect(closedAtStartOver(pr, [closed("2026-10-01T10:00:05.000Z", forge)], null)).toBeNull();
  });

  test("closed by someone before the start over, reopened, then closed by it: dude's close", () => {
    const events = [closed("2026-10-01T09:30:00.000Z", forge), closed("2026-10-01T10:00:05.000Z", forge)];
    expect(closedAtStartOver(pr, events, restartAt)).toBe("2026-10-01T10:00:05.000Z");
  });

  test("a close of the same number in another repository is not this one's", () => {
    const other = { ...closed("2026-10-01T10:00:05.000Z", forge), payload: { number: 478, repo: "example/api" } } as PersistedEvent;
    expect(closedAtStartOver(pr, [other], restartAt)).toBeNull();
    // This one's own close, before the start over, then the other's after it.
    expect(closedAtStartOver(pr, [closed("2026-10-01T09:30:00.000Z", forge), other], restartAt)).toBeNull();
  });
});

describe("which pull request an event is about", () => {
  const web = { ...PULL_REQUEST, id: "pr_web", number: 12, repositoryName: "example/web-console" };
  const api = { ...PULL_REQUEST, id: "pr_api", number: 12, repositoryName: "example/api" };
  const about = (repo: string) => ({ eventType: "pull_request.checks_changed", payload: { number: 12, repo } }) as unknown as PersistedEvent;

  test("two with the same number in different repositories: the event's repository's", () => {
    expect(prOfEvent(about("example/api"), [web, api])?.id).toBe("pr_api");
    expect(prOfEvent(about("example/web-console"), [api, web])?.id).toBe("pr_web");
  });
});

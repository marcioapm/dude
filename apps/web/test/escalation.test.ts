/**
 * Why delivery stopped for a person, in words: each reason the workflow
 * escalates with (delivery.escalate) says what happened, and where to look
 * when it names a Run.
 */

import { describe, expect, test } from "bun:test";
import type { Escalation } from "@dude/domain";
import { escalationWords as wordsFor, shortError } from "../src/escalation.ts";

const at = "2026-01-01T00:00:00Z";
// The words read only reason and detail; the escalation's actions are left out.
const escalationWords = (e: Omit<Escalation, "actions">) => wordsFor(e as Escalation);

describe("escalationWords", () => {
  test("a stuck review counts the findings it is stuck on", () => {
    const words = escalationWords({ reason: "stuck", detail: { reason: "stuck", findingIds: ["f1", "f2"] }, at });
    expect(words.short).toBe("Review stuck on 2 findings");
    expect(words.sentence).toContain("2 blocking findings");
  });

  test("a failed phase says its error, and names its Run", () => {
    const words = escalationWords({ reason: "implement_failed", detail: { runId: "run_1", error: "no model is configured\nmore" }, at });
    expect(words).toEqual({
      short: "Implementer failed",
      sentence: "The implementer failed: no model is configured",
      runId: "run_1",
    });
  });

  test("a failed reviewer names its category, and says the round is not complete", () => {
    expect(escalationWords({ reason: "review_failed", detail: { runId: "run_r", category: "frontend", error: "timeout" }, at })).toEqual({
      short: "A frontend reviewer failed",
      sentence: "The frontend reviewer failed: timeout. The round is not complete without it: try it again, or go on without it.",
      runId: "run_r",
    });
  });

  test("no changes, and a reason it has no words for, still read as sentences", () => {
    expect(escalationWords({ reason: "no_changes", detail: { runId: "run_1" }, at }).short).toBe("No changes made");
    expect(escalationWords({ reason: "brand_new", detail: null, at })).toEqual({
      short: "Brand new", sentence: "Delivery stopped: brand new.", runId: null,
    });
  });

  test("used-up loops say how many rounds they had", () => {
    expect(escalationWords({ reason: "pr_loop_exhausted", detail: { iterations: 1 }, at }).sentence).toContain("after 1 fix round,");
    expect(escalationWords({ reason: "exhausted", detail: { iterations: 3 }, at }).sentence).toContain("after 3 review rounds,");
  });

  test("a pull request's conflict, stuck CI and spent budget say which", () => {
    expect(escalationWords({ reason: "pull_request_conflict", detail: { repo: "web", number: 4 }, at }).sentence).toContain("web #4 conflicts");
    expect(escalationWords({ reason: "ci_stuck", detail: { repo: "api", number: 2 }, at }).short).toBe("CI is stuck");
    expect(escalationWords({ reason: "pr_loop_exhausted", detail: { spent: ["web"], total: 5 }, at }).sentence)
      .toBe("The pull request in web has had 5 fixes, the most the organisation allows for one.");
  });

  test("a failed integration says what is true, why in words, the base as a name, and links its Run only", () => {
    const tried = escalationWords({ reason: "pull_request_conflict", at,
      detail: { repo: "web", number: 4, base: "release", integration: true, runId: "run_9", cause: "still_conflicting", error: "still conflicting after integration" } });
    expect(tried.short).toBe("Could not resolve conflicts");
    expect(tried.sentence).toBe("Pull request web #4 still conflicts with release: dude could not resolve it, as it merged the base in, and GitHub still finds a conflict. Try again, or resolve it yourself and wait on it again.");
    expect(tried.parts?.filter((p) => typeof p !== "string")).toEqual([{ mono: "release" }]);
    expect(tried.runId).toBe("run_9");
    const beforePR = escalationWords({ reason: "pull_request_conflict", at,
      detail: { repo: "web", number: 0, base: "main", integration: true, cause: "pair_attempted", error: "this head/base pair was already attempted" } });
    expect(beforePR.sentence).toBe("The branch in web still conflicts with main: dude could not resolve it, as it already tried this branch against this base. Try again, or resolve it yourself and wait on it again.");
    expect(beforePR.runId).toBeNull();
    const failed = escalationWords({ reason: "pull_request_conflict", at,
      detail: { repo: "web", number: 4, base: "main", integration: true, runId: "run_9", cause: "run_failed", error: "lux 0 stream_ended" } });
    expect(failed.sentence).toBe("Pull request web #4 still conflicts with main: dude could not resolve it, as the agent resolving it stopped before it finished. Try again, or resolve it yourself and wait on it again.");
    // An older escalation names no cause: none is made up, and no internal words show.
    const older = escalationWords({ reason: "pull_request_conflict", at,
      detail: { repo: "web", number: 4, base: "main", integration: true, error: "integration fix budget exhausted" } });
    expect(older.sentence).toBe("Pull request web #4 still conflicts with main: dude could not resolve it. Try again, or resolve it yourself and wait on it again.");
    expect([tried, beforePR, failed, older].filter((w) => /`|pair|budget|integration|stream_ended/.test(w.sentence)).length).toBe(0);
  });
});

test("shortError keeps the first line, and cuts a long one", () => {
  expect(shortError("first\nsecond")).toBe("first");
  expect(shortError("x".repeat(10), 5)).toBe("xxxx…");

});

/**
 * Why delivery stopped for a person, in words: each reason the workflow
 * escalates with (delivery.escalate) says what happened, and where to look
 * when it names a Run.
 */

import { describe, expect, test } from "bun:test";
import { escalationWords, shortError } from "../src/escalation.ts";

const at = "2026-01-01T00:00:00Z";

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
      .toBe("The pull request in web has had 5 fixes, the most the organization allows for one.");
  });
});

test("shortError keeps the first line, and cuts a long one", () => {
  expect(shortError("first\nsecond")).toBe("first");
  expect(shortError("x".repeat(10), 5)).toBe("xxxx…");

});

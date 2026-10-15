/**
 * What a pipeline step says woke it, and which fixes the Findings rail
 * counts as the reviewers': a fix sent by the tester is "for the test", one
 * for a pull request's feedback names it, and neither is a review fix. Why
 * a fix ran is first what its run.created says: an adopted pull request is
 * recorded as opened before its first review.
 */

import { describe, expect, test } from "bun:test";
import type { PersistedEvent, Run } from "@dude/domain";
import type { Finding } from "../src/api/client.ts";
import { fixFor, whyItRan } from "../src/screens/TaskScreen.tsx";

const at = (min: number) => new Date(Date.UTC(2026, 9, 3, 10, min)).toISOString();
const run = (id: string, phase: string, min: number, more: Partial<Run> = {}): Run =>
  ({ id, attempt: 1, phase, role: null, kind: "agent", createdAt: at(min), status: "completed", findingIds: [], reviewOf: null, ...more }) as unknown as Run;
const finding = (id: string, runId: string) => ({ id, runId, status: "open", severity: "blocking" }) as Finding;
const event = (eventType: string, min: number, payload: Record<string, unknown> = {}): PersistedEvent => ({
  eventId: `e_${eventType}_${min}`, eventType, occurredAt: at(min), organizationId: "o", projectId: null, taskId: "t", runId: null,
  sessionId: null, workflowRunId: null, actor: { type: "system", id: "workflow" }, source: "control-plane",
  correlationId: null, causationId: null, payload: { number: 41, repo: "web", ...payload }, cursor: min,
}) as PersistedEvent;

const review = run("r_rev", "review", 1);
const reviewFix = run("r_fix1", "fix", 2, { findingIds: ["f_rev"] });
const tested = run("r_test", "test", 3);
const testFix = run("r_fix2", "fix", 4, { findingIds: ["f_rev_open", "f_test"] });
const simplifyReview = run("r_srev", "review", 5, { reviewOf: "simplify" });
const prFix = run("r_fix3", "fix", 12);
const prReview = run("r_prrev", "review", 13, { reviewOf: "pull_request_fix" });
const prTest = run("r_prtest", "test", 14);
const prFixOfReview = run("r_fix4", "fix", 15, { findingIds: ["f_pr"] });
const phases = [review, reviewFix, tested, testFix, simplifyReview, prFix, prReview, prTest, prFixOfReview];
const findings = [finding("f_rev", "r_rev"), finding("f_rev_open", "r_rev"), finding("f_test", "r_test"), finding("f_pr", "r_prrev")];
const prEvents = [event("pull_request.opened", 10), event("pull_request.commented", 11, { author: "ana" })];
const why = (r: Run) => whyItRan(r, phases.indexOf(r), phases, findings, prEvents);

describe("what woke a step", () => {
  test("a fix the tester's findings were sent reads for the test, even beside a reviewer's", () => {
    expect(why(testFix)).toBe("for the test");
    expect(why(reviewFix)).toBe("for the review");
  });

  test("a fix for a pull request's feedback names whose; a fix of its review is the review's", () => {
    expect(why(prFix)).toBe("for ana's review");
    expect(why(prFixOfReview)).toBe("for the review");
  });

  test("a review of a range says which, a test of a pull request's fix says so", () => {
    expect(why(simplifyReview)).toBe("of the simplifier's diff");
    expect(why(prReview)).toBe("of the pull request's fix");
    expect(why(prTest)).toBe("of the pull request's fix");
    expect(why(tested)).toBeUndefined();
  });

  test("the Findings rail's fix runs are the reviewers' only", () => {
    const kinds = phases.filter((r) => r.phase === "fix").map((r) => fixFor(r, phases, findings, prEvents));
    expect(kinds).toEqual(["review", "test", "pull_request", "review"]);
  });
});

const eventAt = (eventType: string, occurredAt: string, payload: Record<string, unknown> = {}): PersistedEvent => ({
  eventId: `e_${eventType}_${occurredAt}`, eventType, occurredAt, organizationId: "o", projectId: null, taskId: "t", runId: null,
  sessionId: null, workflowRunId: null, actor: { type: "system", id: "forge" }, source: "github",
  correlationId: null, causationId: null, payload: { number: 1, repo: "greeter", ...payload }, cursor: 1,
}) as PersistedEvent;

const runAt = (id: string, phase: string, createdAt: string): Run =>
  ({ id, attempt: 1, phase, role: null, kind: "agent", createdAt, status: "completed" }) as unknown as Run;

test("adopted pull request: the internal review's fix is for the review, the pull request loop's for reviewer-acme's", () => {
  // Recorded at adoption, with reviewer-acme's line comment already on it.
  const prEvents = [eventAt("pull_request.opened", "2026-10-15T10:00:00Z"),
    eventAt("pull_request.commented", "2026-10-15T10:00:01Z", { author: "reviewer-acme" })];
  const review = runAt("r1", "review", "2026-10-15T10:00:02Z");
  const first = runAt("r2", "fix", "2026-10-15T10:00:10Z");
  const loop = runAt("r5", "fix", "2026-10-15T10:01:00Z");
  const phases = [review, first, runAt("r3", "review", "2026-10-15T10:00:20Z"), runAt("r4", "simplify", "2026-10-15T10:00:30Z"), loop];
  expect(whyItRan(first, 1, phases, [], prEvents, { phase: "fix", findingIds: ["fnd_1"] })).toBe("for the review");
  expect(whyItRan(loop, 4, phases, [], prEvents, { phase: "fix", prFeedbackCount: 1 })).toBe("for reviewer-acme's review");
});

test("new work: a fix before its pull request is for the review, one after it for the pull request's feedback", () => {
  const prEvents = [eventAt("pull_request.opened", "2026-10-15T10:05:00Z"),
    eventAt("pull_request.reviewed", "2026-10-15T10:06:00Z", { author: "ana" })];
  const first = runAt("r2", "fix", "2026-10-15T10:01:00Z");
  const later = runAt("r6", "fix", "2026-10-15T10:07:00Z");
  const phases = [runAt("r1", "implement", "2026-10-15T10:00:00Z"), first, later];
  expect(whyItRan(first, 1, phases, [], prEvents, { phase: "fix", findingIds: ["fnd_1"] })).toBe("for the review");
  expect(whyItRan(later, 2, phases, [], prEvents, { phase: "fix", prFeedbackCount: 1 })).toBe("for ana's review");
  // A run.created that says neither (or none read): when it ran decides, as before.
  expect(whyItRan(first, 1, phases, [], prEvents)).toBe("for the review");
  expect(whyItRan(later, 2, phases, [], prEvents, { phase: "fix" })).toBe("for ana's review");
});

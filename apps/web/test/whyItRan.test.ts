/**
 * Why a fix ran is what it was started for (its run.created payload): the
 * review's findings, or a pull request's feedback. An adopted pull request
 * is recorded as opened before its first review, so when it was opened
 * says nothing about what woke a fix.
 */

import { expect, test } from "bun:test";
import type { PersistedEvent, Run } from "@dude/domain";
import { whyItRan } from "../src/screens/TaskScreen.tsx";

const event = (eventType: string, occurredAt: string, payload: Record<string, unknown> = {}): PersistedEvent => ({
  eventId: `e_${eventType}_${occurredAt}`, eventType, occurredAt, organizationId: "o", projectId: null, taskId: "t", runId: null,
  sessionId: null, workflowRunId: null, actor: { type: "system", id: "forge" }, source: "github",
  correlationId: null, causationId: null, payload: { number: 1, repo: "greeter", ...payload }, cursor: 1,
}) as PersistedEvent;

const run = (id: string, phase: string, createdAt: string): Run =>
  ({ id, attempt: 1, phase, role: null, kind: "agent", createdAt, status: "completed" }) as unknown as Run;

test("adopted pull request: the internal review's fix is for the review, the pull request loop's for reviewer-acme's", () => {
  // Recorded at adoption, with reviewer-acme's line comment already on it.
  const prEvents = [event("pull_request.opened", "2026-10-15T10:00:00Z"),
    event("pull_request.commented", "2026-10-15T10:00:01Z", { author: "reviewer-acme" })];
  const review = run("r1", "review", "2026-10-15T10:00:02Z");
  const first = run("r2", "fix", "2026-10-15T10:00:10Z");
  const loop = run("r5", "fix", "2026-10-15T10:01:00Z");
  const phases = [review, first, run("r3", "review", "2026-10-15T10:00:20Z"), run("r4", "simplify", "2026-10-15T10:00:30Z"), loop];
  expect(whyItRan(first, 1, phases, [], prEvents, { phase: "fix", findingIds: ["fnd_1"] })).toBe("for the review");
  expect(whyItRan(loop, 4, phases, [], prEvents, { phase: "fix", prFeedbackCount: 1 })).toBe("for reviewer-acme's review");
});

test("new work: a fix before its pull request is for the review, one after it for the pull request's feedback", () => {
  const prEvents = [event("pull_request.opened", "2026-10-15T10:05:00Z"),
    event("pull_request.reviewed", "2026-10-15T10:06:00Z", { author: "ana" })];
  const first = run("r2", "fix", "2026-10-15T10:01:00Z");
  const later = run("r6", "fix", "2026-10-15T10:07:00Z");
  const phases = [run("r1", "implement", "2026-10-15T10:00:00Z"), first, later];
  expect(whyItRan(first, 1, phases, [], prEvents, { phase: "fix", findingIds: ["fnd_1"] })).toBe("for the review");
  expect(whyItRan(later, 2, phases, [], prEvents, { phase: "fix", prFeedbackCount: 1 })).toBe("for ana's review");
  // A run.created that says neither (or none read): when it ran decides, as before.
  expect(whyItRan(first, 1, phases, [], prEvents)).toBe("for the review");
  expect(whyItRan(later, 2, phases, [], prEvents, { phase: "fix" })).toBe("for ana's review");
});

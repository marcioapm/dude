/**
 * Which pull request feedback a fix step is said to answer: the latest
 * comment, review or change of CI verdict before it.
 */

import { expect, test } from "bun:test";
import type { PersistedEvent } from "@dude/domain";
import { prFeedbackReason } from "../src/screens/TaskScreen.tsx";

const event = (eventType: string, occurredAt: string, payload: Record<string, unknown>): PersistedEvent => ({
  eventId: "e", eventType, occurredAt, organizationId: "o", projectId: null, taskId: "t", runId: null,
  sessionId: null, workflowRunId: null, actor: { type: "system", id: "workflow" }, source: "control-plane",
  correlationId: null, causationId: null, payload: { number: 41, repo: "web", ...payload }, cursor: 1,
}) as PersistedEvent;

const fixAt = "2026-09-28T10:10:00Z";
const reviewed = event("pull_request.reviewed", "2026-09-28T10:01:00Z", { author: "cy" });

test("losing or regaining check-run access with the verdict unchanged is not the reason", () => {
  const lost = event("pull_request.checks_changed", "2026-09-28T10:02:00Z", { from: "pending", to: "pending", diagnostic: "check_runs_forbidden" });
  const regained = event("pull_request.checks_changed", "2026-09-28T10:03:00Z", { from: "pending", to: "pending", fromDiagnostic: "check_runs_forbidden" });
  expect(prFeedbackReason([reviewed, lost], fixAt)).toBe("for cy's review");
  expect(prFeedbackReason([reviewed, lost, regained], fixAt)).toBe("for cy's review");
});

test("CI that failed is the reason, with or without a diagnostic beside it", () => {
  const failed = event("pull_request.checks_changed", "2026-09-28T10:02:00Z", { from: "pending", to: "failing" });
  expect(prFeedbackReason([reviewed, failed], fixAt)).toBe("for failing CI");
  const failedUnreadable = event("pull_request.checks_changed", "2026-09-28T10:02:00Z", { from: "pending", to: "failing", diagnostic: "check_runs_forbidden" });
  expect(prFeedbackReason([reviewed, failedUnreadable], fixAt)).toBe("for failing CI");
  // Only what came before the fix counts.
  expect(prFeedbackReason([reviewed, event("pull_request.checks_changed", "2026-09-28T10:20:00Z", { from: "pending", to: "failing" })], fixAt)).toBe("for cy's review");
});

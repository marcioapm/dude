/**
 * A pull request in words: the checks' line, how it stands against its
 * base, why Merge is off, and each `pull_request.*` event as a line of the
 * task's activity.
 */

import { describe, expect, test } from "bun:test";
import type { PersistedEvent, PullRequest } from "@dude/domain";
import { mergeBlockedBy, pullRequestActivity } from "../src/pullRequests.ts";

const pr = (over: Partial<PullRequest> = {}): PullRequest => ({
  id: "pr_1", taskId: "t", runId: null, repositoryId: "r", repositoryName: "web", number: 41, url: "https://github.com/a/web/pull/41",
  headBranch: "dude/t/attempt-1", baseBranch: "main", title: "Chart library", state: "open",
  checks: [], checkState: "passing", review: "approved", reviews: [], mergeable: "clean", behindBy: 0, unresolvedThreads: 0,
  display: "ready", createdAt: "", updatedAt: "", ...over,
});
const check = (name: string, status: string, conclusion: string | null) => ({ name, status, conclusion });

const event = (eventType: string, payload: Record<string, unknown>, actorId = "workflow"): PersistedEvent => ({
  eventId: "e", eventType, occurredAt: "2026-09-28T10:00:00Z", organizationId: "o", projectId: null, taskId: "t", runId: null,
  sessionId: null, workflowRunId: null, actor: { type: actorId === "workflow" ? "system" : "human", id: actorId }, source: "control-plane",
  correlationId: null, causationId: null, payload: { number: 41, repo: "web", ...payload }, cursor: 1,
}) as PersistedEvent;

describe("merging", () => {
  test("Merge is on only when ready, and says why not", () => {
    expect(mergeBlockedBy(pr())).toBeNull();
    expect(mergeBlockedBy(pr({ display: "ci_red", checkState: "failing", review: "changes_requested",
      checks: [check("e2e (chrome)", "completed", "failure")] }))).toBe("Blocked: e2e (chrome) failing, changes requested");
    expect(mergeBlockedBy(pr({ display: "comments", unresolvedThreads: 2 }))).toBe("Blocked: 2 threads unresolved");
    expect(mergeBlockedBy(pr({ state: "draft", display: "ready" }))).toBe("It is a draft");
  });

  test("pending says pending, and unreadable check runs say why rather than running", () => {
    expect(mergeBlockedBy(pr({ display: "ci_running", checkState: "pending" }))).toBe("Blocked: checks pending");
    const denied = { ...check("GitHub check runs", "unavailable", ""), diagnostic: "check_runs_forbidden" };
    const unreadable = mergeBlockedBy(pr({ display: "ci_running", checkState: "pending", checks: [check("CodeRabbit", "completed", "success"), denied] }));
    expect(unreadable).toContain("cannot read GitHub check runs");
    expect(unreadable).toContain("Checks: Read");
    expect(unreadable).not.toContain("running");
    expect(unreadable).not.toContain("pending");
    // A real failure is said first, and the unreadable runs beside it.
    const both = mergeBlockedBy(pr({ display: "ci_red", checkState: "failing", checks: [check("lint", "completed", "failure"), denied] }));
    expect(both).toStartWith("Blocked: lint failing, dude cannot read GitHub check runs");
  });
});

describe("activity", () => {
  test("dude opening it, and whom it asked", () => {
    expect(pullRequestActivity(event("pull_request.opened", { draft: true, reviewersRequested: ["cy", "bo"] }), false)?.text)
      .toBe("opened #41 as a draft, asking cy and bo to review");
    expect(pullRequestActivity(event("pull_request.opened", {}), false)?.byDude).toBe(true);
  });

  test("a person on GitHub, by login, with what they said", () => {
    const line = pullRequestActivity(event("pull_request.commented", { author: "cy", body: "Please keep fetchRevenue.", kind: "changes_requested" }), true);
    expect(line).toEqual({ who: "cy", actorId: null, text: "cy wrote, requesting changes on web#41", quote: "Please keep fetchRevenue." });
    expect(pullRequestActivity(event("pull_request.commented", { author: "stranger", body: "x", kind: "comment", ignored: "not_permitted" }), false)?.text)
      .toBe("stranger commented on #41 — not acted on: they may not wake a fixer");
  });

  test("reviews by person; CI by the check's name", () => {
    expect(pullRequestActivity(event("pull_request.reviewed", { reviews: [{ login: "cy", state: "APPROVED" }] }), false)?.text).toBe("cy approved #41");
    expect(pullRequestActivity(event("pull_request.reviewed", { reviews: [{ login: "cy", state: "CHANGES_REQUESTED" }] }), false)?.text)
      .toBe("cy requested changes on #41");
    expect(pullRequestActivity(event("pull_request.checks_changed", { to: "failing", failing: ["e2e (chrome)"] }), false)?.text)
      .toBe("CI e2e (chrome) failed on #41");
  });

  test("checks pending is not checks started; losing and regaining read access say so", () => {
    expect(pullRequestActivity(event("pull_request.checks_changed", { from: "passing", to: "pending" }), false)?.text).toBe("Checks pending on #41");
    expect(pullRequestActivity(event("pull_request.checks_changed", { from: "pending", to: "pending", diagnostic: "check_runs_forbidden" }), false)?.text)
      .toBe("GitHub check runs on #41 cannot be read: access denied");
    expect(pullRequestActivity(event("pull_request.checks_changed", { from: "pending", to: "pending", fromDiagnostic: "check_runs_forbidden" }), false)?.text)
      .toBe("GitHub check runs on #41 can be read again");
    expect(pullRequestActivity(event("pull_request.checks_changed", { from: "pending", to: "passing", fromDiagnostic: "check_runs_forbidden" }), false)?.text)
      .toBe("GitHub check runs on #41 can be read again; Checks passed on #41");
    // Still unreadable while a real check fails: the failure is the news.
    expect(pullRequestActivity(event("pull_request.checks_changed", { from: "pending", to: "failing", failing: ["lint"],
      fromDiagnostic: "check_runs_forbidden", diagnostic: "check_runs_forbidden" }), false)?.text).toBe("CI lint failed on #41");
  });

  test("a push, a conflict, falling behind, a reopen", () => {
    expect(pullRequestActivity(event("pull_request.pushed", { author: "Gus" }), false)?.text)
      .toBe("Gus pushed to #41 on GitHub — the next fix starts from it");
    expect(pullRequestActivity(event("pull_request.mergeable_changed", { to: "conflicting" }), false)?.text).toBe("#41 conflicts with its base");
    expect(pullRequestActivity(event("pull_request.mergeable_changed", { to: "behind", behindBy: 1 }), false)?.text).toBe("#41 is 1 commit behind its base");
    expect(pullRequestActivity(event("pull_request.mergeable_changed", { from: "unknown", to: "clean" }), false)).toBeNull();
    expect(pullRequestActivity(event("pull_request.updated", { from: "closed", to: "open" }), false)?.text).toBe("#41 was reopened");
    expect(pullRequestActivity(event("pull_request.closed", {}), false)?.text).toBe("#41 was closed without merging");
  });

  test("what a person did from dude names them", () => {
    const line = pullRequestActivity(event("pull_request.action", { action: "merge", method: "squash" }, "key_ana"), false);
    expect(line).toEqual({ who: null, actorId: "key_ana", text: "merged #41 (squash)", quote: undefined });
    expect(pullRequestActivity(event("pull_request.action", { action: "rerun-failed", rerun: 2 }, "key_ana"), false)?.text).toBe("re-ran 2 failed checks on #41");
  });
});

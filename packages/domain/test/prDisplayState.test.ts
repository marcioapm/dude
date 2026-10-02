import { describe, expect, test } from "bun:test";
import { CHECK_RUNS_FORBIDDEN, PR_DISPLAY_STATES, prActualChecks, prCheckDiagnostic, prCheckDiagnosticFix, prCheckDiagnosticReason, prCheckFailed, prCheckSchema, prChecksSummary, prDisplayState, prReviewSummary, type PrCheck, type PrDisplayInput } from "../src/hierarchy.ts";

const open = (over: Partial<PrDisplayInput> = {}): PrDisplayInput => ({ state: "open", checks: "passing", review: "approved", ...over });
const check = (name: string, status: string, conclusion: string | null): PrCheck => ({ name, status, conclusion });

describe("prDisplayState: the priority order", () => {
  // Each case has everything below it true as well, so a state only wins by its rank.
  const everything = { reviews: [{ login: "cy", state: "CHANGES_REQUESTED" }], mergeable: "conflicting", unresolvedThreads: 2 } as const;

  test("merged and closed say it is over, whatever else is true", () => {
    expect(prDisplayState(open({ state: "merged", checks: "failing", ...everything }))).toBe("merged");
    expect(prDisplayState(open({ state: "closed", checks: "failing", ...everything }))).toBe("closed");
  });

  test("red CI outranks running CI, which outranks the review", () => {
    expect(prDisplayState(open({ checks: [check("e2e", "completed", "failure"), check("unit", "in_progress", null)], ...everything }))).toBe("ci_red");
    expect(prDisplayState(open({ checks: [check("e2e", "completed", "success"), check("unit", "queued", null)], ...everything }))).toBe("ci_running");
  });

  test("with green CI: nobody has looked, then changes asked for", () => {
    expect(prDisplayState(open({ review: "pending", mergeable: "conflicting", unresolvedThreads: 1 }))).toBe("awaiting");
    expect(prDisplayState(open({ ...everything }))).toBe("changes");
  });

  test("approved: a conflict, then open threads, then ready", () => {
    expect(prDisplayState(open({ mergeable: "conflicting", unresolvedThreads: 3 }))).toBe("conflict");
    expect(prDisplayState(open({ mergeable: "behind", unresolvedThreads: 3 }))).toBe("comments");
    expect(prDisplayState(open({ mergeable: "behind", unresolvedThreads: 0 }))).toBe("ready");
  });

  test("today's fields alone are enough", () => {
    expect(prDisplayState(open({ checks: "failing" }))).toBe("ci_red");
    expect(prDisplayState(open({ checks: "pending" }))).toBe("ci_running");
    expect(prDisplayState(open({ review: "changes_requested" }))).toBe("changes");
    expect(prDisplayState(open())).toBe("ready");
    // No checks reported is not a reason to wait: the review decides.
    expect(prDisplayState(open({ checks: "unknown", review: "pending" }))).toBe("awaiting");
  });

  test("a draft is judged like an open one", () => {
    expect(prDisplayState(open({ state: "draft", review: "pending" }))).toBe("awaiting");
  });

  test("every state it can return is in the order", () => {
    const seen = new Set([
      prDisplayState(open({ state: "merged" })), prDisplayState(open({ state: "closed" })),
      prDisplayState(open({ checks: "failing" })), prDisplayState(open({ checks: "pending" })),
      prDisplayState(open({ review: "pending" })), prDisplayState(open({ review: "changes_requested" })),
      prDisplayState(open({ mergeable: "conflicting" })), prDisplayState(open({ unresolvedThreads: 1 })), prDisplayState(open()),
    ]);
    expect([...seen].sort()).toEqual([...PR_DISPLAY_STATES].sort());
  });
});

describe("prChecksSummary", () => {
  test("no runs is unknown; skipped and neutral runs pass", () => {
    expect(prChecksSummary([])).toBe("unknown");
    expect(prChecksSummary([check("lint", "completed", "skipped"), check("unit", "completed", "neutral")])).toBe("passing");
  });
  test("a timed-out run failed", () => {
    expect(prChecksSummary([check("e2e", "completed", "timed_out")])).toBe("failing");
    expect(prChecksSummary([check("e2e", "COMPLETED", "FAILURE")])).toBe("failing");
  });
  test("cancelled, waiting on a person or superseded is pending, as the orchestrator reads it", () => {
    for (const c of ["cancelled", "action_required", "stale"]) {
      expect(prChecksSummary([check("e2e", "completed", c)])).toBe("pending");
      expect(prCheckFailed(check("e2e", "completed", c))).toBe(false);
    }
  });
});

describe("prReviewSummary", () => {
  test("each person's latest verdict counts, and a comment is not one", () => {
    const reviews = [
      { login: "cy", state: "CHANGES_REQUESTED", submittedAt: "2026-09-01T10:00:00Z" },
      { login: "cy", state: "APPROVED", submittedAt: "2026-09-01T11:00:00Z" },
      { login: "cy", state: "COMMENTED", submittedAt: "2026-09-01T12:00:00Z" },
    ];
    expect(prReviewSummary({ review: "pending", reviews })).toBe("approved");
  });
  test("one change request outweighs approvals", () => {
    const reviews = [{ login: "bo", state: "APPROVED" }, { login: "cy", state: "CHANGES_REQUESTED" }];
    expect(prReviewSummary({ review: "approved", reviews })).toBe("changes_requested");
  });
  test("without reviews the summary stands", () => {
    expect(prReviewSummary({ review: "approved", reviews: [] })).toBe("approved");
    expect(prReviewSummary({ review: "pending" })).toBe("pending");
  });
});

describe("checks that cannot be read", () => {
  // As the orchestrator stores it beside the checks it could read.
  const denied: PrCheck = { name: "GitHub check runs", status: "unavailable", conclusion: "", url: "", durationMs: 0, diagnostic: CHECK_RUNS_FORBIDDEN };

  test("hold the verdict at pending, never passing or failing, and a real failure still wins", () => {
    expect(prChecksSummary([denied])).toBe("pending");
    expect(prChecksSummary([check("CodeRabbit", "completed", "success"), denied])).toBe("pending");
    expect(prCheckFailed(denied)).toBe(false);
    expect(prDisplayState(open({ checks: [check("CodeRabbit", "completed", "success"), denied] }))).toBe("ci_running");
    expect(prDisplayState(open({ checks: [check("lint", "completed", "failure"), denied] }))).toBe("ci_red");
    expect(prDisplayState(open({ state: "merged", checks: [denied] }))).toBe("merged");
  });

  test("are named apart from the checks", () => {
    const checks = [check("CodeRabbit", "completed", "success"), denied];
    expect(prCheckDiagnostic(checks)).toBe(CHECK_RUNS_FORBIDDEN);
    expect(prCheckDiagnostic([check("CodeRabbit", "completed", "success")])).toBeNull();
    expect(prCheckDiagnostic("pending")).toBeNull();
    expect(prActualChecks(checks).map((c) => c.name)).toEqual(["CodeRabbit"]);
    expect(prCheckDiagnosticReason(CHECK_RUNS_FORBIDDEN)).toBe("GitHub won't show dude this repository's checks");
    expect(prCheckDiagnosticFix(CHECK_RUNS_FORBIDDEN)).toBe(
      "A fine-grained token cannot read check runs: connect a classic token with the repo scope (or a GitHub App once dude supports one), authorized for the organization's SSO and with access to this repository. CI may be running; dude can't see it, so Merge waits.");
    expect(prCheckDiagnosticFix("other")).toBeNull();
    expect(prCheckDiagnosticReason("other")).toBe("Some GitHub checks cannot be read");
  });

  test("survive the API schema", () => {
    const parsed = prCheckSchema.parse(denied);
    expect(parsed.diagnostic).toBe(CHECK_RUNS_FORBIDDEN);
    expect(prCheckSchema.parse(check("unit", "completed", "success")).diagnostic).toBeUndefined();
  });
});

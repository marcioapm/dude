/**
 * Delivery policy: the bounds every loop terminates on.
 *
 * These are pure functions, and they are the only thing standing between a
 * review → fix cycle and an unbounded spend. So the tests worth having are
 * the ones that pin *termination*: that a loop with blocking findings keeps
 * going, that one without stops, and that neither of those depends on a model
 * deciding it is finished.
 */

import { describe, expect, test } from "bun:test";
import {
  DEFAULT_DELIVERY_POLICY,
  isBlocking,
  loopExit,
  matchesGlob,
  reviewersFor,
  type DeliveryPolicy,
  type FindingSeverity,
} from "../src/workflow/policy.ts";

const policy = DEFAULT_DELIVERY_POLICY;

/** A finding with only the fields the loop reads. */
function finding(
  severity: FindingSeverity,
  status = "open",
  fixAttempts = 0,
  id = `f_${Math.random().toString(36).slice(2, 8)}`,
) {
  return { id, severity, status, fixAttempts };
}

describe("what blocks", () => {
  test("only open findings of a blocking severity", () => {
    expect(isBlocking(policy, finding("blocking"))).toBe(true);
    expect(isBlocking(policy, finding("high"))).toBe(true);
    expect(isBlocking(policy, finding("medium"))).toBe(false);
    expect(isBlocking(policy, finding("note"))).toBe(false);
  });

  test("a resolved, superseded or accepted finding never blocks", () => {
    // Each of these means something different — fixed, moot, and a human
    // decided to ship it — and none of them should hold up the work item.
    for (const status of ["resolved", "superseded", "accepted"]) {
      expect(isBlocking(policy, finding("blocking", status))).toBe(false);
    }
  });

  test("policy decides, not severity", () => {
    // The reviewer reports; the policy rules (plan §11.2). A project that
    // treats every finding as blocking gets that by changing the policy, not
    // by changing what reviewers report.
    const strict: DeliveryPolicy = { ...policy, blockingSeverities: ["blocking", "high", "medium", "low", "note"] };
    expect(isBlocking(strict, finding("note"))).toBe(true);

    const lax: DeliveryPolicy = { ...policy, blockingSeverities: [] };
    expect(isBlocking(lax, finding("blocking"))).toBe(false);
  });
});

describe("loop termination", () => {
  test("no blocking findings is a clear exit", () => {
    const exit = loopExit(policy, [finding("medium"), finding("blocking", "resolved")], 1);
    expect(exit).toEqual({ reason: "clear" });
  });

  test("an empty finding set is clear, not stuck", () => {
    // A review that found nothing is the good case, and must not read as a
    // failure to converge.
    expect(loopExit(policy, [], 1)).toEqual({ reason: "clear" });
  });

  test("blocking findings early in the loop mean keep going", () => {
    expect(loopExit(policy, [finding("blocking")], 1)).toBeNull();
  });

  test("the loop stops at its iteration bound rather than spending forever", () => {
    const exit = loopExit(policy, [finding("blocking")], policy.maxReviewIterations);
    expect(exit).toMatchObject({ reason: "exhausted" });
  });

  test("a finding the fixer keeps failing is escalated before the loop's budget", () => {
    /*
     * The per-finding bound is tighter than the loop bound on purpose: a
     * finding that survived two fix attempts is one the fixer misunderstood,
     * and a third attempt usually produces a third variation of the same
     * wrong change.
     */
    const stubborn = finding("blocking", "open", policy.maxAttemptsPerFinding, "f_stuck");
    const fresh = finding("blocking", "open", 0, "f_fresh");

    const exit = loopExit(policy, [stubborn, fresh], 1);
    expect(exit).toEqual({ reason: "stuck", findingIds: ["f_stuck"] });
  });

  test("a stuck finding that is no longer blocking does not stop the loop", () => {
    // Attempts spent on something since resolved should not strand the work
    // item.
    const exit = loopExit(policy, [finding("blocking", "resolved", 9)], 1);
    expect(exit).toEqual({ reason: "clear" });
  });

  test("every loop terminates within its bound", () => {
    /*
     * The property that matters: whatever the findings, iterating cannot
     * exceed maxReviewIterations. Simulated rather than asserted on one case,
     * because "it terminates" is a claim about all inputs.
     */
    let iteration = 0;
    const findings = [finding("blocking")];
    while (loopExit(policy, findings, iteration) === null) {
      iteration += 1;
      expect(iteration).toBeLessThanOrEqual(policy.maxReviewIterations);
    }
    expect(iteration).toBe(policy.maxReviewIterations);
  });
});

describe("reviewer selection", () => {
  test("required reviewers always run", () => {
    expect(reviewersFor(policy, ["README.md"])).toContain("correctness");
  });

  test("a conditional reviewer runs only when the diff warrants it", () => {
    // A security reviewer on a CSS change is a tax on every work item.
    expect(reviewersFor(policy, ["src/styles/app.css"])).not.toContain("security");
    expect(reviewersFor(policy, ["src/auth/session.ts"])).toContain("security");
  });

  test("touching migrations brings the database reviewer", () => {
    expect(reviewersFor(policy, ["migrations/011_pull_requests.sql"])).toContain("database");
  });

  test("one diff can summon several reviewers", () => {
    const selected = reviewersFor(policy, [
      "src/auth/login.tsx",
      "migrations/012_phases.sql",
      "src/api/routes/work.ts",
    ]);
    expect(selected).toEqual(expect.arrayContaining(["correctness", "security", "database", "api", "frontend"]));
  });

  test("the order is stable, so a fan-out is reproducible", () => {
    const paths = ["src/auth/x.ts", "migrations/y.sql"];
    expect(reviewersFor(policy, paths)).toEqual(reviewersFor(policy, [...paths].reverse()));
  });

  test("no duplicates when several rules select the same reviewer", () => {
    const selected = reviewersFor(
      { ...policy, requiredReviewers: ["correctness", "security"] },
      ["src/auth/session.ts"],
    );
    expect(new Set(selected).size).toBe(selected.length);
  });
});

describe("glob matching", () => {
  test("** crosses directories and * does not", () => {
    expect(matchesGlob("**/auth/**", "apps/web/src/auth/login.ts")).toBe(true);
    expect(matchesGlob("**/*.sql", "migrations/001_initial.sql")).toBe(true);
    expect(matchesGlob("src/*.ts", "src/index.ts")).toBe(true);
    // The whole point of the distinction: a single star must not swallow a
    // path separator, or `src/*.ts` would match every file in the tree.
    expect(matchesGlob("src/*.ts", "src/nested/index.ts")).toBe(false);
  });

  test("a leading **/ also matches the root", () => {
    // `**/migrations/**` should catch a migration at the top level, not just
    // a nested one.
    expect(matchesGlob("**/migrations/**", "migrations/001.sql")).toBe(true);
  });

  test("a dot is literal, not a wildcard", () => {
    expect(matchesGlob("**/*.sql", "migrations/001xsql")).toBe(false);
  });
});

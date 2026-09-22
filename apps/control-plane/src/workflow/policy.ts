/**
 * Delivery policy: what blocks, what runs, and when to stop trying.
 *
 * The reviewer reports findings; it does not decide whether the work may
 * progress (plan §11.2). That separation is the point — a reviewer that could
 * also decide would be able to talk itself into shipping, and a policy that
 * lived inside a prompt could not be audited or changed without a new model
 * call.
 *
 * Every loop in the delivery workflow terminates on a bound declared here.
 * None of them terminate on the model deciding it is finished.
 */

import type { AgentRole } from "@dude/domain";

/** What a reviewer looks for. Several run in parallel over the same diff. */
export type ReviewCategory =
  | "correctness"
  | "security"
  | "performance"
  | "frontend"
  | "database"
  | "api";

export type FindingSeverity = "blocking" | "high" | "medium" | "low" | "note";

export interface ReviewerPolicy {
  category: ReviewCategory;
  /**
   * Run this reviewer only when the diff touches a matching path. A security
   * reviewer on a CSS change is a tax on every work item.
   */
  whenPathsMatch?: readonly string[];
}

export interface DeliveryPolicy {
  /** Severities that stop a work item from progressing. */
  blockingSeverities: readonly FindingSeverity[];

  /** Reviewers that always run. */
  requiredReviewers: readonly ReviewCategory[];
  /** Reviewers that run only when the diff warrants them. */
  conditionalReviewers: readonly ReviewerPolicy[];

  /**
   * Review → fix cycles before the work item is escalated to a human.
   *
   * Plan §11.2 suggests 5. The bound exists because a loop that cannot
   * converge will happily spend a budget discovering that.
   */
  maxReviewIterations: number;

  /**
   * Fix attempts one finding may survive before it is escalated on its own.
   *
   * Tighter than the loop bound, and deliberately: a finding the fixer has
   * failed twice is a finding it has misunderstood, and a third attempt
   * usually produces a third variation of the same wrong change.
   */
  maxAttemptsPerFinding: number;

  /** Run the simplifier once blocking findings are clear (plan §11.4). */
  simplify: boolean;

  /** Drive a browser to demonstrate the change (plan §12). */
  test: boolean;

  /**
   * Wake a fixer for PR feedback this many times before escalating.
   *
   * Separate from `maxReviewIterations` because a human reviewer asking for
   * changes is not the same failure as an agent reviewer finding them, and
   * should not consume the same budget.
   */
  maxPrFixIterations: number;
}

export const DEFAULT_DELIVERY_POLICY: DeliveryPolicy = {
  blockingSeverities: ["blocking", "high"],
  requiredReviewers: ["correctness"],
  conditionalReviewers: [
    { category: "security", whenPathsMatch: ["**/auth/**", "**/payments/**", "**/*credential*"] },
    { category: "database", whenPathsMatch: ["**/migrations/**", "**/*.sql"] },
    { category: "frontend", whenPathsMatch: ["**/*.tsx", "**/*.css"] },
    { category: "api", whenPathsMatch: ["**/routes/**", "**/api/**"] },
  ],
  maxReviewIterations: 5,
  maxAttemptsPerFinding: 2,
  simplify: true,
  test: false,
  maxPrFixIterations: 3,
};

/** The agent role that performs each phase. */
export const ROLE_FOR_PHASE: Record<string, AgentRole> = {
  investigate: "investigator",
  implement: "implementer",
  review: "reviewer",
  fix: "implementer",
  simplify: "simplifier",
  test: "qa_browser",
};

/**
 * Phases whose commits are pushed to the work item's branch.
 *
 * A property of the phase rather than a prompt instruction: the reviewer gets
 * a full sandbox and may run the code, write probes and poke at things, but
 * the publish step simply does not run for it, so "the reviewer does not
 * commit" is structural.
 */
export const PHASE_PUBLISHES: Record<string, boolean> = {
  investigate: false,
  implement: true,
  review: false,
  fix: true,
  simplify: true,
  test: false,
};

/**
 * Which reviewers to run over a diff.
 *
 * Required ones always; conditional ones only when a changed path matches.
 * Returned in a stable order so a work item's review fan-out is reproducible.
 */
export function reviewersFor(
  policy: DeliveryPolicy,
  changedPaths: readonly string[],
): ReviewCategory[] {
  const selected = new Set<ReviewCategory>(policy.requiredReviewers);

  for (const reviewer of policy.conditionalReviewers) {
    const patterns = reviewer.whenPathsMatch;
    if (!patterns || patterns.length === 0) {
      selected.add(reviewer.category);
      continue;
    }
    if (changedPaths.some((path) => patterns.some((pattern) => matchesGlob(pattern, path)))) {
      selected.add(reviewer.category);
    }
  }

  const order: ReviewCategory[] = [
    "correctness",
    "security",
    "database",
    "api",
    "frontend",
    "performance",
  ];
  return order.filter((category) => selected.has(category));
}

/**
 * Does this finding stop the work item progressing?
 *
 * Only `open` findings block: a resolved one is fixed, a superseded one
 * describes code that no longer exists, and an accepted one is a human's
 * decision to ship anyway.
 */
export function isBlocking(
  policy: DeliveryPolicy,
  finding: { severity: FindingSeverity; status: string },
): boolean {
  return finding.status === "open" && policy.blockingSeverities.includes(finding.severity);
}

/**
 * Why the loop should stop, or null to keep going.
 *
 * Three exits, and naming them separately matters because they mean different
 * things to a person: "done" needs no attention, "not converging" means the
 * fixer is stuck on something specific, and "gave up" means the whole loop
 * ran out of budget.
 */
export type LoopExit =
  | { reason: "clear" }
  | { reason: "stuck"; findingIds: string[] }
  | { reason: "exhausted"; iterations: number };

export function loopExit(
  policy: DeliveryPolicy,
  findings: ReadonlyArray<{ id: string; severity: FindingSeverity; status: string; fixAttempts: number }>,
  iteration: number,
): LoopExit | null {
  const blocking = findings.filter((f) => isBlocking(policy, f));
  if (blocking.length === 0) return { reason: "clear" };

  // A finding the fixer has failed repeatedly is escalated on its own,
  // before the whole loop's budget is spent on it.
  const stuck = blocking.filter((f) => f.fixAttempts >= policy.maxAttemptsPerFinding);
  if (stuck.length > 0) return { reason: "stuck", findingIds: stuck.map((f) => f.id) };

  if (iteration >= policy.maxReviewIterations) {
    return { reason: "exhausted", iterations: iteration };
  }
  return null;
}

/**
 * Minimal glob matching: `**` crosses directories, `*` does not.
 *
 * Deliberately not a glob library. Path patterns here come from policy an
 * operator writes, the vocabulary is small, and a dependency whose semantics
 * differ from these three lines would be harder to reason about than the
 * lines themselves.
 */
export function matchesGlob(pattern: string, path: string): boolean {
  const escaped = pattern.replace(/[.+^${}()|[\]\\]/g, "\\$&");
  const regex = escaped
    // A placeholder first, so the single-star rule cannot rewrite half of a
    // double star.
    .replace(/\*\*\//g, "\u0000")
    .replace(/\*\*/g, "\u0001")
    .replace(/\*/g, "[^/]*")
    .replace(/\u0000/g, "(?:.*/)?")
    .replace(/\u0001/g, ".*");
  return new RegExp(`^${regex}$`).test(path);
}

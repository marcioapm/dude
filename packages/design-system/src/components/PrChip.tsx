import type { HTMLAttributes, ReactNode } from "react";
import { prActualChecks, prCheckDiagnostic, prCheckDiagnosticReason, prCheckFailed, prChecksSummary, prDisplayState, prReviewSummary, type PrDisplayInput, type PrDisplayState } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { Tooltip } from "../primitives/Tooltip.tsx";
import styles from "./PrChip.module.css";

/** A pull request as the chip reads it: the display state's inputs, where it is, and what it is called. */
export interface PrChipPullRequest extends PrDisplayInput {
  readonly number: number;
  readonly url: string;
  /** "acme/dashboard", or the repository's name; in the tooltip. */
  readonly repositoryName?: string | undefined;
  readonly baseBranch?: string | undefined;
  readonly behindBy?: number | null | undefined;
  /** The state, when the API computed it (`display`); otherwise it is computed here. */
  readonly display?: PrDisplayState | undefined;
}

export interface PrDisplaySpec {
  readonly label: string;
  readonly glyph: IconName;
  /** What it means, for the rules sheet and a tooltip's first line. */
  readonly description: string;
}

/**
 * Every state a pull request is shown as, by priority: an icon and a word
 * (never hue alone), and what it means. `merged` wears the one violet;
 * the rest take tones.
 */
export const PR_DISPLAY_SPECS: Record<PrDisplayState, PrDisplaySpec> = {
  merged: { label: "Merged", glyph: "merge", description: "The pull request was merged. The task is done." },
  closed: { label: "Closed", glyph: "stop", description: "Closed without merging." },
  ci_red: { label: "CI failing", glyph: "circle-x", description: "A check failed. dude wakes a fixer with the failing check's log." },
  ci_running: { label: "CI pending", glyph: "circle-dotted", description: "No CI verdict on the latest commit yet." },
  awaiting: { label: "Awaiting approval", glyph: "eye", description: "Checks pass; nobody has approved or asked for changes yet." },
  changes: { label: "Changes requested", glyph: "file-diff", description: "A reviewer asked for changes. dude wakes a fixer with their comments." },
  conflict: { label: "Conflicts", glyph: "git-conflict", description: "The branch conflicts with its base. Update the branch or resolve it by hand." },
  comments: { label: "Unresolved comments", glyph: "comments", description: "Approved, but open review threads stand in the way of the merge." },
  ready: { label: "Ready to merge", glyph: "circle-check", description: "Approved, checks green, mergeable. Merge it here or on GitHub." },
};

/** The state to show: the API's, when it sent one, else computed from what the PR says. */
export function prStateOf(pr: PrChipPullRequest): PrDisplayState {
  return pr.display ?? prDisplayState(pr);
}

/**
 * How a state is shown for this pull request: pending because its checks
 * cannot be read is said so, not left to read as CI at work. The state
 * itself stays `ci_running`, which is what older clients know.
 */
export function prSpecOf(pr: PrChipPullRequest, state: PrDisplayState = prStateOf(pr)): PrDisplaySpec {
  const diagnostic = prCheckDiagnostic(pr.checks);
  if (state === "ci_running" && diagnostic) return { label: "CI unavailable", glyph: "warning", description: prCheckDiagnosticReason(diagnostic) };
  return PR_DISPLAY_SPECS[state];
}

/**
 * Everything else true of a pull request, one line each, for the chip's
 * tooltip: checks, review, how it stands against its base, open threads.
 * Only what is known — a field the forge has not reported says nothing.
 */
export function prFacts(pr: PrChipPullRequest): string[] {
  const out: string[] = [];
  if (pr.state === "merged") out.push("Merged");
  if (pr.state === "closed") out.push("Closed without merging");
  if (typeof pr.checks !== "string") {
    const checks = prActualChecks(pr.checks);
    const diagnostic = prCheckDiagnostic(pr.checks);
    const failing = checks.filter(prCheckFailed);
    const done = checks.filter((c) => c.status.toLowerCase() === "completed").length;
    if (failing.length > 0) out.push(`Checks: ${failing.map((c) => c.name).join(", ")} failing`);
    else if (done < checks.length) out.push(`Checks: running (${done} of ${checks.length} done)`);
    else if (checks.length > 0) out.push(diagnostic ? `Checks: ${checks.length} readable passing` : `Checks: all ${checks.length} passing`);
    if (diagnostic) out.push(prCheckDiagnosticReason(diagnostic));
  } else {
    const words = { failing: "Checks: failing", pending: "Checks: pending", passing: "Checks: passing", unknown: null }[prChecksSummary(pr.checks)];
    if (words) out.push(words);
  }
  const review = prReviewSummary(pr);
  const by = (state: string) => (pr.reviews ?? []).filter((r) => r.state.toUpperCase() === state).map((r) => r.login);
  if (review === "approved") out.push(by("APPROVED").length ? `Approved by ${[...new Set(by("APPROVED"))].join(", ")}` : "Approved");
  else if (review === "changes_requested") out.push(by("CHANGES_REQUESTED").length ? `Changes requested by ${[...new Set(by("CHANGES_REQUESTED"))].join(", ")}` : "Changes requested");
  else if (pr.state === "open" || pr.state === "draft") out.push("No review yet");
  const base = pr.baseBranch ?? "its base";
  if (pr.mergeable === "conflicting") out.push(`Conflicts with ${base}`);
  else if (pr.behindBy) out.push(`${pr.behindBy} commit${pr.behindBy === 1 ? "" : "s"} behind ${base}, no conflicts`);
  else if (pr.mergeable === "clean") out.push(`Up to date with ${base}`);
  if (pr.unresolvedThreads) out.push(`${pr.unresolvedThreads} unresolved comment${pr.unresolvedThreads === 1 ? "" : "s"}`);
  return out;
}

export interface PrChipProps extends Omit<HTMLAttributes<HTMLAnchorElement>, "children"> {
  readonly pr: PrChipPullRequest;
  /** Show "#41" after the word. On by default; off where the number is said beside it. */
  readonly showNumber?: boolean | undefined;
  /** The glyph alone (a tree row): the word moves to the tooltip and the accessible name. */
  readonly iconOnly?: boolean | undefined;
  readonly size?: "sm" | "md" | undefined;
}

/**
 * A pull request's one state, picked by priority: an icon, a word and its
 * number, as a link to GitHub. The tooltip lists everything else that is
 * true of it. A task with two pull requests shows two chips.
 */
export function PrChip({ pr, showNumber = true, iconOnly, size = "md", className, ...rest }: PrChipProps) {
  const state = prStateOf(pr);
  const spec = prSpecOf(pr, state);
  const where = pr.repositoryName ? `${pr.repositoryName}#${pr.number}` : `#${pr.number}`;
  const tip: ReactNode = (
    <span className={styles["tip"]}>
      <span className={styles["tipHead"]}>
        {where} · {spec.label}
      </span>
      {prFacts(pr).map((f) => (
        <span key={f} className={styles["tipFact"]}>
          {f}
        </span>
      ))}
      <span className={styles["tipFoot"]}>Opens on GitHub</span>
    </span>
  );
  return (
    <Tooltip content={tip}>
      <a
        className={cx(styles["root"], styles[state], size === "sm" && styles["sm"], iconOnly && styles["iconOnly"], className)}
        href={pr.url}
        target="_blank"
        rel="noreferrer"
        data-pr-state={state}
        aria-label={`${spec.label}, pull request ${where} (opens on GitHub)`}
        {...rest}
      >
        <Icon name={spec.glyph} size={size === "sm" ? 12 : 13} strokeWidth={1.75} className={styles["glyph"]} />
        {iconOnly ? null : <span className={cx(styles["word"], "ds-cap")}>{spec.label}</span>}
        {showNumber && !iconOnly ? <span className={cx(styles["number"], "ds-cap")}>#{pr.number}</span> : null}
      </a>
    </Tooltip>
  );
}

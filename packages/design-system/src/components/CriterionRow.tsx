import { useId, type HTMLAttributes, type ReactNode } from "react";
import type { VerdictStatus } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { Badge } from "../primitives/Badge.tsx";
import type { ToneName } from "../tokens/palette.ts";
import styles from "./CriterionRow.module.css";

// ---------------------------------------------------------------------------
// Vocabulary. A verdict is the tester's status checked by dude, or a
// person's decision on it; keyed on the domain union plus the two
// decisions, so a new status is a compile error here first.
// ---------------------------------------------------------------------------

export type CriterionState = VerdictStatus | "waived" | "accepted";

export interface CriterionStateSpec {
  readonly label: string;
  readonly tone: ToneName;
  readonly glyph: IconName;
  readonly description: string;
}

export const CRITERION_STATES = ["pass", "fail", "blocked", "unverified", "not-applicable", "waived", "accepted"] as const satisfies ReadonlyArray<CriterionState>;

export const CRITERION_STATE_SPECS: Record<CriterionState, CriterionStateSpec> = {
  pass: { label: "Pass", tone: "success", glyph: "circle-check", description: "Shown working, with evidence a person can open." },
  fail: { label: "Fail", tone: "danger", glyph: "circle-x", description: "The tester saw it fail." },
  blocked: { label: "Blocked", tone: "attention", glyph: "lock", description: "Nobody could check it: never a pass." },
  unverified: { label: "Unverified", tone: "attention", glyph: "warning", description: "Called a pass, but its evidence is not a file the test published." },
  "not-applicable": { label: "Not applicable", tone: "neutral", glyph: "minus", description: "Does not apply to this change, with the tester's reason." },
  waived: { label: "Waived", tone: "neutral", glyph: "hand", description: "Could not be checked; a person let it ship, with a reason." },
  accepted: { label: "Accepted", tone: "neutral", glyph: "hand", description: "Failed; a person let it ship, with a reason." },
};

/** What a criterion reads as: a person's decision over the verdict it settles. */
export function criterionState(status: VerdictStatus, decision: "waived" | "accepted" | null | undefined): CriterionState {
  return decision ?? status;
}

// ---------------------------------------------------------------------------
// Row
// ---------------------------------------------------------------------------

export interface CriterionRowProps extends Omit<HTMLAttributes<HTMLLIElement>, "title"> {
  /** 1-based, as the task lists it. */
  readonly number: number;
  readonly text: string;
  readonly status: VerdictStatus;
  readonly decision?: "waived" | "accepted" | null | undefined;
  /** Who decided, and why. */
  readonly decidedBy?: string | null | undefined;
  readonly decisionNote?: string | null | undefined;
  /** One line: what the tester saw. */
  readonly observed?: string | undefined;
  /** Why dude's status differs from the tester's. */
  readonly problem?: string | undefined;
  /** The evidence, as the app links it (each opens the file). */
  readonly evidence?: ReactNode;
}

/**
 * One acceptance criterion as the tester left it: the verdict as glyph +
 * word in its tone (a person's waiver or acceptance shown over it, with
 * the verdict it settled still named), the criterion, the evidence, and
 * under it what was seen and why dude disagreed or a person decided.
 */
export function CriterionRow({ number, text, status, decision, decidedBy, decisionNote, observed, problem, evidence, className, ...rest }: CriterionRowProps) {
  const state = criterionState(status, decision);
  const spec = CRITERION_STATE_SPECS[state];
  return (
    <li className={cx(styles["root"], styles[spec.tone], className)} data-status={status} data-state={state} {...rest}>
      <div className={styles["row"]}>
        <span className={styles["state"]} title={spec.description}>
          <Icon name={spec.glyph} size={12} />
          <span>{spec.label}</span>
        </span>
        <span className={styles["number"]}>#{number}</span>
        <span className={styles["text"]}>{text}</span>
        {decision ? (
          <Badge tone="neutral" emphasis="subtle" size="sm" title={CRITERION_STATE_SPECS[status].description}>
            was {CRITERION_STATE_SPECS[status].label.toLowerCase()}
          </Badge>
        ) : null}
      </div>
      {observed || problem || evidence || decision ? (
        <div className={styles["body"]}>
          {evidence ? <div className={styles["evidence"]}>{evidence}</div> : null}
          {observed ? <div className={styles["observed"]}>{observed}</div> : null}
          {problem ? <div className={styles["problem"]}>{problem}</div> : null}
          {decision ? (
            <div className={styles["decision"]} data-testid="criterion-decision">
              {spec.label} by {decidedBy || "a person"}{decisionNote ? `: ${decisionNote}` : ""}
            </div>
          ) : null}
        </div>
      ) : null}
    </li>
  );
}

// ---------------------------------------------------------------------------
// Group
// ---------------------------------------------------------------------------

export interface VerificationGroupProps extends Omit<HTMLAttributes<HTMLElement>, "title"> {
  /** What was tested: repository → commit. Shown short. */
  readonly heads: Readonly<Record<string, string>>;
  /** The work has moved past the tested commits. */
  readonly stale?: boolean | undefined;
  /** Section title. Default "Verification". */
  readonly title?: ReactNode;
  /** Right side of the header: an earlier test, a link to the Run. */
  readonly actions?: ReactNode;
  readonly children: ReactNode;
}

/**
 * A test's verdict, criterion by criterion (`CriterionRow`s), under the
 * commits it covers — and, once the branch has moved past them, a stale
 * mark: what was shown working is not what the branch holds now.
 */
export function VerificationGroup({ heads, stale, title = "Verification", actions, children, className, ...rest }: VerificationGroupProps) {
  const headingId = useId();
  const repos = Object.keys(heads).sort();
  return (
    <section className={cx(styles["group"], className)} aria-labelledby={headingId} {...rest}>
      <header className={styles["groupHead"]}>
        <span id={headingId} className={cx("ds-label", styles["groupTitle"])}>
          {title}
        </span>
        <span className={styles["heads"]} data-testid="verification-heads">
          {repos.length > 0 ? "tested at " : null}
          {repos.map((r, i) => (
            <span key={r}>
              {i > 0 ? ", " : null}
              {repos.length > 1 ? `${r} ` : null}
              <code className={styles["sha"]} title={heads[r]}>{heads[r]!.slice(0, 7)}</code>
            </span>
          ))}
        </span>
        {stale ? (
          <Badge tone="attention" emphasis="tinted" size="sm" icon="warning" data-testid="verification-stale"
            title="The branch has changed since this test: its verdict is about the commits it names.">
            Stale
          </Badge>
        ) : null}
        {actions ? <span className={styles["groupActions"]}>{actions}</span> : null}
      </header>
      <ul className={styles["list"]}>{children}</ul>
    </section>
  );
}

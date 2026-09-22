import type { HTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import { COUNTED_TRIAGE_KINDS, TRIAGE_SPECS, type TriageCounts, type TriageKind } from "../tokens/triage.ts";
import { StatusBadge } from "./StatusBadge.tsx";
import styles from "./TriageRollup.module.css";

export interface TriageRollupProps extends Omit<HTMLAttributes<HTMLSpanElement>, "children"> {
  readonly counts: TriageCounts;
  /** Show only these buckets (default: every counted bucket that is > 0). */
  readonly only?: ReadonlyArray<TriageKind> | undefined;
  /** Spell the buckets out ("2 need you") instead of mark + number. */
  readonly verbose?: boolean | undefined;
}

/**
 * What is inside a collapsed parent, in one glance: a mark and a count per
 * non-empty bucket, most urgent first. The marks are the `StatusBadge` dot
 * shapes — diamond for needs-you, round for active, and so on — so the
 * roll-up never invents a second vocabulary, and the needs-you diamond
 * keeps its ring. Zero buckets are omitted: a quiet project shows nothing.
 */
export function TriageRollup({ counts, only, verbose, className, ...rest }: TriageRollupProps) {
  const kinds = (only ?? COUNTED_TRIAGE_KINDS).filter((k) => counts[k] > 0);
  if (kinds.length === 0) return null;
  const summary = kinds.map((k) => TRIAGE_SPECS[k].countLabel(counts[k])).join(", ");
  return (
    <span className={cx(styles["root"], className)} title={summary} aria-label={summary} {...rest}>
      {kinds.map((k) => {
        const spec = TRIAGE_SPECS[k];
        return (
          <StatusBadge
            key={k}
            status={spec.status}
            variant="dot"
            label={verbose ? spec.countLabel(counts[k]) : String(counts[k])}
            className={cx(styles["item"], styles[k])}
            data-triage={k}
            aria-hidden
          />
        );
      })}
    </span>
  );
}

import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import styles from "./TaskHistory.module.css";

export interface TaskHistoryProps extends Omit<HTMLAttributes<HTMLDivElement>, "children"> {
  /** How the work went, in a few words: "Delivered automatically", "Not started". */
  readonly lead: string;
  /** What ran, in order, each already folded ("reviewers ×3"): drawn with arrows between. */
  readonly steps?: readonly string[] | undefined;
  /** What it came to, each a short fact ("5 findings, all fixed", the cost): after the steps. */
  readonly facts?: readonly ReactNode[] | undefined;
  readonly icon?: IconName | undefined;
  /** One quiet action at the end: the way to the whole pipeline. */
  readonly action?: ReactNode;
}

/**
 * A task's history so far, in one line: how it went, what ran, what it
 * came to — "Delivered automatically · implementer → reviewers ×3 → fixer
 * → PR #88 · 5 findings, all fixed · $9.80". It heads a task's Chat, above
 * the conversation, on the raised shade; the steps' arrows are muted so
 * the words read first. Wraps rather than cuts: every step is named.
 */
export function TaskHistory({ lead, steps = [], facts = [], icon = "zap", action, className, ...rest }: TaskHistoryProps) {
  return (
    <div className={cx(styles["root"], className)} {...rest}>
      <Icon name={icon} size={14} className={styles["icon"]} />
      <p className={styles["line"]}>
        <span className={styles["lead"]}>{lead}</span>
        {steps.length > 0 ? (
          <>
            <span className={styles["dot"]} aria-hidden> · </span>
            <span className={styles["steps"]}>
              {steps.map((s, i) => (
                <span key={i} className={styles["step"]}>
                  {i > 0 ? <span className={styles["arrow"]} aria-label="then"> → </span> : null}
                  {s}
                </span>
              ))}
            </span>
          </>
        ) : null}
        {facts.map((f, i) => (
          <span key={i} className={styles["fact"]}>
            <span className={styles["dot"]} aria-hidden> · </span>
            {f}
          </span>
        ))}
      </p>
      {action ? <span className={styles["action"]}>{action}</span> : null}
    </div>
  );
}

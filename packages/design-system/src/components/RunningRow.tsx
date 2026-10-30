import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import styles from "./RunningRow.module.css";

export interface RunningRowProps extends Omit<HTMLAttributes<HTMLLIElement>, "title"> {
  /** Who it is: an `AgentAvatar` with `live`. */
  readonly avatar: ReactNode;
  /** "Correctness review", "Implement". Its accessible name with `detail`. */
  readonly label: ReactNode;
  /** After the label, muted: the model and the status ("claude-opus-5-5 · Running"). */
  readonly detail?: ReactNode;
  /**
   * What it is doing, as recorded: its plan's current step or its latest
   * tool call. Never a summary made up for the row; omit when none is recorded.
   */
  readonly activity?: ReactNode;
  /** What it has cost so far: a `Cost`. Omit when not reported, never $0.00. */
  readonly cost?: ReactNode;
  /** How long it has run: a live `Duration`. */
  readonly elapsed?: ReactNode;
  /** The way to it: "Open session". */
  readonly action?: ReactNode;
}

/**
 * One agent at work right now, in a list of them: its face, who it is and
 * on what model, what it is doing as its own record says, what it has
 * cost and for how long, and the way to its session. Rows are told apart by
 * space, not frames. On a narrow pane the figures and the action wrap
 * under the words, aligned with them, never off the side.
 */
export function RunningRow({ avatar, label, detail, activity, cost, elapsed, action, className, ...rest }: RunningRowProps) {
  return (
    <li className={cx(styles["row"], className)} {...rest}>
      <span className={styles["avatar"]}>{avatar}</span>
      <span className={styles["what"]}>
        <span className={styles["head"]}>
          <span className={styles["label"]}>{label}</span>
          {detail ? <span className={styles["detail"]}>{detail}</span> : null}
        </span>
        {activity ? <span className={styles["activity"]}>{activity}</span> : null}
      </span>
      {cost || elapsed || action ? (
        <span className={styles["end"]}>
          {cost ? <span className={styles["figure"]}>{cost}</span> : null}
          {elapsed ? <span className={cx(styles["figure"], styles["elapsed"])}>{elapsed}</span> : null}
          {action ? <span className={styles["action"]}>{action}</span> : null}
        </span>
      ) : null}
    </li>
  );
}

/** The list a page's `RunningRow`s sit in, named for assistive technology ("Running now"). */
export function RunningList({ className, children, ...rest }: HTMLAttributes<HTMLUListElement> & { readonly "aria-label": string }) {
  return (
    <ul className={cx(styles["list"], className)} {...rest}>
      {children}
    </ul>
  );
}

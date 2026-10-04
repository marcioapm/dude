import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import styles from "./DeciderLine.module.css";

export interface DeciderLineProps extends Omit<HTMLAttributes<HTMLDivElement>, "children"> {
  /** Who takes the task's decisions: Deliver's rules, or its conductor. */
  readonly decider: "policy" | "conductor";
  /** What the delivery waits on now, in a person's words: the conductor, or (Deliver deciding) the person. */
  readonly waiting?: string | undefined;
  /** One quiet action: hand the decisions back, or to the conductor. */
  readonly action?: ReactNode;
}

/**
 * Who decides a task's delivery, in one line above its Chat's composer:
 * "The conductor decides · waiting on it: whether to open the pull
 * request", with the way to hand the decisions back. The conductor's face
 * and colour when it is the conductor; Deliver's bolt when it is the
 * pipeline's fixed rules.
 */
export function DeciderLine({ decider, waiting, action, className, ...rest }: DeciderLineProps) {
  const conductor = decider === "conductor";
  let detail: ReactNode;
  if (waiting) {
    detail = <>waiting on {conductor ? "it" : "the person"}: {waiting}</>;
  } else if (conductor) {
    detail = "each step that finishes comes back to it";
  } else {
    detail = "the pipeline runs to the pull request on its own";
  }
  return (
    <div className={cx(styles["root"], conductor && styles["conductor"], className)} data-decider={decider} {...rest}>
      <Icon name={conductor ? "conductor" : "zap"} size={14} className={styles["icon"]} />
      <p className={styles["line"]}>
        <span className={styles["who"]}>{conductor ? "The conductor decides" : "Deliver decides"}</span>
        <span className={styles["detail"]}>
          {" · "}
          {detail}
        </span>
      </p>
      {action ? <span className={styles["action"]}>{action}</span> : null}
    </div>
  );
}

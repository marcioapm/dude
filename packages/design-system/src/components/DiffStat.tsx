import type { HTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import styles from "./DiffStat.module.css";

export interface DiffStatProps extends Omit<HTMLAttributes<HTMLSpanElement>, "children"> {
  readonly additions: number;
  readonly deletions: number;
}

/** "+12 −3": lines added and removed, in the diff's colours, as tabular figures. */
export function DiffStat({ additions, deletions, className, ...rest }: DiffStatProps) {
  return (
    <span className={cx(styles["stat"], className)} {...rest}>
      <span className={styles["add"]}>+{additions}</span> <span className={styles["del"]}>−{deletions}</span>
    </span>
  );
}

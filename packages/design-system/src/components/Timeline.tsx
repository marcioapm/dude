import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import styles from "./Timeline.module.css";

export interface TimelineProps extends HTMLAttributes<HTMLOListElement> {
  readonly children?: ReactNode;
}

/** What happened to something, newest first: who, what, when. No frames, no rules. */
export function Timeline({ className, children, ...rest }: TimelineProps) {
  return (
    <ol className={cx(styles["list"], className)} {...rest}>
      {children}
    </ol>
  );
}

export interface TimelineItemProps extends Omit<HTMLAttributes<HTMLLIElement>, "title"> {
  /** Who: a face, or an agent's tile. */
  readonly who: ReactNode;
  /** What happened, as a sentence with the actor's name in it (bold). */
  readonly children: ReactNode;
  /** Something they said, quoted under the sentence. */
  readonly quote?: ReactNode;
  /** When, as an age ("4m") or a time. */
  readonly when?: ReactNode;
}

export function TimelineItem({ who, children, quote, when, className, ...rest }: TimelineItemProps) {
  return (
    <li className={cx(styles["item"], className)} {...rest}>
      <span className={styles["who"]}>{who}</span>
      <div className={styles["body"]}>
        <div className={styles["what"]}>{children}</div>
        {quote ? <blockquote className={styles["quote"]}>{quote}</blockquote> : null}
      </div>
      <span className={styles["when"]}>{when}</span>
    </li>
  );
}

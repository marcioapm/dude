import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import styles from "./SessionList.module.css";

export interface SessionListProps extends HTMLAttributes<HTMLUListElement> {
  readonly children?: ReactNode;
}

/** A task's sessions, newest first, on the chrome shade beside the one that is open. */
export function SessionList({ className, children, ...rest }: SessionListProps) {
  return (
    <ul className={cx(styles["list"], className)} {...rest}>
      {children}
    </ul>
  );
}

export interface SessionItemProps extends Omit<HTMLAttributes<HTMLLIElement>, "title" | "onClick"> {
  /** The agent's tile. */
  readonly avatar: ReactNode;
  /** "Fix · Cy's review". */
  readonly title: ReactNode;
  /** "claude-sonnet-5 · 2m 31s · $0.10". */
  readonly detail?: ReactNode;
  /** At the end: a live status mark. */
  readonly trailing?: ReactNode;
  readonly current?: boolean | undefined;
  readonly onOpen: () => void;
}

export function SessionItem({ avatar, title, detail, trailing, current, onOpen, className, children, ...rest }: SessionItemProps) {
  return (
    <li className={className} {...rest}>
      <button type="button" className={cx(styles["item"], current && styles["current"])} aria-current={current ? "true" : undefined} onClick={onOpen}>
        <span className={styles["avatar"]}>{avatar}</span>
        <span className={styles["text"]}>
          <span className={styles["title"]}>{title}</span>
          {detail ? <span className={styles["detail"]}>{detail}</span> : null}
        </span>
        {trailing ? <span className={styles["trailing"]}>{trailing}</span> : null}
      </button>
      {children}
    </li>
  );
}

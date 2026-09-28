import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import styles from "./WaitingList.module.css";

export interface WaitingGroupProps extends Omit<HTMLAttributes<HTMLElement>, "title"> {
  /** "Yours", "Waiting on others". */
  readonly title: string;
  readonly count: number;
  readonly children?: ReactNode;
  /** Said instead of the list when it is empty. */
  readonly empty?: ReactNode;
}

/** A group of things waiting on a person, under a small-caps label and its count. */
export function WaitingGroup({ title, count, empty, className, children, ...rest }: WaitingGroupProps) {
  return (
    <section className={cx(styles["group"], className)} aria-label={title} {...rest}>
      <h2 className={cx(styles["label"], "ds-label")}>
        {title} · {count}
      </h2>
      {count === 0 && empty ? <p className={styles["empty"]}>{empty}</p> : <ul className={styles["list"]}>{children}</ul>}
    </section>
  );
}

export interface WaitingRowProps extends Omit<HTMLAttributes<HTMLLIElement>, "title"> {
  /** Whose it is: a face, with the asking agent on it. */
  readonly face: ReactNode;
  /** What is asked, in the asker's words. */
  readonly ask: ReactNode;
  /** Where and whose: project face, key, title, "Bo's task". */
  readonly where: ReactNode;
  /** The row's tooltip: the project and epic. */
  readonly whereTitle?: string | undefined;
  /** How long it has waited. */
  readonly age?: ReactNode;
  /** What to do: Answer (yours, primary) or Take over (others', quiet). */
  readonly action?: ReactNode;
  /** It is yours: the one highlighted kind, the attention tint and its bar. */
  readonly mine?: boolean | undefined;
  readonly onOpen: () => void;
}

/**
 * One thing waiting on a person: whose face, what is asked and where, how
 * long, and what to do. Yours take the attention tint and its bar;
 * others' are plain rows. The ask opens where it is answered.
 */
export function WaitingRow({ face, ask, where, whereTitle, age, action, mine, onOpen, className, ...rest }: WaitingRowProps) {
  return (
    <li className={cx(styles["row"], mine && styles["mine"], className)} data-mine={mine ? "true" : undefined} {...rest}>
      <span className={styles["face"]}>{face}</span>
      <button type="button" className={styles["open"]} onClick={onOpen} title={whereTitle}>
        <span className={styles["ask"]}>{ask}</span>
        <span className={styles["where"]}>{where}</span>
      </button>
      <span className={styles["age"]}>{age}</span>
      <span className={styles["action"]}>{action}</span>
    </li>
  );
}

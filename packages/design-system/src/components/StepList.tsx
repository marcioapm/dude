import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import styles from "./StepList.module.css";

export interface StepListProps extends HTMLAttributes<HTMLOListElement> {
  readonly children?: ReactNode;
}

/**
 * Steps in order — a delivery's phases, then its pull requests — as rows
 * on the page, told apart by space, not frames. Each row opens something:
 * the step's conversation, or a page elsewhere. The running step is told
 * by its status and its live avatar.
 */
export function StepList({ className, children, ...rest }: StepListProps) {
  return (
    <ol className={cx(styles["list"], className)} {...rest}>
      {children}
    </ol>
  );
}

interface StepRowBase extends Omit<HTMLAttributes<HTMLLIElement>, "title" | "onClick"> {
  /** Its place ("1", "PR"). Optional: the avatar usually says enough. */
  readonly step?: ReactNode;
  /** Who does it: an AgentAvatar. */
  readonly avatar?: ReactNode;
  readonly label: ReactNode;
  /** After the label, muted: why it ran, what it found ("for the review", "2 blocking"). */
  readonly note?: ReactNode;
  /** Its status: a StatusBadge or StatusMark. */
  readonly status?: ReactNode;
  /** Muted mono: a commit, a branch. */
  readonly meta?: ReactNode;
  /** The meta's full text, when it is shortened. */
  readonly metaTitle?: string | undefined;
  /** How long it took, or has been running. */
  readonly duration?: ReactNode;
  /** A second line under the label: the running step's plan. */
  readonly below?: ReactNode;
}

export type StepRowProps = StepRowBase &
  (
    | { readonly onOpen: () => void; readonly href?: undefined }
    /** Opens elsewhere, in a new tab. */
    | { readonly href: string; readonly onOpen?: undefined }
  );

/** One step: a button that opens it, or a link out. */
export function StepRow({ step, avatar, label, note, status, meta, metaTitle, duration, below, onOpen, href, className, ...rest }: StepRowProps) {
  const inner = (
    <>
      {step !== undefined ? <span className={cx(styles["step"], "ds-mono")}>{step}</span> : null}
      <span className={styles["avatar"]}>{avatar}</span>
      <span className={styles["what"]}>
        <span className={styles["label"]}>{label}</span>
        {note ? <span className={styles["note"]}>{note}</span> : null}
      </span>
      <span className={styles["status"]}>{status}</span>
      <span className={cx(styles["meta"], "ds-mono")} title={metaTitle}>
        {meta}
      </span>
      <span className={styles["duration"]}>{duration}</span>
      {href ? <Icon name="external" size={14} className={styles["open"]} /> : null}
      {below ? <span className={styles["below"]}>{below}</span> : null}
    </>
  );
  return (
    <li className={cx(styles["row"], className)} {...rest}>
      {href ? (
        <a className={styles["target"]} href={href} target="_blank" rel="noreferrer">
          {inner}
        </a>
      ) : (
        <button type="button" className={styles["target"]} onClick={onOpen}>
          {inner}
        </button>
      )}
    </li>
  );
}

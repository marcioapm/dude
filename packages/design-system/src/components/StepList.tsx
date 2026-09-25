import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import styles from "./StepList.module.css";

export interface StepListProps extends HTMLAttributes<HTMLOListElement> {
  readonly children?: ReactNode;
}

/**
 * Steps in order — a delivery's phases, then its pull request — one framed
 * list, a hairline between rows. Each row opens something: the step's
 * conversation, or a page elsewhere. The running step is told by its
 * status and its avatar, not a coloured edge.
 */
export function StepList({ className, children, ...rest }: StepListProps) {
  return (
    <ol className={cx(styles["list"], className)} {...rest}>
      {children}
    </ol>
  );
}

interface StepRowBase extends Omit<HTMLAttributes<HTMLLIElement>, "title" | "onClick"> {
  /** Its place: "1", "2", "PR". Muted mono, one column wide. */
  readonly step: ReactNode;
  /** Who does it: an AgentAvatar. */
  readonly avatar?: ReactNode;
  readonly label: ReactNode;
  /** Its StatusBadge. */
  readonly status?: ReactNode;
  /** After the status, muted: "2 blocking", "checks passing". */
  readonly note?: ReactNode;
  /** On the right, muted mono: a commit, a branch. */
  readonly meta?: ReactNode;
  /** The meta's full text, when it is shortened. */
  readonly metaTitle?: string | undefined;
}

export type StepRowProps = StepRowBase &
  (
    | { readonly onOpen: () => void; readonly href?: undefined }
    /** Opens elsewhere, in a new tab. */
    | { readonly href: string; readonly onOpen?: undefined }
  );

/** One step: a button that opens it, or a link out. */
export function StepRow({ step, avatar, label, status, note, meta, metaTitle, onOpen, href, className, ...rest }: StepRowProps) {
  const inner = (
    <>
      <span className={cx(styles["step"], "ds-mono")}>{step}</span>
      {avatar}
      <span className={styles["label"]}>{label}</span>
      {status}
      {note ? <span className={styles["note"]}>{note}</span> : null}
      {meta ? (
        <span className={cx(styles["meta"], "ds-mono")} title={metaTitle}>
          {meta}
        </span>
      ) : null}
      <Icon name={href ? "external" : "chevron-right"} size={14} className={styles["open"]} />
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

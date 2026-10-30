import { useId, useRef, useState, type HTMLAttributes, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import styles from "./Disclosure.module.css";

export interface DisclosureProps extends Omit<HTMLAttributes<HTMLElement>, "title" | "onToggle"> {
  /** What it holds, as its summary's name: "Goal & acceptance criteria". */
  readonly title: ReactNode;
  /** After the title, muted and tabular: "6 criteria", "2 attempts". Part of the summary's name. */
  readonly meta?: ReactNode;
  /** Open from the start (uncontrolled). */
  readonly defaultOpen?: boolean | undefined;
  /** Controlled: open or closed, with `onOpenChange`. */
  readonly open?: boolean | undefined;
  readonly onOpenChange?: ((open: boolean) => void) | undefined;
  /** Mount the body only while open: a body that reads its own data reads it when asked. */
  readonly lazy?: boolean | undefined;
  readonly children: ReactNode;
}

/**
 * A section folded on the chrome shade, opened in place: a summary row
 * (chevron, title, a muted fact about it) over the body at its full
 * length. For the details under a page's summary (a task's goal, its
 * relations, its history), never for what someone must see to act. A real
 * `<details>`, so it opens from the keyboard and is announced as expanded
 * or collapsed with no script; the body is a labelled region.
 */
export function Disclosure({ title, meta, defaultOpen = false, open: controlled, onOpenChange, lazy = false, className, children, ...rest }: DisclosureProps) {
  const [own, setOwn] = useState(defaultOpen);
  const open = controlled ?? own;
  // What it last said or was told: a second toggle event before the next render says nothing again.
  const said = useRef(open);
  said.current = open;
  const titleId = useId();
  return (
    <details
      className={cx(styles["root"], className)}
      open={open}
      onToggle={(e) => {
        const next = (e.currentTarget as HTMLDetailsElement).open;
        if (next === said.current) return;
        said.current = next;
        if (controlled === undefined) setOwn(next);
        onOpenChange?.(next);
      }}
      {...rest}
    >
      <summary className={styles["summary"]}>
        <Icon name="chevron-right" size={14} className={styles["chevron"]} />
        <span id={titleId} className={styles["title"]}>{title}</span>
        {meta !== undefined && meta !== null ? <span className={styles["meta"]}>{meta}</span> : null}
      </summary>
      <div className={styles["body"]} role="region" aria-labelledby={titleId}>
        {!lazy || open ? children : null}
      </div>
    </details>
  );
}

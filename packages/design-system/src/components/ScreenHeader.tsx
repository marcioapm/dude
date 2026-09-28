import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import styles from "./ScreenHeader.module.css";

export interface ScreenHeaderProps extends Omit<HTMLAttributes<HTMLElement>, "title"> {
  /** Before the title: where this is (a Breadcrumb), or a face. */
  readonly lead?: ReactNode;
  /** The screen's name. Omit when the breadcrumb says it. */
  readonly title?: ReactNode;
  /** After the title, muted: what it is, its figures. */
  readonly meta?: ReactNode;
  /** At the right: the screen's actions, one primary at most. */
  readonly actions?: ReactNode;
}

/**
 * The line at the top of a screen: where it is, what it is called, a few
 * figures, and what can be done. On the surface, no rule under it: the
 * content below starts on the same shade.
 */
export function ScreenHeader({ lead, title, meta, actions, className, children, ...rest }: ScreenHeaderProps) {
  return (
    <header className={cx(styles["root"], className)} {...rest}>
      {lead}
      {title !== undefined ? <h1 className={styles["title"]}>{title}</h1> : null}
      {meta !== undefined ? <span className={styles["meta"]}>{meta}</span> : null}
      <span className={styles["spacer"]} />
      {actions !== undefined ? <span className={styles["actions"]}>{actions}</span> : null}
      {children}
    </header>
  );
}

export interface SegmentedProps<T extends string> extends Omit<HTMLAttributes<HTMLSpanElement>, "onChange"> {
  readonly options: ReadonlyArray<{ readonly value: T; readonly label: ReactNode }>;
  readonly value: T;
  readonly onChange: (value: T) => void;
  /** Names the group for a screen reader: "Whose tasks". */
  readonly label: string;
  readonly size?: "sm" | "md" | undefined;
}

/** Two or three views of the same thing, one chosen: Everyone / Mine, All / Open / Fixed. */
export function Segmented<T extends string>({ options, value, onChange, label, size = "md", className, ...rest }: SegmentedProps<T>) {
  return (
    <span className={cx(styles["seg"], size === "sm" && styles["segSm"], className)} role="group" aria-label={label} {...rest}>
      {options.map((o) => (
        <button key={o.value} type="button" aria-pressed={o.value === value} onClick={() => onChange(o.value)}>
          {o.label}
        </button>
      ))}
    </span>
  );
}

import type { HTMLAttributes, KeyboardEvent, ReactNode } from "react";
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
  readonly options: ReadonlyArray<{ readonly value: T; readonly label: ReactNode; readonly disabled?: boolean | undefined }>;
  readonly value: T;
  readonly onChange: (value: T) => void;
  /** Names the group for a screen reader: "Whose tasks". */
  readonly label: string;
  /** `toolbar`: level with `sm` buttons in a bar (its track is `control-sm`), words at the `sm` text size. */
  readonly size?: "sm" | "md" | "toolbar" | undefined;
  readonly disabled?: boolean | undefined;
  /**
   * The options switch panels the caller renders: a `tablist` whose tabs
   * are `${tabs}-${value}-tab` and control `${tabs}-${value}-panel`, with
   * one Tab stop, ← → between them and Home End to the ends. Omitted: a
   * group of pressed buttons.
   */
  readonly tabs?: string | undefined;
}

/** Two or three views of the same thing, one chosen: Everyone / Mine, All / Open / Fixed. */
export function Segmented<T extends string>({ options, value, onChange, label, size = "md", disabled, tabs, className, ...rest }: SegmentedProps<T>) {
  const enabled = options.filter((o) => !(disabled || o.disabled));
  function move(e: KeyboardEvent<HTMLButtonElement>, from: T): void {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(e.key)) return;
    e.preventDefault();
    const at = enabled.findIndex((o) => o.value === from);
    let to: number;
    if (e.key === "Home") to = 0;
    else if (e.key === "End") to = enabled.length - 1;
    else to = (at + (e.key === "ArrowRight" ? 1 : -1) + enabled.length) % enabled.length;
    const next = enabled[to];
    if (!next) return;
    onChange(next.value);
    e.currentTarget.parentElement?.querySelector<HTMLButtonElement>(`#${CSS.escape(`${tabs}-${next.value}-tab`)}`)?.focus();
  }
  return (
    <span className={cx(styles["seg"], size === "sm" && styles["segSm"], size === "toolbar" && styles["segToolbar"], className)} role={tabs ? "tablist" : "group"} aria-label={label} {...rest}>
      {options.map((o) => {
        const off = disabled || o.disabled;
        const on = o.value === value;
        return (
          <button key={o.value} type="button" disabled={off} onClick={() => onChange(o.value)}
            {...(tabs ? {
              role: "tab",
              id: `${tabs}-${o.value}-tab`,
              "aria-controls": `${tabs}-${o.value}-panel`,
              "aria-selected": on,
              tabIndex: on ? 0 : -1,
              onKeyDown: (e: KeyboardEvent<HTMLButtonElement>) => move(e, o.value),
            } : { "aria-pressed": on })}>
            {o.label}
          </button>
        );
      })}
    </span>
  );
}

import type { HTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { statusSpec, type Status, type StatusEmphasis } from "../tokens/status.ts";
import styles from "./StatusBadge.module.css";

export interface StatusBadgeProps extends Omit<HTMLAttributes<HTMLSpanElement>, "children"> {
  readonly status: Status;
  /** Override the default emphasis from the status vocabulary. */
  readonly emphasis?: StatusEmphasis | undefined;
  readonly size?: "sm" | "md" | undefined;
  /** Hide the text and keep the glyph; the label moves to `title`. */
  readonly iconOnly?: boolean | undefined;
  /**
   * Dot mode: an 8px mark + optional label. For very dense lists (session
   * trees, kanban cards). Shape still carries the meaning: hollow = pending,
   * round = active, square = terminal, diamond = needs you.
   */
  readonly variant?: "badge" | "dot" | undefined;
  /** Replace the vocabulary label (e.g. "Needs you (2)"). */
  readonly label?: string | undefined;
}

/**
 * The one component for every Run / Session / Task status.
 *
 * Meaning is carried three ways at once — tone, glyph, and text — so no
 * single channel is load-bearing. `awaiting_input` / `awaiting_input`
 * are the only statuses that default to the solid treatment; do not
 * promote anything else to solid, or the signal is lost.
 */
export function StatusBadge({
  status,
  emphasis,
  size = "md",
  iconOnly,
  variant = "badge",
  label,
  className,
  ...rest
}: StatusBadgeProps) {
  const spec = statusSpec(status);
  const text = label ?? spec.label;
  const em = emphasis ?? spec.emphasis;
  const stateClasses = cx(
    styles[spec.tone],
    spec.live && styles["live"],
    spec.needsHuman && styles["needsHuman"],
    spec.terminal && styles["terminal"],
    (status === "pending" || status === "received") && styles["pending"],
  );

  if (variant === "dot") {
    return (
      <span
        className={cx(styles["dot"], stateClasses, className)}
        data-status={status}
        title={iconOnly ? text : undefined}
        {...rest}
      >
        <span className={styles["dotMark"]} aria-hidden />
        {iconOnly ? <span className="ds-sr-only">{text}</span> : <span className={cx(styles["label"], "ds-cap")}>{text}</span>}
      </span>
    );
  }

  return (
    <span
      className={cx(
        styles["root"],
        stateClasses,
        styles[em],
        size === "sm" && styles["sm"],
        iconOnly && styles["iconOnly"],
        className,
      )}
      data-status={status}
      title={iconOnly ? text : undefined}
      {...rest}
    >
      <span className={styles["glyph"]} aria-hidden>
        <Icon name={spec.glyph as IconName} size={size === "sm" ? 10 : 12} strokeWidth={1.75} />
      </span>
      {iconOnly ? <span className="ds-sr-only">{text}</span> : <span className={cx(styles["label"], "ds-cap")}>{text}</span>}
    </span>
  );
}

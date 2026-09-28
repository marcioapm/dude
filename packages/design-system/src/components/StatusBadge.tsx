import type { HTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import { statusSpec, type Status, type StatusEmphasis } from "../tokens/status.ts";
import { StatusMark } from "./StatusMark.tsx";
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
 * single channel is load-bearing. The badge variant draws a `StatusMark`
 * (glyph and word, no box); `awaiting_input` alone defaults to the solid
 * pill. Do not promote anything else to solid, or the signal is lost.
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

  // The badge is a glyph and a word now (`StatusMark`): no bordered pill.
  // Only a state waiting on a person keeps a fill, and only when solid.
  return <StatusMark status={status} size={size} iconOnly={iconOnly} label={label} emphasis={em} className={className} {...rest} />;
}

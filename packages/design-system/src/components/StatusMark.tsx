import type { HTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { statusSpec, type Status, type StatusEmphasis } from "../tokens/status.ts";
import styles from "./StatusMark.module.css";

export interface StatusMarkProps extends Omit<HTMLAttributes<HTMLSpanElement>, "children"> {
  readonly status: Status;
  readonly size?: "sm" | "md" | undefined;
  /** Keep the glyph, hide the word; the word moves to `title` and the accessible name. */
  readonly iconOnly?: boolean | undefined;
  /** Replace the vocabulary's word ("Needs you (2)"). */
  readonly label?: string | undefined;
  /** Only `solid` means anything, and only for a state that waits on a person: the one filled pill. */
  readonly emphasis?: StatusEmphasis | undefined;
}

/**
 * A status as a glyph and a word in its tone — no box, no fill. The shape
 * carries it in grayscale, the word carries it to a screen reader, the tone
 * carries it at a glance. Running breathes; nothing else moves.
 *
 * The one exception is a state that waits on a person (`needsHuman` and
 * `solid` in the vocabulary): that is the single filled pill on a screen,
 * with its slow ring, because it is the only thing that must be loud.
 */
export function StatusMark({ status, size = "md", iconOnly, label, emphasis, className, ...rest }: StatusMarkProps) {
  const spec = statusSpec(status);
  const text = label ?? spec.label;
  const loud = spec.needsHuman && (emphasis ?? spec.emphasis) === "solid";
  return (
    <span
      className={cx(styles["root"], styles[spec.tone], size === "sm" && styles["sm"], spec.live && styles["live"], loud && styles["loud"], className)}
      data-status={status}
      title={iconOnly ? text : undefined}
      {...rest}
    >
      <Icon name={spec.glyph as IconName} size={size === "sm" ? 12 : 14} strokeWidth={1.75} className={styles["glyph"]} />
      {iconOnly ? <span className="ds-sr-only">{text}</span> : <span className={cx(styles["word"], "ds-cap")}>{text}</span>}
    </span>
  );
}

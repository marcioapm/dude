import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import type { ToneName } from "../tokens/palette.ts";
import styles from "./Badge.module.css";

export type BadgeEmphasis = "subtle" | "tinted" | "solid";

export interface BadgeProps extends HTMLAttributes<HTMLSpanElement> {
  readonly tone?: ToneName | undefined;
  readonly emphasis?: BadgeEmphasis | undefined;
  readonly icon?: IconName | undefined;
  /** A leading 6px dot instead of an icon. */
  readonly dot?: boolean | undefined;
  readonly size?: "sm" | "md" | undefined;
  /** Monospace: for IDs, SHAs, versions, counts. */
  readonly mono?: boolean | undefined;
  readonly children?: ReactNode;
}

/**
 * Badge / Tag. Generic labelled chip. For domain statuses use `StatusBadge`
 * — it enforces the status vocabulary; this one is for everything else
 * (labels, versions, counts, model names).
 */
export function Badge({
  tone = "neutral",
  emphasis = "tinted",
  icon,
  dot,
  size = "md",
  mono,
  className,
  children,
  ...rest
}: BadgeProps) {
  return (
    <span
      className={cx(
        styles["root"],
        styles[tone],
        styles[emphasis],
        size === "sm" && styles["sm"],
        mono && styles["mono"],
        className,
      )}
      {...rest}
    >
      {dot ? <span className={styles["dot"]} aria-hidden /> : null}
      {icon ? <Icon name={icon} size={size === "sm" ? 10 : 12} /> : null}
      <span className={styles["text"]}>{children}</span>
    </span>
  );
}

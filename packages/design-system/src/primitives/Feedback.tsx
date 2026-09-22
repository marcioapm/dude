import type { CSSProperties, HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import styles from "./Feedback.module.css";

export interface SkeletonProps extends HTMLAttributes<HTMLSpanElement> {
  readonly width?: number | string | undefined;
  readonly height?: number | string | undefined;
  readonly variant?: "rect" | "text" | "circle" | undefined;
}

/** Placeholder block. Matches the shape of the content it stands in for. */
export function Skeleton({ width, height, variant = "rect", className, style, ...rest }: SkeletonProps) {
  const s: CSSProperties = { ...style };
  if (width !== undefined) s.width = width;
  if (height !== undefined) s.height = height;
  return (
    <span
      className={cx(styles["skeleton"], variant === "text" && styles["text"], variant === "circle" && styles["circle"], className)}
      style={s}
      aria-hidden
      {...rest}
    />
  );
}

/** N lines of text skeleton, last one shorter. */
export function SkeletonLines({ lines = 3, className }: { readonly lines?: number | undefined; readonly className?: string | undefined }) {
  return (
    <span className={cx(styles["lines"], className)} aria-hidden>
      {Array.from({ length: lines }, (_, i) => (
        <Skeleton key={i} variant="text" width={i === lines - 1 ? "60%" : "100%"} />
      ))}
    </span>
  );
}

/** Inline spinner with optional label; for short waits inside a component. */
export function Spinner({ label, size = 14 }: { readonly label?: string | undefined; readonly size?: number | undefined }) {
  return (
    <span className={styles["spinner"]} role="status" aria-live="polite">
      <Icon name="spinner" size={size} />
      {label ? <span>{label}</span> : <span className="ds-sr-only">Loading</span>}
    </span>
  );
}

export interface EmptyStateProps {
  readonly icon?: IconName | undefined;
  readonly title: ReactNode;
  readonly description?: ReactNode;
  readonly action?: ReactNode;
  /** Single-row layout for panels and table bodies. */
  readonly compact?: boolean | undefined;
  readonly className?: string | undefined;
}

/**
 * Empty state. Say what would appear here and, if possible, how to make it
 * appear. Never an illustration — this is a console.
 */
export function EmptyState({ icon = "inbox", title, description, action, compact, className }: EmptyStateProps) {
  return (
    <div className={cx(styles["empty"], compact && styles["emptyCompact"], className)}>
      <span className={styles["emptyIcon"]} aria-hidden>
        <Icon name={icon} size={compact ? 12 : 16} />
      </span>
      <div className={styles["emptyText"]}>
        <div className={styles["emptyTitle"]}>{title}</div>
        {description ? <div className={styles["emptyDesc"]}>{description}</div> : null}
      </div>
      {action ? <div className={styles["emptyAction"]}>{action}</div> : null}
    </div>
  );
}

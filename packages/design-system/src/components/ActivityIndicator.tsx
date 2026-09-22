import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { formatDuration } from "../util/format.ts";
import { toMs, useNow } from "../util/useNow.ts";
import { activitySpec, TOOL_SLOW_AFTER_MS, type ActivityKind } from "../tokens/activity.ts";
import { StatusBadge } from "./StatusBadge.tsx";
import styles from "./ActivityIndicator.module.css";

export interface ActivityIndicatorProps extends Omit<HTMLAttributes<HTMLSpanElement>, "children"> {
  readonly kind: ActivityKind;
  /**
   * `line` (default): a full-width status line for the foot of a message.
   * `badge`: a compact chip for headers and collapsed threads.
   */
  readonly variant?: "line" | "badge" | undefined;
  /** When this state began. Drives the elapsed time. */
  readonly since?: string | number | Date | undefined;
  /** Replace the vocabulary label. */
  readonly label?: string | undefined;
  /** Free text after the label ("Reading client.ts", "429 from api.anthropic.com"). */
  readonly detail?: ReactNode;
  /** `tool` only: the tool name, shown in mono. */
  readonly tool?: string | undefined;
  /** `tool` only: promote to "slow" (attention ink) after this long. */
  readonly slowAfterMs?: number | undefined;
  /** `retrying` only. */
  readonly attempt?: number | undefined;
  readonly maxAttempts?: number | undefined;
  /** `retrying` only: when the next attempt fires. Drives the countdown. */
  readonly retryAt?: string | number | Date | undefined;
  readonly size?: "sm" | "md" | undefined;
}

/**
 * What an agent turn is doing right now. Each state has its own tone,
 * glyph *and* rhythm, so they are distinguishable in peripheral vision:
 *
 *   thinking   dashed ring drifting slowly
 *   streaming  block caret blinking (stepped)
 *   tool       a sweep along a 2px track, elapsed time ticking; turns
 *              attention-toned once it has run past `slowAfterMs`
 *   retrying   countdown ring depleting once a second, attempt N of M
 *   needs you  the StatusBadge ring, unchanged — still the loudest thing
 *
 * Everything loops via `--ds-motion-live`; reduced motion freezes each in a
 * legible pose. The countdown and elapsed times are state, not decoration,
 * and keep ticking as text.
 */
export function ActivityIndicator({
  kind,
  variant = "line",
  since,
  label,
  detail,
  tool,
  slowAfterMs = TOOL_SLOW_AFTER_MS,
  attempt,
  maxAttempts,
  retryAt,
  size = "md",
  className,
  ...rest
}: ActivityIndicatorProps) {
  const spec = activitySpec(kind);
  const now = useNow(spec.live);
  const start = toMs(since);
  const elapsed = start === null ? null : Math.max(0, now - start);
  const slow = kind === "tool" && elapsed !== null && elapsed >= slowAfterMs;
  const text = label ?? (slow ? "Still running" : spec.label);

  // Retry countdown: remaining until retryAt, and the fraction of the wait already spent.
  const retryAtMs = toMs(retryAt);
  const remaining = retryAtMs === null ? null : Math.max(0, retryAtMs - now);
  const total = retryAtMs !== null && start !== null ? Math.max(1, retryAtMs - start) : null;
  const fraction = remaining !== null && total !== null ? Math.min(1, Math.max(0, 1 - remaining / total)) : 0;

  const isBadge = variant === "badge";

  if (kind === "awaiting_input") {
    // Reuse the loudest treatment in the system as-is.
    return (
      <span className={cx(styles["root"], isBadge ? styles["badge"] : styles["line"], styles["attention"], styles["needsHuman"], className)} data-activity={kind} {...rest}>
        <StatusBadge status="awaiting_input" size="sm" label={label} />
        {!isBadge ? <span className={styles["detail"]}>{detail ?? "Waiting for your answer"}</span> : null}
        {!isBadge && elapsed !== null ? <span className={cx(styles["elapsed"], styles["elapsedAttention"])}>{formatDuration(elapsed)}</span> : null}
      </span>
    );
  }

  const glyph = (
    <span className={cx(styles["glyph"], styles[`motion-${spec.motion}`], slow && styles["slow"])} aria-hidden>
      {kind === "retrying" ? (
        <span className={styles["countdown"]}>
          <svg viewBox="0 0 16 16" width="100%" height="100%" className={styles["countdownRing"]}>
            <circle cx="8" cy="8" r="6.5" className={styles["countdownTrack"]} />
            <circle
              cx="8"
              cy="8"
              r="6.5"
              className={styles["countdownFill"]}
              style={{ strokeDashoffset: `${(1 - fraction) * 40.84}` }}
            />
          </svg>
          <Icon name="retry" size={size === "sm" ? 8 : 9} strokeWidth={2} />
        </span>
      ) : kind === "streaming" ? (
        <span className={styles["caret"]} />
      ) : (
        <Icon name={spec.glyph} size={size === "sm" ? 11 : 12} strokeWidth={kind === "thinking" ? 2 : 1.75} />
      )}
    </span>
  );

  return (
    <span
      className={cx(
        styles["root"],
        isBadge ? styles["badge"] : styles["line"],
        styles[spec.tone],
        slow && styles["attention"],
        spec.live && styles["live"],
        spec.terminal && styles["terminal"],
        size === "sm" && styles["sm"],
        className,
      )}
      data-activity={kind}
      data-slow={slow ? "true" : undefined}
      role="status"
      aria-live={spec.needsHuman ? "assertive" : "polite"}
      {...rest}
    >
      {glyph}
      <span className={styles["label"]}>{text}</span>
      {kind === "tool" && tool ? <code className={styles["tool"]}>{tool}</code> : null}
      {kind === "retrying" && attempt !== undefined ? (
        <span className={styles["attempt"]}>
          attempt {attempt}
          {maxAttempts !== undefined ? ` of ${maxAttempts}` : ""}
        </span>
      ) : null}
      {detail !== undefined && detail !== null ? <span className={styles["detail"]}>{detail}</span> : null}
      {kind === "retrying" && remaining !== null ? (
        <span className={cx(styles["elapsed"], styles["elapsedAttention"])}>{remaining <= 0 ? "retrying now" : `next in ${Math.ceil(remaining / 1000)}s`}</span>
      ) : elapsed !== null ? (
        <span className={cx(styles["elapsed"], slow && styles["elapsedAttention"])}>{formatDuration(elapsed)}</span>
      ) : null}
      {kind === "tool" && !isBadge ? (
        <span className={styles["track"]} aria-hidden>
          <span className={styles["sweep"]} />
        </span>
      ) : null}
    </span>
  );
}

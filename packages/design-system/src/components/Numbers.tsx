import { useEffect, useState, type HTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import { formatDuration, formatTokens, formatUsd, type DurationOptions } from "../util/format.ts";
import styles from "./Numbers.module.css";

interface NumberBaseProps extends Omit<HTMLAttributes<HTMLSpanElement>, "children"> {
  readonly mono?: boolean | undefined;
  readonly tone?: "default" | "secondary" | "muted" | undefined;
}

export interface CostDisplayProps extends NumberBaseProps {
  readonly usd: number;
  readonly compact?: boolean | undefined;
  /** When set, shows `/ $budget` and colors the value at 80% and 100%. */
  readonly budgetUsd?: number | undefined;
  /** Live: value is still accruing. Adds a small pulse. */
  readonly live?: boolean | undefined;
}

/**
 * Money, formatted consistently everywhere. Always tabular numerals. Full
 * precision lives in `title` so hovering a rounded number reveals cents.
 */
export function CostDisplay({ usd, compact, budgetUsd, live, mono, tone = "default", className, ...rest }: CostDisplayProps) {
  const ratio = budgetUsd !== undefined && budgetUsd > 0 ? usd / budgetUsd : 0;
  const state = ratio >= 1 ? "over" : ratio >= 0.8 ? "warn" : null;
  return (
    <span
      className={cx(
        styles["num"],
        mono && styles["mono"],
        tone === "muted" && styles["muted"],
        tone === "secondary" && styles["secondary"],
        state === "warn" && styles["warn"],
        state === "over" && styles["over"],
        live && styles["live"],
        className,
      )}
      title={`$${usd.toFixed(4)}${budgetUsd !== undefined ? ` of $${budgetUsd.toFixed(2)} budget` : ""}`}
      {...rest}
    >
      {formatUsd(usd, { compact: compact ?? false })}
      {budgetUsd !== undefined ? <span className={styles["budget"]}>/ {formatUsd(budgetUsd, { compact: true })}</span> : null}
    </span>
  );
}

export interface TokenCountProps extends NumberBaseProps {
  readonly tokens: number;
  readonly exact?: boolean | undefined;
}

export function TokenCount({ tokens, exact, mono, tone = "default", className, ...rest }: TokenCountProps) {
  return (
    <span
      className={cx(styles["num"], mono && styles["mono"], tone === "muted" && styles["muted"], tone === "secondary" && styles["secondary"], className)}
      title={`${Math.round(tokens).toLocaleString("en-US")} tokens`}
      {...rest}
    >
      {formatTokens(tokens, { exact: exact ?? false })}
      <span className={styles["unit"]}>tok</span>
    </span>
  );
}

export interface DurationProps extends NumberBaseProps {
  /** `short` = "3m 12s" (default); `clock` = "03:12"; `long` = "3 min 12 sec". */
  readonly format?: DurationOptions["style"] | undefined;
  /** Elapsed milliseconds. Ignored when `since` is given. */
  readonly ms?: number | undefined;
  /** Start time; the component ticks once a second until `until` is set. */
  readonly since?: string | number | Date | undefined;
  /** End time; when provided with `since`, the display is static. */
  readonly until?: string | number | Date | null | undefined;
}

/**
 * Elapsed time. Static when given `ms` or `since`+`until`; live-ticking
 * when only `since` is given. Tick rate is 1s — nothing in this product
 * needs sub-second live timers, and they read as noise.
 */
export function Duration({ ms, since, until, format, mono, tone = "default", className, ...rest }: DurationProps) {
  const isLive = since !== undefined && (until === undefined || until === null);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!isLive) return;
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, [isLive]);

  let elapsed: number;
  if (since !== undefined) {
    const start = new Date(since).getTime();
    const end = until !== undefined && until !== null ? new Date(until).getTime() : now;
    elapsed = end - start;
  } else {
    elapsed = ms ?? 0;
  }

  return (
    <span
      className={cx(
        styles["num"],
        mono && styles["mono"],
        tone === "muted" && styles["muted"],
        tone === "secondary" && styles["secondary"],
        isLive && styles["live"],
        className,
      )}
      title={`${Math.round(elapsed).toLocaleString("en-US")} ms`}
      {...rest}
    >
      {formatDuration(elapsed, format !== undefined ? { style: format } : {})}
    </span>
  );
}

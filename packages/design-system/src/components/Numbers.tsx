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
  /** `short` = "3m 12s" (default); `clock` = "03:12"; `long` = "3 min 12 sec"; `age` = "4h". */
  readonly format?: DurationOptions["style"] | undefined;
  /** Elapsed milliseconds. Ignored when `since` is given. */
  readonly ms?: number | undefined;
  /** Start time. */
  readonly since?: string | number | Date | undefined;
  /** End time; when known, the display is static and measures to it. */
  readonly until?: string | number | Date | null | undefined;
  /**
   * Whether the clock is still running. Defaults to "yes if there is no
   * `until`".
   *
   * Say so explicitly for something settled without a recorded end — an
   * aborted Run whose `endedAt` never made it to the ledger, say. Otherwise
   * it ticks upward forever, reading as though the work were still going.
   */
  readonly live?: boolean | undefined;
}

/**
 * Elapsed time. Static when given `ms`, an `until`, or `live={false}`;
 * live-ticking otherwise. Tick rate is 1s — nothing in this product needs
 * sub-second live timers, and they read as noise.
 */
export function Duration({ ms, since, until, live, format, mono, tone = "default", className, ...rest }: DurationProps) {
  const running = live ?? (until === undefined || until === null);
  const isLive = since !== undefined && running;
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!isLive) return;
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, [isLive]);

  let elapsed: number;
  if (since !== undefined) {
    const start = new Date(since).getTime();
    // A settled duration with no recorded end has nothing to measure to;
    // freezing at zero is honest, where counting up would be a lie.
    const end =
      until !== undefined && until !== null ? new Date(until).getTime() : isLive ? now : start;
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

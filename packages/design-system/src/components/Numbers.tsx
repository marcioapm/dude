import { useEffect, useState, type HTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import { formatDuration, formatTokens, formatUsd, type DurationOptions } from "../util/format.ts";
import styles from "./Numbers.module.css";

const INT = new Intl.NumberFormat("en-US", { maximumFractionDigits: 0 });

/**
 * Where a value stands against its limit: `warn` from 80%, `over` at
 * 100%. A cost against its budget and a context against its window use
 * the same thresholds so the same colour means the same thing.
 */
export function limitState(value: number, limit: number | undefined): "warn" | "over" | null {
  if (limit === undefined || limit <= 0) return null;
  const ratio = value / limit;
  return ratio >= 1 ? "over" : ratio >= 0.8 ? "warn" : null;
}

interface NumberBaseProps extends Omit<HTMLAttributes<HTMLSpanElement>, "children"> {
  readonly mono?: boolean | undefined;
  readonly tone?: "default" | "secondary" | "muted" | undefined;
}

export interface CostDisplayProps extends NumberBaseProps {
  /**
   * `null` means the cost is not known — a harness on a subscription that
   * reports nothing, or a run whose ledger has not caught up. It renders
   * as an em dash with a title, never as `$0.00`: zero is a price, unknown
   * is not.
   */
  readonly usd: number | null;
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
  if (usd === null) {
    return (
      <span className={cx(styles["num"], styles["unknown"], mono && styles["mono"], className)} title="Cost not reported" aria-label="Cost not reported" {...rest}>
        —
      </span>
    );
  }
  const state = limitState(usd, budgetUsd);
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
  /**
   * A short word before the number saying which count this is — `ctx`
   * (context at this point), `out` (output tokens), `in`. Replaces the
   * trailing `tok` unit, which the label already implies.
   */
  readonly label?: string | undefined;
  /**
   * The window the count sits in (the model's context limit). Shows
   * `/ 744k` and colours the value at 80% and 100%, the same thresholds
   * a cost takes against its budget.
   */
  readonly windowTokens?: number | undefined;
}

export function TokenCount({ tokens, exact, label, windowTokens, mono, tone = "default", className, ...rest }: TokenCountProps) {
  // A zero window is "not known", not a limit of nothing.
  const window = windowTokens !== undefined && windowTokens > 0 ? windowTokens : undefined;
  const state = limitState(tokens, window);
  const title = `${INT.format(tokens)} ${label ? `${label} ` : ""}tokens${window !== undefined ? ` of ${INT.format(window)} (${Math.round((tokens / window) * 100)}%)` : ""}`;
  return (
    <span
      className={cx(
        styles["num"],
        mono && styles["mono"],
        tone === "muted" && styles["muted"],
        tone === "secondary" && styles["secondary"],
        state === "warn" && styles["warn"],
        state === "over" && styles["over"],
        className,
      )}
      title={title}
      {...rest}
    >
      {label ? <span className={styles["prefix"]}>{label}</span> : null}
      {formatTokens(tokens, { exact: exact ?? false })}
      {window !== undefined ? <span className={styles["budget"]}>/ {formatTokens(window)}</span> : null}
      {!label ? <span className={styles["unit"]}>tok</span> : null}
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
      title={`${INT.format(elapsed)} ms`}
      {...rest}
    >
      {formatDuration(elapsed, format !== undefined ? { style: format } : {})}
    </span>
  );
}

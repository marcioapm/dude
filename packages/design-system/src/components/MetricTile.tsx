import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { formatDuration, formatPercent, formatTokens, formatUsd } from "../util/format.ts";
import styles from "./MetricTile.module.css";

export type MetricUnit = "usd" | "tokens" | "ms" | "count" | "percent" | "none";

export interface MetricDelta {
  /** Signed change. For `percent` deltas this is a fraction (0.12 = +12%). */
  readonly value: number;
  readonly kind?: "absolute" | "percent" | undefined;
  /**
   * Which direction is good. Cost going up is bad; throughput going up is
   * good; many metrics are neutral (token count). Default: neutral.
   */
  readonly goodDirection?: "up" | "down" | "neutral" | undefined;
  /** Comparison basis, e.g. "vs yesterday". Shown as the sub line unless `sub` is set. */
  readonly label?: string | undefined;
}

export interface MetricTileProps extends Omit<HTMLAttributes<HTMLDivElement>, "children"> {
  readonly label: ReactNode;
  readonly value: number | string;
  readonly unit?: MetricUnit | undefined;
  /** Text shown after the number (only when unit is `none`/`count`). */
  readonly unitLabel?: string | undefined;
  readonly icon?: IconName | undefined;
  readonly delta?: MetricDelta | undefined;
  /** Secondary line under the value (e.g. "12 sessions"). */
  readonly sub?: ReactNode;
  /** Still accruing. */
  readonly live?: boolean | undefined;
  /** Budget / limit; renders a thin progress bar that turns at 80% and 100%. */
  readonly max?: number | undefined;
  readonly size?: "sm" | "md" | "lg" | undefined;
  /** No card chrome — for embedding in a table cell or side panel. */
  readonly flat?: boolean | undefined;
}

function formatValue(value: number | string, unit: MetricUnit): { text: string; suffix?: string } {
  if (typeof value === "string") return { text: value };
  switch (unit) {
    case "usd":
      return { text: formatUsd(value, { compact: value >= 10_000 }) };
    case "tokens":
      return { text: formatTokens(value), suffix: "tok" };
    case "ms":
      return { text: formatDuration(value) };
    case "percent":
      return { text: `${(value * 100).toFixed(value * 100 >= 10 ? 0 : 1)}`, suffix: "%" };
    case "count":
    case "none":
      return { text: value.toLocaleString("en-US") };
  }
}

/**
 * One number that matters. Label above, value large, delta and unit small.
 * Numbers are tabular so a row of tiles stays aligned while values tick.
 */
export function MetricTile({
  label,
  value,
  unit = "none",
  unitLabel,
  icon,
  delta,
  sub,
  live,
  max,
  size = "md",
  flat,
  className,
  ...rest
}: MetricTileProps) {
  const { text, suffix } = formatValue(value, unit);
  const ratio = max !== undefined && max > 0 && typeof value === "number" ? value / max : null;
  const barState = ratio === null ? null : ratio >= 1 ? "over" : ratio >= 0.8 ? "warn" : "ok";

  let deltaNode: ReactNode = null;
  if (delta) {
    const dir = delta.value > 0 ? "up" : delta.value < 0 ? "down" : "flat";
    const good = delta.goodDirection ?? "neutral";
    const cls =
      dir === "flat"
        ? styles["deltaFlat"]
        : good === "neutral"
          ? styles["deltaNeutral"]
          : (dir === "up") === (good === "up")
            ? styles["deltaUp"]
            : styles["deltaDown"];
    const deltaText =
      delta.kind === "percent"
        ? formatPercent(delta.value)
        : unit === "usd"
          ? `${delta.value > 0 ? "+" : delta.value < 0 ? "-" : ""}${formatUsd(Math.abs(delta.value), { compact: true })}`
          : unit === "tokens"
            ? `${delta.value > 0 ? "+" : ""}${formatTokens(delta.value)}`
            : unit === "ms"
              ? `${delta.value > 0 ? "+" : delta.value < 0 ? "-" : ""}${formatDuration(Math.abs(delta.value))}`
              : `${delta.value > 0 ? "+" : ""}${delta.value.toLocaleString("en-US")}`;
    deltaNode = (
      <span className={cx(styles["delta"], cls)} title={delta.label ? `${deltaText} ${delta.label}` : undefined}>
        {dir !== "flat" ? <Icon name={dir === "up" ? "arrow-up" : "arrow-down"} size={10} strokeWidth={2} /> : null}
        {deltaText}
      </span>
    );
  }

  return (
    <div className={cx(styles["root"], flat && styles["flat"], live && styles["live"], className)} {...rest}>
      <div className={styles["label"]}>
        {icon ? <Icon name={icon} size={11} className={styles["labelIcon"]} /> : null}
        {label}
      </div>
      <div className={styles["valueRow"]}>
        <span className={cx(styles["value"], size === "sm" && styles["valueSm"], size === "lg" && styles["valueLg"])}>
          {text}
          {suffix || unitLabel ? <span className={styles["unit"]}>{suffix ?? unitLabel}</span> : null}
        </span>
        {deltaNode}
      </div>
      {ratio !== null ? (
        <div
          className={cx(styles["bar"], barState === "warn" && styles["barWarn"], barState === "over" && styles["barOver"])}
          role="progressbar"
          aria-valuemin={0}
          aria-valuemax={max}
          aria-valuenow={typeof value === "number" ? value : undefined}
        >
          <div className={styles["barFill"]} style={{ width: `${Math.min(100, ratio * 100)}%` }} />
        </div>
      ) : null}
      {sub || delta?.label ? <div className={styles["sub"]}>{sub ?? delta?.label}</div> : null}
    </div>
  );
}

export interface MetricGroupProps extends HTMLAttributes<HTMLDivElement> {
  /** Tiles share one card with dividers instead of separate cards. */
  readonly joined?: boolean | undefined;
}

export function MetricGroup({ joined, className, children, ...rest }: MetricGroupProps) {
  return (
    <div className={cx(styles["group"], joined && styles["groupJoined"], className)} {...rest}>
      {children}
    </div>
  );
}

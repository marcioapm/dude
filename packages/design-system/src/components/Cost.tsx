import type { HTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import { formatDuration, formatTokens, formatUsd } from "../util/format.ts";
import { Tooltip } from "../primitives/Tooltip.tsx";
import styles from "./Cost.module.css";

export interface CostProps extends Omit<HTMLAttributes<HTMLSpanElement>, "children"> {
  /** Model tokens, in USD. `null`: not reported (shown as "—", never $0.00). */
  readonly tokensUsd: number | null;
  /** Machine time, in USD. Absent or null until it is measured: the cost is then tokens only, and says so. */
  readonly machineUsd?: number | null | undefined;
  /** The token count behind `tokensUsd`, for the tooltip ("412k tokens"). */
  readonly tokens?: number | undefined;
  /** The machine time behind `machineUsd`, in ms, for the tooltip. */
  readonly machineMs?: number | undefined;
  readonly size?: "sm" | "md" | "lg" | undefined;
  readonly tone?: "default" | "secondary" | "muted" | undefined;
}

/**
 * A cost is a total: model tokens plus machine time. The number is the
 * sum; a 2px hairline under it shows the split at a glance (tokens, then
 * machine), and the tooltip gives both parts with their units.
 *
 * Until machine time is measured the total is the tokens alone — the
 * hairline is one colour and the tooltip says machine time is not
 * counted yet. An unreported cost is "—" with a title, never $0.00.
 */
export function Cost({ tokensUsd, machineUsd, tokens, machineMs, size = "md", tone = "default", className, ...rest }: CostProps) {
  if (tokensUsd === null && (machineUsd === null || machineUsd === undefined)) {
    return (
      <span className={cx(styles["unknown"], styles[size], className)} title="Cost not reported" aria-label="Cost not reported" {...rest}>
        —
      </span>
    );
  }
  const tok = tokensUsd ?? 0;
  const machineKnown = machineUsd !== null && machineUsd !== undefined;
  const mach = machineKnown ? machineUsd : 0;
  const total = tok + mach;
  const share = total > 0 ? Math.round((tok / total) * 100) : 100;
  const tip = (
    <span className={styles["tip"]}>
      <span className={styles["tipTotal"]}>{formatUsd(total)} total</span>
      <span>
        Model tokens {tokensUsd === null ? "not reported" : formatUsd(tok)}
        {tokens !== undefined ? ` (${formatTokens(tokens)} tokens)` : ""}
      </span>
      <span>
        {machineKnown
          ? `Machine time ${formatUsd(mach)}${machineMs !== undefined ? ` (${formatDuration(machineMs)})` : ""}`
          : "Machine time not counted yet"}
      </span>
    </span>
  );
  return (
    <Tooltip content={tip}>
      <span
        className={cx(styles["root"], styles[size], tone !== "default" && styles[tone], className)}
        tabIndex={0}
        aria-label={`${formatUsd(total)}: model tokens ${formatUsd(tok)}${machineKnown ? `, machine time ${formatUsd(mach)}` : ""}`}
        data-split={machineKnown ? "both" : "tokens"}
        {...rest}
      >
        <span className={styles["total"]}>{formatUsd(total)}</span>
        <i className={styles["bar"]} aria-hidden>
          <b style={{ width: `${share}%` }} />
        </i>
      </span>
    </Tooltip>
  );
}

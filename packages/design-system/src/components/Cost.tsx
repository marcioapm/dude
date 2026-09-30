import type { HTMLAttributes } from "react";
import type { CostOrigin, CostProvenance } from "@dude/domain";
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
  /**
   * Who priced the tokens: `"lux"` (the runtime's metering) or `"agent"`
   * (the harness's own figure, an estimate). Absent: the tooltip says nothing
   * of where the numbers came from.
   */
  readonly tokensFrom?: CostOrigin | undefined;
  /** Who priced the machine time: `"lux"` or dude's own `"estimate"`. */
  readonly machineFrom?: CostProvenance["machine"] | undefined;
  /** lux has settled its figures (its cost is final). Only read for a part from lux. */
  readonly settled?: boolean | undefined;
}

/** "· reported by lux" for a settled lux figure; every other origin is an estimate. */
function tokensNote(from: CostOrigin | undefined, settled: boolean): string {
  if (from === undefined) return "";
  if (from === "lux") return settled ? " · reported by lux" : " · estimate, lux settling";
  return " · estimate";
}

function machineNote(from: CostProvenance["machine"] | undefined, settled: boolean): string {
  if (from === undefined) return "";
  if (from === "lux") return settled ? " · lux" : " · lux, settling";
  return " · estimated";
}

export type CostWordsInput = Pick<CostProps, "tokensUsd" | "machineUsd" | "tokens" | "machineMs" | "tokensFrom" | "machineFrom" | "settled">;

/**
 * What a known cost says, in words: the tooltip's lines (total, tokens,
 * machine) and the aria-label, built from the same parts so they cannot
 * say different things.
 */
export function costWords({ tokensUsd, machineUsd, tokens, machineMs, tokensFrom, machineFrom, settled = false }: CostWordsInput): {
  total: number; machineKnown: boolean; share: number; lines: [string, string, string]; label: string;
} {
  const tok = tokensUsd ?? 0;
  const machineKnown = machineUsd !== null && machineUsd !== undefined;
  const mach = machineKnown ? machineUsd : 0;
  const total = tok + mach;
  // The tokens' percentage of the total, for the hairline; all tokens at $0.
  const share = total > 0 ? Math.round((tok / total) * 100) : 100;
  const tokensPart = `model tokens ${tokensUsd === null ? "not reported" : formatUsd(tok)}`
    + (tokens !== undefined ? ` (${formatTokens(tokens)} tokens)` : "")
    + (tokensUsd === null ? "" : tokensNote(tokensFrom, settled));
  const machinePart = machineKnown
    ? `machine time ${formatUsd(mach)}${machineMs !== undefined ? ` (${formatDuration(machineMs)})` : ""}${machineNote(machineFrom, settled)}`
    : null;
  const line = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);
  return {
    total,
    machineKnown,
    share,
    lines: [`${formatUsd(total)} total`, line(tokensPart), machinePart ? line(machinePart) : "Machine time not counted yet"],
    label: `${formatUsd(total)}: ${tokensPart}${machinePart ? `, ${machinePart}` : ""}`,
  };
}

/**
 * A cost is a total: model tokens plus machine time. The number is the
 * sum; a 2px hairline under it shows the split at a glance (tokens, then
 * machine), and the tooltip gives both parts with their units.
 *
 * Until machine time is measured the total is the tokens alone — the
 * hairline is one colour and the tooltip says machine time is not
 * counted yet. An unreported cost is "—" with a title, never $0.00.
 *
 * With `tokensFrom` / `machineFrom` each part also says where it came
 * from and whether it is settled: only a lux figure lux has made final is
 * "reported"; the rest are estimates and say so.
 */
export function Cost({ tokensUsd, machineUsd, tokens, machineMs, size = "md", tone = "default",
  tokensFrom, machineFrom, settled = false, className, ...rest }: CostProps) {
  const words = costWords({ tokensUsd, machineUsd, tokens, machineMs, tokensFrom, machineFrom, settled });
  if (tokensUsd === null && !words.machineKnown) {
    return (
      <span className={cx(styles["unknown"], styles[size], className)} title="Cost not reported" aria-label="Cost not reported" {...rest}>
        —
      </span>
    );
  }
  const { total, machineKnown, share } = words;
  const [totalLine, tokensLine, machineLine] = words.lines;
  const tip = (
    <span className={styles["tip"]}>
      <span className={styles["tipTotal"]}>{totalLine}</span>
      <span>{tokensLine}</span>
      <span>{machineLine}</span>
    </span>
  );
  return (
    <Tooltip content={tip}>
      <span
        className={cx(styles["root"], styles[size], tone !== "default" && styles[tone], className)}
        tabIndex={0}
        aria-label={words.label}
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

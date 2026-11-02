/**
 * Who decides the next attempt, inside a dialog that starts one: two
 * choices, each with its driver's glyph; the one that decided the current
 * attempt says "as now" and is the default. To change who decides the
 * attempt running now, use `DriverSwitch`.
 */

import { useId } from "react";
import { DriverIcons } from "./DriverMark.tsx";
import { Segmented } from "./ScreenHeader.tsx";
import { cx } from "../util/cx.ts";
import styles from "./DeciderField.module.css";

export type NextDecider = "conductor" | "deliver";

export interface DeciderFieldProps {
  /** The attempt it starts: N+1. */
  readonly attempt: number;
  readonly value: NextDecider;
  readonly onChange: (v: NextDecider) => void;
  /** Attempt N's: marked "as now", the default. */
  readonly previous: NextDecider;
  /**
   * Its person accepted Deliver's defaults: Deliver decides the next
   * attempt itself, with no quiet conductor (true), as the orchestrator
   * keeps it.
   */
  readonly accepted?: boolean | undefined;
  readonly className?: string | undefined;
}

const NAME: Record<NextDecider, string> = { conductor: "The conductor", deliver: "Deliver" };

function hint(v: NextDecider, attempt: number, accepted: boolean): string {
  if (v === "conductor") {
    return `A new conductor, briefed with your note and what attempt ${attempt - 1} did. Each finished step comes back to it, in Chat, with you.`;
  }
  return accepted
    ? `Deliver decides attempt ${attempt} to the pull request on its own, as you accepted its defaults.`
    : `A quiet conductor takes Deliver's defaults through attempt ${attempt} to the pull request; it speaks in Chat only when it needs you.`;
}

export function DeciderField({ attempt, value, onChange, previous, accepted = false, className }: DeciderFieldProps) {
  const hintId = useId();
  const label = `Who decides attempt ${attempt}`;
  return (
    <div className={cx(styles["root"], className)} data-testid="decider-field" data-value={value}>
      <span className={styles["label"]} aria-hidden>{label}</span>
      <Segmented<NextDecider> label={label} value={value} onChange={onChange} aria-describedby={hintId}
        options={(["conductor", "deliver"] as const).map((v) => ({
          value: v,
          label: (
            <span className={styles["option"]}>
              <DriverIcons mode={v} size={12} />
              {NAME[v]}
              {v === previous ? <span className={styles["asNow"]}>as now</span> : null}
            </span>
          ),
        }))} />
      <span id={hintId} className={styles["hint"]}>{hint(value, attempt, accepted)}</span>
    </div>
  );
}

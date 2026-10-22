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
  readonly className?: string | undefined;
}

const NAME: Record<NextDecider, string> = { conductor: "The conductor", deliver: "Deliver" };

function hint(v: NextDecider, attempt: number): string {
  return v === "conductor"
    ? `A new conductor, briefed with your note and what attempt ${attempt - 1} did. Each finished step comes back to it, in Chat, with you.`
    : `Deliver runs attempt ${attempt} to the pull request on its own; a message in Chat still reaches a conductor, who decides nothing.`;
}

export function DeciderField({ attempt, value, onChange, previous, className }: DeciderFieldProps) {
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
      <span id={hintId} className={styles["hint"]}>{hint(value, attempt)}</span>
    </div>
  );
}

/**
 * Where one attempt ends and the next begins in a task's Chat: a rule,
 * "Attempt 2 · started by Ana · 1m ago", and the note under it. The
 * earlier attempt's turns sit above it inside `EarlierTurns`, muted but
 * whole; nothing in them can be answered.
 */

import type { ReactNode } from "react";
import { formatDuration } from "../util/format.ts";
import styles from "./AttemptDivider.module.css";

export interface AttemptDividerProps {
  readonly attempt: number;
  readonly by: string;
  readonly note?: string | undefined;
  /** When it started (epoch ms). */
  readonly at: number;
}

export function AttemptDivider({ attempt, by, note, at }: AttemptDividerProps) {
  return (
    <div className={styles["divider"]} role="separator" aria-label={`Attempt ${attempt}, started by ${by}`} data-testid="attempt-divider">
      <div className={styles["line"]}>
        <span className={styles["rule"]} aria-hidden />
        <span className={styles["words"]}>
          <span className={styles["attempt"]}>Attempt {attempt}</span>
          {" · "}started by {by}{" · "}
          <time dateTime={new Date(at).toISOString()}>{formatDuration(Math.max(0, Date.now() - at), { style: "age" })} ago</time>
        </span>
        <span className={styles["rule"]} aria-hidden />
      </div>
      {note ? <q className={styles["note"]}>{note}</q> : null}
    </div>
  );
}

export interface EarlierTurnsProps {
  /** The attempt these turns belong to. */
  readonly attempt: number;
  readonly children: ReactNode;
}

export function EarlierTurns({ attempt, children }: EarlierTurnsProps) {
  return (
    <div className={styles["earlier"]} role="group" aria-label={`Attempt ${attempt}'s conversation`} data-testid="earlier-turns" inert>
      {children}
    </div>
  );
}

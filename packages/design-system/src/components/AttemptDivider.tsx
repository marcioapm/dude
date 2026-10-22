/**
 * Where one attempt ends and the next begins in a task's Chat: a rule,
 * "Attempt 2 · started by Ana · 1m ago", and the note under it. The
 * earlier attempt's turns sit above it inside `EarlierTurns`, muted but
 * whole and readable; nothing in them can be answered or opened.
 */

import type { ReactNode } from "react";
import { Duration } from "./Numbers.tsx";
import { EarlierProvider } from "./earlier.ts";
import styles from "./AttemptDivider.module.css";

export interface AttemptDividerProps {
  readonly attempt: number;
  readonly by: string;
  readonly note?: string | undefined;
  /** When it started (epoch ms). */
  readonly at: number;
}

/**
 * The separator semantics sit on the rules alone: the words and the note
 * are ordinary text, so assistive tech reads them whole.
 */
export function AttemptDivider({ attempt, by, note, at }: AttemptDividerProps) {
  const name = `Attempt ${attempt}, started by ${by}`;
  return (
    <div className={styles["divider"]} data-testid="attempt-divider">
      <div className={styles["line"]}>
        <hr className={styles["rule"]} aria-label={name} />
        <span className={styles["words"]}>
          <span className={styles["attempt"]}>Attempt {attempt}</span>
          {" · "}started by {by}{" · "}
          <time dateTime={new Date(at).toISOString()}><Duration ms={Math.max(0, Date.now() - at)} format="age" tone="muted" /> ago</time>
        </span>
        <hr className={styles["rule"]} aria-hidden />
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

/**
 * An earlier attempt's turns: a named group, muted, its text reachable and
 * selectable. What renders inside reads `useEarlier` and leaves its
 * actions out (a `ChatRunLine` opens nothing, a `QuestionCard` takes no
 * answer).
 */
export function EarlierTurns({ attempt, children }: EarlierTurnsProps) {
  return (
    <div className={styles["earlier"]} role="group" aria-label={`Attempt ${attempt}'s conversation`} data-testid="earlier-turns">
      <EarlierProvider value>{children}</EarlierProvider>
    </div>
  );
}

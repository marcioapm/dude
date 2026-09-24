import type { HTMLAttributes } from "react";
import type { AgentRole } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { AgentAvatar, ROLE_LABEL } from "./AgentAvatar.tsx";
import { Duration } from "./Numbers.tsx";
import styles from "./ChatProgress.module.css";

export interface ChatProgressProps extends Omit<HTMLAttributes<HTMLDivElement>, "children"> {
  /** Units finished; null when the agent did not say. */
  readonly done: number | null;
  /** Units in total; null when unknown — the bar is then indeterminate. */
  readonly of: number | null;
  /** What it is on now: "Running integration tests". */
  readonly step: string | null;
  /** When this update was recorded. */
  readonly at: string | number | Date;
  /** When the progress began; the row shows the elapsed time from here. */
  readonly startedAt: string | number | Date;
  readonly role: AgentRole;
  /** Frozen: finished (or stopped) where it got to; nothing moves. */
  readonly ended?: boolean | undefined;
  /** Accessible name; defaults to the step, then "Progress". */
  readonly label?: string | undefined;
}

/** The fraction a determinate bar fills, clamped to [0, 1]; null when indeterminate. */
export function progressFraction(done: number | null, of: number | null): number | null {
  if (done === null || of === null || !Number.isFinite(done) || !Number.isFinite(of) || of <= 0) return null;
  return Math.min(1, Math.max(0, done / of));
}

/**
 * A progress row that updates in place — the agent's own "3 of 10,
 * running integration tests". Same 24px muted line as a ChatEvent, with a
 * 2px bar under it: determinate when `done` and `of` are known, a sweep
 * otherwise. While running the determinate fill breathes and the sweep
 * moves; `ended` freezes both where they got to, so a finished or stopped
 * bar never reads as still going. Reduced motion stills the loops.
 */
export function ChatProgress({ done, of, step, at, startedAt, role, ended, label, className, ...rest }: ChatProgressProps) {
  const fraction = progressFraction(done, of);
  const determinate = fraction !== null;
  const running = !ended;
  const complete = determinate && fraction >= 1;
  const count = determinate ? `${done} of ${of}` : done !== null ? `${done}` : null;
  const name = label ?? step ?? "Progress";
  const text = ended ? (complete ? "Finished" : "Stopped") : null;
  return (
    <div className={cx(styles["root"], running && styles["running"], ended && styles["ended"], complete && styles["complete"], className)} data-progress={ended ? "ended" : "running"} {...rest}>
      <div className={styles["row"]}>
        <span className={styles["glyph"]} aria-hidden>
          <Icon name={complete ? "check" : ended ? "stop" : "clock"} size={12} />
        </span>
        {count ? <span className={styles["count"]}>{count}</span> : null}
        {step ? (
          <span className={styles["step"]} title={step}>
            {step}
          </span>
        ) : (
          <span className={styles["spacer"]} />
        )}
        <span className={styles["trailing"]}>
          {text ? <span className={styles["state"]}>{text}</span> : null}
          <span className={styles["who"]} title={`Reported by the ${ROLE_LABEL[role].toLowerCase()}`}>
            <AgentAvatar role={role} size="xs" />
          </span>
          <Duration since={startedAt} until={ended ? at : null} live={running} tone="muted" className={styles["elapsed"]} />
        </span>
      </div>
      <div
        className={styles["track"]}
        role="progressbar"
        aria-label={name}
        aria-valuemin={determinate ? 0 : undefined}
        aria-valuemax={determinate ? of ?? undefined : undefined}
        aria-valuenow={determinate ? done ?? undefined : undefined}
        aria-valuetext={determinate ? `${count}${step ? `, ${step}` : ""}` : step ?? undefined}
        aria-busy={running || undefined}
      >
        {determinate ? <span className={styles["fill"]} style={{ width: `${fraction * 100}%` }} /> : <span className={styles["sweep"]} />}
      </div>
    </div>
  );
}

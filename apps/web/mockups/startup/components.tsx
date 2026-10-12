/*
 * What the proposal adds to the design system, built here to its rules:
 * tokens only, both themes, both densities, accessible names, reduced
 * motion. docs/design/agent-startup.md lists each with its props and the
 * README text it would add.
 *
 * - StartProgress: the row where a starting agent's next turn will be. One
 *   line (what it waits on, for how long), a button that opens the stages
 *   as a vertical stepper; a calm note past a threshold; a failure as the
 *   transcript's ended turn already is (a danger Callout), naming the stage.
 * - StartChip: the same state in a header, one line, no stepper.
 *
 * Both take steps in words, not lux's stage names: the app maps lux's
 * stage to words through one map shared with branch previews.
 */

import { useEffect, useId, useState, type ReactNode } from "react";
import { formatDuration, Icon } from "@dude/design-system";
import { Duration, StatusMark } from "@dude/design-system/components";
import { Button, Callout } from "@dude/design-system/primitives";
import s from "./startup.module.css";

export type StartStepState = "done" | "current" | "pending" | "failed";

export interface StartStep {
  readonly id: string;
  /** While current or pending: "Checking out repositories". */
  readonly label: string;
  /** Once passed: "Repositories checked out". */
  readonly doneLabel: string;
  readonly state: StartStepState;
  /** Done: how long it took. */
  readonly tookMs?: number | undefined;
  /** Current: since when (lux's stageSince). */
  readonly since?: number | undefined;
}

export interface StartProgressProps {
  /** "Starting" for a fresh agent, "Resuming" for a parked one taken back up. */
  readonly verb: string;
  readonly steps: readonly StartStep[];
  /** When this start began; the total clock counts from it. */
  readonly startedAt: number;
  /** The agent's face, in the turn's gutter: where its first words will appear. */
  readonly avatar?: ReactNode;
  /** A calm line under the row once the current stage is slow. Never a tone. */
  readonly slowNote?: ReactNode;
  /** It did not start: lux's reason (may be null) and a way to try again. */
  readonly failure?: { readonly reason: string | null; readonly onRetry?: (() => void) | undefined } | undefined;
  readonly defaultOpen?: boolean | undefined;
  /** Gallery and screenshots only: open it from outside. */
  readonly open?: boolean | undefined;
  readonly onOpenChange?: ((open: boolean) => void) | undefined;
  readonly "data-testid"?: string | undefined;
}

/** "while getting the image ready", from a step's label: the failure line names where. */
function whileWords(label: string): string {
  return `while ${label.charAt(0).toLowerCase()}${label.slice(1)}`;
}

/**
 * A starting agent, where its next turn will be: what it waits on now and
 * for how long, the total, and (open) every stage. The stage change, not
 * the clock, is announced: one polite line per stage.
 */
export function StartProgress({ verb, steps, startedAt, avatar, slowNote, failure, defaultOpen, open: openProp, onOpenChange, ...rest }: StartProgressProps) {
  const [openState, setOpenState] = useState(defaultOpen ?? false);
  const open = openProp ?? openState;
  const setOpen = (o: boolean) => { setOpenState(o); onOpenChange?.(o); };
  const listId = useId();
  const current = steps.find((st) => st.state === "current" || st.state === "failed");
  const failed = current?.state === "failed";
  // A failure is a danger Callout (role=alert), announced by itself.
  const announce = current && !failed ? `${verb}: ${current.label}.` : "";
  const stepper = (
        <ol id={listId} className={s.steps} hidden={!open} aria-label="Stages">
          {steps.map((st) => (
            <li key={st.id} className={s.step} data-state={st.state} aria-current={st.state === "current" || st.state === "failed" ? "step" : undefined}>
              <span className={s.stepGlyph} aria-hidden>
                <Icon name={st.state === "done" ? "check" : st.state === "failed" ? "cross" : st.state === "current" ? "circle-half" : "circle-dotted"} size={12} strokeWidth={2} />
              </span>
              <span className={s.stepLabel}>
                {st.state === "done" ? st.doneLabel : st.label}
                <span className="ds-sr-only">{st.state === "pending" ? ", to come" : st.state === "current" ? ", now" : st.state === "failed" ? ", failed" : ""}</span>
              </span>
              <span className={s.stepTime}>
                {st.state === "done" && st.tookMs !== undefined ? <Duration ms={st.tookMs} tone="muted" />
                  : st.state === "current" && st.since !== undefined ? <Clock since={st.since} />
                  : st.state === "failed" ? <span className={s.stepFailed}>failed</span> : null}
              </span>
            </li>
          ))}
        </ol>
  );
  const toggle = (label: ReactNode, quiet = false) => (
    <button type="button" className={quiet ? s.toggleQuiet : s.head} aria-expanded={open} aria-controls={listId} onClick={() => setOpen(!open)} data-testid="start-toggle">
      {label}
      <span className={s.chev} aria-hidden><Icon name="chevron-down" size={14} /></span>
      {quiet ? null : <span className="ds-sr-only">{open ? ", hide the stages" : ", show the stages"}</span>}
    </button>
  );

  if (failed && current) {
    // The transcript's own failure (the ended turn's danger Callout), naming the stage and lux's reason.
    // The stages stay folded under it: the Callout says where; the list is for whoever wants the times.
    const didNot = verb === "Resuming" ? "Did not resume" : "Did not start";
    return (
      <section className={s.failedRow} aria-label={didNot} data-testid={rest["data-testid"]}>
        <Callout tone="danger">
          <span className={s.failedLine}>
            <span>
              <b>{didNot}</b>: failed {whileWords(current.label)}{failure?.reason ? <>: <code className={s.reason}>{failure.reason}</code>.</> : "."}
              {" "}Nothing was spent on the model.
            </span>
            {failure?.onRetry ? (
              <Button size="sm" variant="secondary" leadingIcon="retry" onClick={failure.onRetry} data-testid="start-retry">Try again</Button>
            ) : null}
          </span>
        </Callout>
        <div className={s.failedStages}>
          {toggle(open ? "Hide the stages" : "Show the stages", true)}
          {stepper}
        </div>
      </section>
    );
  }

  return (
    <section className={s.row} aria-label={`${verb} the agent`} data-testid={rest["data-testid"]}>
      <div className={s.gutter}>{avatar}</div>
      <div className={s.column}>
        <div className={s.headLine}>
          {toggle(
            <>
              <StatusMark status="starting" size="sm" label={verb} />
              {current ? <span className={s.now}>{current.label}</span> : null}
              {current?.since !== undefined ? (
                <span className={s.clock}><Clock since={current.since} /><span className="ds-sr-only"> in this stage</span></span>
              ) : null}
              {/* The total only once it differs from the stage's own clock. */}
              {steps.some((st) => st.state === "done") ? (
                <span className={s.total}>
                  <span aria-hidden>·</span> <Clock since={startedAt} tone="muted" /> in all
                </span>
              ) : null}
            </>,
          )}
        </div>
        {slowNote ? (
          <div className={s.slow} data-testid="start-slow"><Icon name="clock" size={12} className={s.slowGlyph} /><span>{slowNote}</span></div>
        ) : null}
        {stepper}
        {/* Announced once per stage: the words change, the clock never reaches it. */}
        <span className="ds-sr-only" role="status" aria-live="polite" aria-atomic="true">{announce}</span>
      </div>
    </section>
  );
}

export interface StartChipProps {
  readonly verb: string;
  readonly step: StartStep;
  /** The status is already said beside it (a Run's header): words and clock only. */
  readonly bare?: boolean | undefined;
  readonly "data-testid"?: string | undefined;
}

/** The same state in a header: "Starting · Checking out repositories · 00:18". */
export function StartChip({ verb, step, bare, ...rest }: StartChipProps) {
  const failed = step.state === "failed";
  return (
    <span className={s.chip} data-failed={failed || undefined} data-testid={rest["data-testid"]}>
      {bare ? null : failed ? <StatusMark status="failed" size="sm" label={verb === "Resuming" ? "Did not resume" : "Did not start"} />
        : <StatusMark status="starting" size="sm" label={verb} />}
      {bare ? null : <span aria-hidden className={s.chipDot}>·</span>}
      <span className={s.chipWords}>{failed ? `Failed ${whileWords(step.label)}` : step.label}</span>
      {!failed && step.since !== undefined ? <><span aria-hidden className={s.chipDot}>·</span><Clock since={step.since} tone="secondary" /></> : null}
    </span>
  );
}

/** Ticks a clock for a scenario that plays out (scenario 1). */
export function useNow(every = 250): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), every);
    return () => clearInterval(id);
  }, [every]);
  return now;
}

/**
 * An elapsed clock ("00:12") that ticks once a second, with no live dot of
 * its own: the starting mark beside it is the one live thing on the line.
 * `Duration` would add its dot to every clock (three on one line).
 * role="timer" is not announced as it ticks.
 */
function Clock({ since, tone }: { since: number; tone?: "muted" | "secondary" | undefined }) {
  const now = useNow(1000);
  return (
    <span role="timer" className={cx(s.tick, tone === "muted" && s.tickMuted, tone === "secondary" && s.tickSecondary)}>
      {formatDuration(Math.max(0, now - since), { style: "clock" })}
    </span>
  );
}

function cx(...names: Array<string | false | undefined>): string {
  return names.filter(Boolean).join(" ");
}

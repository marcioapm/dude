import { useId, useState, type ReactNode } from "react";
import { FRESH_START_STAGES, START_STAGES, START_STAGE_WORDS, type StartStage } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { Button } from "../primitives/Button.tsx";
import { Callout } from "../primitives/Layout.tsx";
import { Duration } from "./Numbers.tsx";
import { StatusMark } from "./StatusMark.tsx";
import styles from "./StartProgress.module.css";

export type StartStepState = "done" | "current" | "pending" | "failed";

/** One stage of a start, in words. */
export interface StartStep {
  readonly id: StartStage;
  /** While current or pending: "Checking out repositories". */
  readonly label: string;
  /** Once passed: "Repositories checked out". */
  readonly doneLabel: string;
  readonly state: StartStepState;
  /** Done: how long it took. */
  readonly tookMs?: number | undefined;
  /** Current (or failed): since when, lux's `stageSince`. */
  readonly since?: string | number | Date | undefined;
}

/** What kind of start it is: names the default verb and the failure's words. */
export type StartKind = "start" | "resume" | "move";

export interface StartStepsInput {
  /** The current stage; null when lux reports none (an older lux). */
  readonly stage: StartStage | null;
  readonly since?: string | number | Date | null | undefined;
  /** Stages passed in this start, with how long each took when known. */
  readonly done?: ReadonlyArray<{ readonly stage: StartStage; readonly tookMs?: number | undefined }> | undefined;
  /** It failed in `stage`. */
  readonly failed?: boolean | undefined;
  /** The stages this kind of start has, in order; the fresh-start list by default. */
  readonly stages?: readonly StartStage[] | undefined;
  /** The pool it waits in: `waiting` reads "Waiting for a machine in <pool>" while current or pending. */
  readonly pool?: string | null | undefined;
}

/**
 * The steps of a start from the shared words and a stage list. A stage
 * lux reports that the list leaves out (a resume that pulled its image
 * after all) is drawn in its place in lux's order; stages that never
 * occur are not drawn.
 */
export function startSteps({ stage, since, done = [], failed, stages = FRESH_START_STAGES, pool }: StartStepsInput): StartStep[] {
  const present = new Set<StartStage>(stages);
  if (stage) present.add(stage);
  for (const d of done) present.add(d.stage);
  const order = START_STAGES.filter((s) => present.has(s));
  const at = stage ? order.indexOf(stage) : -1;
  return order.map((id, i) => {
    const words = START_STAGE_WORDS[id];
    const label = id === "waiting" && pool ? `${words.now} in ${pool}` : words.now;
    const state: StartStepState = at < 0 ? "pending" : i < at ? "done" : i === at ? (failed ? "failed" : "current") : "pending";
    const tookMs = state === "done" ? done.find((d) => d.stage === id)?.tookMs : undefined;
    return {
      id, label, doneLabel: words.done, state,
      ...(tookMs !== undefined ? { tookMs } : {}),
      ...(i === at && since !== null && since !== undefined ? { since } : {}),
    };
  });
}

/** "Starting" for a fresh agent, "Resuming" for one taken back up; a move names its own ("Moving to Large"). */
export function startVerb(kind: StartKind = "start"): string {
  return kind === "start" ? "Starting" : "Resuming";
}

function didNot(kind: StartKind): string {
  return kind === "start" ? "Did not start" : "Did not resume";
}

/** "while getting the image ready", from a step's label: the failure names where. */
function whileWords(label: string): string {
  return `while ${label.charAt(0).toLowerCase()}${label.slice(1)}`;
}

function Clock({ since, tone }: { since: string | number | Date; tone?: "muted" | "secondary" | undefined }) {
  // No live dot: the starting mark beside it is the line's one live thing; role=timer is not announced as it ticks.
  return <Duration since={since} format="clock" dot={false} role="timer" tone={tone ?? "default"} className={styles["tick"]} />;
}

export interface StartProgressProps {
  /** Which start it is; names the default verb. */
  readonly kind?: StartKind | undefined;
  /** "Starting", "Resuming", "Moving to Large", "Moving to another machine"; also names the region. */
  readonly verb?: string | undefined;
  readonly steps: readonly StartStep[];
  /** When this start began: the total clock counts from it. */
  readonly startedAt?: string | number | Date | undefined;
  /** The agent's face, in the turn's gutter. */
  readonly avatar?: ReactNode;
  /** A calm line once the current stage is slow; the app decides when. Never a tone. */
  readonly slowNote?: ReactNode;
  /** lux's own reason under the slow line, in muted mono. */
  readonly slowReason?: string | null | undefined;
  /**
   * It did not start: the reason (may be null) and Try again. The reason is the runtime's raw words, in mono,
   * unless `plain`: a sentence of the app's own, in body text.
   */
  readonly failure?: {
    readonly reason: string | null; readonly plain?: boolean | undefined; readonly onRetry?: (() => void) | undefined;
  } | undefined;
  readonly defaultOpen?: boolean | undefined;
  readonly open?: boolean | undefined;
  readonly onOpenChange?: ((open: boolean) => void) | undefined;
  readonly "data-testid"?: string | undefined;
}

/**
 * A starting agent, where its next turn will be: the stage it waits on and
 * for how long, the total, and (opened) every stage as a stepper. Each
 * stage change is announced once; the clocks are timers outside the live
 * region. A failure is the transcript's danger Callout naming the stage.
 */
export function StartProgress({ kind = "start", verb: verbProp, steps, startedAt, avatar, slowNote, slowReason, failure, defaultOpen,
  open: openProp, onOpenChange, ...rest }: StartProgressProps) {
  const verb = verbProp ?? startVerb(kind);
  const [openState, setOpenState] = useState(defaultOpen ?? false);
  const open = openProp ?? openState;
  const setOpen = (o: boolean) => {
    setOpenState(o);
    onOpenChange?.(o);
  };
  const listId = useId();
  const current = steps.find((st) => st.state === "current" || st.state === "failed");
  const failed = current?.state === "failed" || (failure !== undefined && !current);
  const anyDone = steps.some((st) => st.state === "done");
  const announce = failed ? "" : current ? `${verb}: ${current.label}.` : `${verb}.`;

  const stepper = (
    <ol id={listId} className={styles["steps"]} hidden={!open} aria-label="Stages">
      {steps.map((st) => (
        <li key={st.id} className={styles["step"]} data-state={st.state} aria-current={st.state === "current" || st.state === "failed" ? "step" : undefined}>
          <span className={styles["stepGlyph"]} aria-hidden>
            <Icon name={st.state === "done" ? "check" : st.state === "failed" ? "cross" : st.state === "current" ? "circle-half" : "circle-dotted"} size={12} strokeWidth={2} />
          </span>
          <span className={styles["stepLabel"]}>
            {st.state === "done" ? st.doneLabel : st.label}
            <span className="ds-sr-only">{st.state === "pending" ? ", to come" : st.state === "current" ? ", now" : st.state === "failed" ? ", failed" : ""}</span>
          </span>
          <span className={styles["stepTime"]}>
            {st.state === "done" && st.tookMs !== undefined ? <Duration ms={st.tookMs} tone="muted" />
              : st.state === "current" && st.since !== undefined ? <Clock since={st.since} />
              : st.state === "failed" ? <span className={styles["stepFailed"]}>failed</span> : null}
          </span>
        </li>
      ))}
    </ol>
  );
  const toggle = (label: ReactNode, quiet = false) => (
    <button type="button" className={quiet ? styles["toggleQuiet"] : styles["head"]} aria-expanded={open} aria-controls={listId}
      onClick={() => setOpen(!open)} data-testid="start-toggle">
      {label}
      <span className={styles["chev"]} aria-hidden><Icon name="chevron-down" size={14} /></span>
      {quiet ? null : <span className="ds-sr-only">{open ? ", hide the stages" : ", show the stages"}</span>}
    </button>
  );

  if (failed) {
    const reason = failure?.reason?.trim() || null;
    // "Did not start: failed while <stage>: <reason>." or, with no stage, "Did not start: <reason>."
    const said = reason ? (failure?.plain ? reason : <code className={styles["reason"]}>{reason}</code>) : null;
    const lead = current ? <>: failed {whileWords(current.label)}{said ? ": " : ""}</> : said ? ": " : "";
    const stop = reason && /[.!?]$/.test(reason) ? "" : ".";
    return (
      <section className={styles["failedRow"]} aria-label={didNot(kind)} data-testid={rest["data-testid"]} data-state="failed">
        <Callout tone="danger">
          <span className={styles["failedLine"]}>
            <span data-testid="start-failed">
              <b>{didNot(kind)}</b>{lead}{said}{stop}
              {" "}Nothing was spent on the model.
            </span>
            {failure?.onRetry ? (
              <Button size="sm" variant="secondary" leadingIcon="retry" onClick={failure.onRetry} data-testid="start-retry">Try again</Button>
            ) : null}
          </span>
        </Callout>
        {current ? (
          <div className={styles["failedStages"]}>
            {toggle(open ? "Hide the stages" : "Show the stages", true)}
            {stepper}
          </div>
        ) : null}
      </section>
    );
  }

  const line = (
    <>
      <StatusMark status="starting" size="sm" label={verb} />
      {current ? <span className={styles["now"]}>{current.label}</span> : null}
      {current?.since !== undefined ? (
        <span className={styles["clock"]}><Clock since={current.since} /><span className="ds-sr-only"> in this stage</span></span>
      ) : null}
      {/* The total once it differs from the stage's own clock, or alone when lux names no stage. */}
      {startedAt !== undefined && (anyDone || !current) ? (
        <span className={styles["total"]}>
          {current ? <span aria-hidden>· </span> : null}<Clock since={startedAt} tone="muted" />{current ? " in all" : null}
        </span>
      ) : null}
    </>
  );

  return (
    <section className={styles["row"]} aria-label={`${verb} the agent`} data-testid={rest["data-testid"]} data-state="starting"
      data-stage={current?.id}>
      <div className={styles["gutter"]}>{avatar}</div>
      <div className={styles["column"]}>
        <div className={styles["headLine"]}>
          {current ? toggle(line) : <span className={cx(styles["head"], styles["headStatic"])}>{line}</span>}
        </div>
        {slowNote ? (
          <div className={styles["slow"]} data-testid="start-slow">
            <Icon name="clock" size={12} className={styles["slowGlyph"]} />
            <span>
              {slowNote}
              {slowReason ? <span className={styles["slowDetail"]} data-testid="start-slow-reason">lux: {slowReason}</span> : null}
            </span>
          </div>
        ) : null}
        {current ? stepper : null}
        <span className="ds-sr-only" role="status" aria-live="polite" aria-atomic="true" data-testid="start-announce">{announce}</span>
      </div>
    </section>
  );
}

export interface StartChipProps {
  readonly kind?: StartKind | undefined;
  readonly verb?: string | undefined;
  /** The current (or failed) step; none from an older lux. */
  readonly step?: StartStep | null | undefined;
  /** With no step: the total clock's start. */
  readonly startedAt?: string | number | Date | undefined;
  /** The status is already said beside it (a Run's header): words and clock only. */
  readonly bare?: boolean | undefined;
  readonly "data-testid"?: string | undefined;
}

/** The same state in a header: "◐ Resuming · Checking out repositories · 00:12". */
export function StartChip({ kind = "start", verb: verbProp, step, startedAt, bare, ...rest }: StartChipProps) {
  const verb = verbProp ?? startVerb(kind);
  const failed = step?.state === "failed";
  const since = step ? step.since : startedAt;
  const parts: ReactNode[] = [];
  if (step) parts.push(<span key="w" className={styles["chipWords"]}>{failed ? `Failed ${whileWords(step.label)}` : step.label}</span>);
  if (!failed && since !== undefined) parts.push(<Clock key="c" since={since} tone="secondary" />);
  return (
    <span className={styles["chip"]} data-failed={failed || undefined} data-testid={rest["data-testid"]}>
      {bare ? null : failed ? <StatusMark status="failed" size="sm" label={didNot(kind)} /> : <StatusMark status="starting" size="sm" label={verb} />}
      {parts.map((p, i) => (bare && i === 0 ? p : <span key={i} className={styles["chipPart"]}><span aria-hidden className={styles["chipDot"]}>·</span>{p}</span>))}
    </span>
  );
}

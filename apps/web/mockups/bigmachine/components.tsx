/*
 * What the proposal adds to the design system, built here to its rules:
 * tokens only, both themes, both densities, accessible names. Each reuses
 * the stylesheet of the component it extends (MachineChip, ServersSummary)
 * rather than copying it; docs/design/agent-machine-request.md lists the
 * props and the README text each would add.
 *
 * - MachineRequestCall: `request_machine` as a ToolCallCard, composed, not new.
 * - RaisedMachineChip: MachineChip with `raised` ("raised for this task").
 * - MachineRaises: the task's raised machines, in ServersSummary's grammar,
 *   each with Reset.
 * - ResetMachineConfirm: Reset's confirmation, a `Dialog size="sm"`.
 * - StartProgress: a stand-in for the design system's, given the verb
 *   "Moving to Large" and the move's stage list.
 */

import { useEffect, useId, useState, type ReactNode } from "react";
import { formatDuration, Icon } from "@dude/design-system";
import { AgentAvatar, StatusMark, TextButton, ToolCallCard } from "@dude/design-system/components";
import { Button, Dialog, DialogClose, Tooltip } from "@dude/design-system/primitives";
import mc from "../../../../packages/design-system/src/components/Machines.module.css";
import ss from "../../../../packages/design-system/src/components/ServersSummary.module.css";
import { STAGE_WORDS, type Size, type StageId } from "./data.ts";
import s from "./bigmachine.module.css";

type Role = "implementer" | "brainstorm" | "qa_browser" | "simplifier";

const cx = (...names: Array<string | false | null | undefined>) => names.filter(Boolean).join(" ");

// ---- MachineRequestCall ----------------------------------------------------

/**
 * `request_machine` in a transcript: ToolCallCard with the chip glyph and
 * the change as its summary ("Asked for a larger machine: Standard → Large"),
 * the arguments and the tool's answer when open. A refusal is a failed call,
 * open, its first line in the row.
 */
export function MachineRequestCall({ from, to, reason, answer, refused, fresh, startedAt, durationMs, defaultExpanded }: {
  readonly from: Size;
  /** The size asked for; a name alone when it is not one of the organisation's. */
  readonly to: Size | string;
  readonly reason: string;
  readonly answer: string;
  readonly refused?: boolean | undefined;
  readonly fresh?: boolean | undefined;
  readonly startedAt?: number | undefined;
  readonly durationMs?: number | undefined;
  readonly defaultExpanded?: boolean | undefined;
}) {
  const name = typeof to === "string" ? to : to.name;
  const pool = typeof to !== "string" && from.poolId !== to.poolId ? to.pool : null;
  return (
    <ToolCallCard name="request_machine" icon="chip" status={refused ? "failed" : "completed"} data-testid="machine-call"
      summary={<>Asked for a larger machine: {from.name} → <b>{name}</b>
        {pool ? <span className={s.callPool}> · {pool} pool</span> : null}</>}
      args={{ size: name, reason, ...(fresh ? { fresh: true } : {}) }}
      {...(refused ? { error: answer } : { result: answer })}
      startedAt={startedAt} endedAt={startedAt !== undefined && durationMs !== undefined ? startedAt + durationMs : undefined}
      defaultExpanded={defaultExpanded} />
  );
}

// ---- RaisedMachineChip -----------------------------------------------------

export interface RaisedMachineChipProps {
  readonly size: Size;
  readonly spec: string;
  /** "for this task", "for this session". */
  readonly raised: string;
  readonly tooltip: ReactNode;
}

/**
 * MachineChip with `raised`: after the spec, the arrow-up glyph and
 * "raised for this task" in secondary ink. The tooltip says which Run
 * asked, when and why.
 */
export function RaisedMachineChip({ size, spec, raised, tooltip }: RaisedMachineChipProps) {
  return (
    <Tooltip content={tooltip} side="bottom" keepOnPress>
      <button type="button" className={mc["chip"]} data-testid="run-machine" aria-label={`Machine: ${size.name}, ${spec}, raised ${raised}`}>
        <Icon name="chip" size={12} className={mc["chipIcon"]} />
        <span className={cx(mc["chipName"], "ds-cap")}>{size.name}</span>
        <span className={cx(mc["chipSpec"], "ds-cap", "ds-tnum")}>{spec}</span>
        <span className={cx(s.raised, "ds-cap")}><Icon name="arrow-up" size={11} />raised {raised}</span>
      </button>
    </Tooltip>
  );
}

// ---- MachineRaises -----------------------------------------------------------

export interface MachineRaise {
  readonly role: Role;
  readonly label: string;
  readonly size: Size;
  /** "Follows the implementer", or which Run asked, when and why. */
  readonly detail: ReactNode;
  /** Its settings' size, which Reset goes back to. */
  readonly settings: Size;
  readonly onReset?: (() => void) | undefined;
}

/**
 * The machines raised in a task: one row per role, its face,
 * "Implementer runs on Large", which Run asked under it, and Reset.
 * ServersSummary's grammar, on the chrome shade, in the Overview's aside.
 */
export function MachineRaises({ raises, where }: { readonly raises: readonly MachineRaise[]; readonly where: string }) {
  return (
    <aside className={ss["root"]} aria-label="Machines raised" data-testid="machine-raises">
      <div className={ss["head"]}>
        <Icon name="chip" size={16} className={ss["glyph"]} />
        <span className={ss["title"]}>Machines</span>
        <span className={ss["where"]}>{where}</span>
      </div>
      <ul className={s.raiseRows}>
        {raises.map((r) => (
          <li key={r.label} className={s.raiseRow}>
            <AgentAvatar role={r.role} size="sm" />
            <span className={s.raiseText}>
              <span className={s.raiseName}>{r.label} runs on <b>{r.size.name}</b> <span className={s.raiseSpec}>{r.size.cpus} CPUs · {r.size.memoryGiB} GiB</span></span>
              <span className={s.raiseDetail}>{r.detail}</span>
            </span>
            {r.onReset ? (
              <Tooltip content={`Moves the ${r.label.toLowerCase()} back to ${r.settings.name}, its settings' size, now; later Runs run on ${r.settings.name} too.`}>
                <TextButton onClick={r.onReset} aria-label={`Reset the ${r.label.toLowerCase()} to ${r.settings.name}`} data-testid="reset">Reset</TextButton>
              </Tooltip>
            ) : null}
          </li>
        ))}
      </ul>
    </aside>
  );
}

// ---- ResetMachineConfirm -------------------------------------------------------

/**
 * Reset's confirmation: a `Dialog size="sm"` whose description is the one
 * consequence. In the same pool nothing is lost, so the confirm is primary;
 * across pools the running agent is replaced, so the dialog takes the danger
 * tone and the confirm is `danger solid`, as the README asks of a loss.
 */
export function ResetMachineConfirm({ open, onOpenChange, label, from, to, onConfirm }: {
  readonly open: boolean;
  readonly onOpenChange: (open: boolean) => void;
  /** "implementer". */
  readonly label: string;
  readonly from: Size;
  readonly to: Size;
  readonly onConfirm: () => void;
}) {
  const otherPool = from.poolId !== to.poolId;
  return (
    <Dialog open={open} onOpenChange={onOpenChange} size="sm" tone={otherPool ? "danger" : undefined}
      title={`Move the ${label} back to ${to.name} now?`}
      description={otherPool
        ? `${from.name} is in another pool: a fresh ${label} starts from what it pushed; what it has not pushed is lost.`
        : `It keeps its work (same pool). Later ${label} Runs in this task run on ${to.name} too.`}
      footer={
        <>
          <DialogClose asChild><Button variant="quiet">Keep {from.name}</Button></DialogClose>
          <Button data-testid="confirm-reset" {...(otherPool ? { variant: "danger", solid: true } : { variant: "primary" })}
            onClick={() => { onConfirm(); onOpenChange(false); }}>
            {otherPool ? `Replace it on ${to.name}` : `Move it to ${to.name}`}
          </Button>
        </>
      } />
  );
}

// ---- StartProgress ---------------------------------------------------------------
// Stands in for the design system's StartProgress (github/design/agent-startup,
// being built in packages/design-system); the mockup cannot import it yet.
// Same props: a verb and a caller-supplied stage list.

export interface StartStep {
  readonly id: string;
  readonly label: string;
  readonly doneLabel: string;
  readonly state: "done" | "current" | "pending" | "failed";
  readonly tookMs?: number | undefined;
  readonly since?: number | undefined;
}

/**
 * The startup design's row, where the agent's next turn will be: the verb
 * ("Starting", "Moving to Large"), the current stage and its clock, the
 * total once a stage has passed, and the stages as a stepper behind the line.
 */
export function StartProgress({ verb, steps, startedAt, avatar, defaultOpen }: {
  readonly verb: string; readonly steps: readonly StartStep[]; readonly startedAt: number; readonly avatar: ReactNode; readonly defaultOpen?: boolean;
}) {
  const [open, setOpen] = useState(defaultOpen ?? false);
  const listId = useId();
  const current = steps.find((st) => st.state === "current");
  return (
    <section className={s.moveRow} aria-label={verb} data-testid="move-progress">
      <div className={s.moveGutter}>{avatar}</div>
      <div className={s.moveColumn}>
        <button type="button" className={s.moveHead} aria-expanded={open} aria-controls={listId} onClick={() => setOpen(!open)}>
          <StatusMark status="starting" size="sm" label={verb} />
          {current ? <span className={s.moveNow}>{current.label}</span> : null}
          {current?.since !== undefined ? <Clock since={current.since} /> : null}
          {steps.some((st) => st.state === "done") ? (
            <span className={s.moveTotal}><span aria-hidden>·</span> <Clock since={startedAt} muted /> in all</span>
          ) : null}
          <Icon name="chevron-down" size={14} className={s.moveChev} />
          <span className="ds-sr-only">{open ? ", hide the stages" : ", show the stages"}</span>
        </button>
        <ol id={listId} className={s.steps} hidden={!open} aria-label="Stages">
          {steps.map((st) => (
            <li key={st.id} className={s.step} data-state={st.state} aria-current={st.state === "current" ? "step" : undefined}>
              <span className={s.stepGlyph} aria-hidden>
                <Icon name={st.state === "done" ? "check" : st.state === "failed" ? "cross" : st.state === "current" ? "circle-half" : "circle-dotted"} size={12} strokeWidth={2} />
              </span>
              <span className={s.stepLabel}>
                {st.state === "done" ? st.doneLabel : st.label}
                <span className="ds-sr-only">{st.state === "pending" ? ", to come" : st.state === "current" ? ", now" : st.state === "failed" ? ", failed" : ""}</span>
              </span>
              <span className={s.stepTime}>
                {st.state === "done" && st.tookMs !== undefined ? formatDuration(st.tookMs) : st.state === "current" && st.since !== undefined ? <Clock since={st.since} /> : null}
              </span>
            </li>
          ))}
        </ol>
        <span className="ds-sr-only" role="status" aria-live="polite" aria-atomic="true">{current ? `${verb}: ${current.label}.` : ""}</span>
      </div>
    </section>
  );
}

function Clock({ since, muted }: { since: number; muted?: boolean }) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, []);
  return <span role="timer" className={cx(s.tick, muted && s.tickMuted)}>{formatDuration(Math.max(0, now - since), { style: "clock" })}</span>;
}

/**
 * Stand-in for the design system's shared helper: the steps for a list of
 * stage ids, words from STAGE_WORDS, `done` of them finished (with their
 * times) and the next current for 4s.
 */
export function startSteps(ids: readonly StageId[], done: number, at: number, took: readonly number[]): StartStep[] {
  return ids.map((id, i) => ({
    id, ...STAGE_WORDS[id],
    state: i < done ? "done" : i === done ? "current" : "pending",
    tookMs: took[i], since: i === done ? at - 4_000 : undefined,
  }));
}

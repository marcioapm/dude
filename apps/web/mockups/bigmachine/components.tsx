/*
 * What the proposal adds to the design system, built here to its rules:
 * tokens only, both themes, both densities, accessible names. Each reuses
 * the stylesheet of the component it extends (QuestionCard, MachineChip,
 * ServersSummary) rather than copying it; docs/design/agent-machine-request.md
 * lists the props and the README text each would add.
 *
 * - SizeChange: current → asked, per resource, with the delta, the pool
 *   when it changes, the memory peak against both limits, and the price
 *   per hour when it is known.
 * - MachineRequestCard: the agent's request, as QuestionCard draws a
 *   request (`kind="machine"` plus a `detail` slot), with a note back and
 *   Approve / Decline; settled, the record of who decided and their note.
 * - RaisedMachineChip: MachineChip with `raised` ("raised for this task").
 * - MachineRaises: the task's (or session's) raised machines, in
 *   ServersSummary's grammar, each with Reset.
 * - MoveProgress: the startup design's StartProgress, verb "Moving to Large".
 */

import { useEffect, useId, useState, type ReactNode } from "react";
import { formatDuration, formatUsd, Icon } from "@dude/design-system";
import { AgentAvatar, Duration, Markdown, StatusMark, TextButton, type Person } from "@dude/design-system/components";
import { Button, Callout, Textarea, Tooltip } from "@dude/design-system/primitives";
import qc from "../../../../packages/design-system/src/components/QuestionCard.module.css";
import mc from "../../../../packages/design-system/src/components/Machines.module.css";
import ss from "../../../../packages/design-system/src/components/ServersSummary.module.css";
import type { Size } from "./data.ts";
import s from "./bigmachine.module.css";

type Role = "implementer" | "brainstorm" | "qa_browser" | "simplifier" | "investigator";

const cx = (...names: Array<string | false | null | undefined>) => names.filter(Boolean).join(" ");
const n = (v: number) => (Number.isInteger(v) ? String(v) : v.toFixed(1));
const delta = (a: number, b: number) => `${b >= a ? "+" : "−"}${n(Math.abs(b - a))}`;

// ---- SizeChange -------------------------------------------------------

export interface SizeChangeProps {
  readonly from: Size;
  readonly to: Size;
  /** The most memory the Run's container used, as lux measured it, in GiB. */
  readonly peakGiB?: number | undefined;
  /** Price per hour, both sizes known: a row with the difference. */
  readonly showCost?: boolean | undefined;
  /** One line, for a settled request: the names and each "was → now". */
  readonly compact?: boolean | undefined;
}

/**
 * A size change in one block: the two names, then CPUs, memory and disk
 * as "2 → 4" with the difference, the pool when it changes, and the
 * memory peak drawn against the new limit with the old one marked. Every
 * number is text; the meter repeats what the words say.
 */
export function SizeChange({ from, to, peakGiB, showCost, compact }: SizeChangeProps) {
  const otherPool = from.poolId !== to.poolId;
  const cost = showCost && from.usdPerHour !== null && to.usdPerHour !== null;
  const label = `${from.name} to ${to.name}: ${from.cpus} to ${to.cpus} CPUs, ${from.memoryGiB} to ${to.memoryGiB} GiB of memory, ${from.diskGiB} to ${to.diskGiB} GiB of disk`
    + (otherPool ? `, in another pool, ${to.pool}` : "") + (peakGiB !== undefined ? `. Its memory peaked at ${n(peakGiB)} GiB.` : ".");
  if (compact) {
    return (
      <p className={s.changeLine} aria-label={label} data-testid="size-change">
        <Icon name="chip" size={12} className={s.changeGlyph} />
        <span aria-hidden>
          <span className={s.changeFrom}>{from.name}</span> → <span className={s.changeTo}>{to.name}</span>
          <span className={s.changeLineFacts}> · {n(from.cpus)} → {n(to.cpus)} CPUs · {n(from.memoryGiB)} → {n(to.memoryGiB)} GiB · {n(from.diskGiB)} → {n(to.diskGiB)} GiB disk{otherPool ? ` · ${from.pool} → ${to.pool} pool` : ""}</span>
        </span>
      </p>
    );
  }
  return (
    <figure className={s.change} aria-label={label} data-testid="size-change">
      <div className={s.changeHead} aria-hidden>
        <Icon name="chip" size={14} className={s.changeGlyph} />
        <span className={s.changeFrom}>{from.name}</span>
        <Icon name="arrow-right" size={14} className={s.changeArrow} />
        <span className={s.changeTo}>{to.name}</span>
      </div>
      <dl className={s.facts} aria-hidden>
        <Fact label="CPUs" from={n(from.cpus)} to={n(to.cpus)} diff={delta(from.cpus, to.cpus)} />
        <Fact label="Memory" from={n(from.memoryGiB)} to={`${n(to.memoryGiB)} GiB`} diff={delta(from.memoryGiB, to.memoryGiB)}
          extra={peakGiB !== undefined ? <PeakMeter peak={peakGiB} was={from.memoryGiB} now={to.memoryGiB} /> : undefined} />
        <Fact label="Disk" from={n(from.diskGiB)} to={`${n(to.diskGiB)} GiB`} diff={delta(from.diskGiB, to.diskGiB)} />
        {otherPool ? (
          <Fact label="Pool" from={from.pool} to={to.pool} mono
            extra={<span className={s.poolNote}><Icon name="warning" size={12} className={s.poolGlyph} />another pool: a fresh Run</span>} />
        ) : null}
        {cost ? (
          <Fact label="Price" from={formatUsd(from.usdPerHour!)} to={`${formatUsd(to.usdPerHour!)} an hour`}
            diff={`+${formatUsd(to.usdPerHour! - from.usdPerHour!)}`} />
        ) : null}
      </dl>
    </figure>
  );
}

function Fact({ label, from, to, diff, extra, mono }: { label: string; from: string; to: string; diff?: string; extra?: ReactNode; mono?: boolean }) {
  return (
    <div className={s.fact}>
      <dt className={s.factLabel}>{label}</dt>
      <dd className={cx(s.factValue, mono && s.mono)}>
        <span className={s.was}>{from}</span>
        <span className={s.factArrow}>→</span>
        <span className={s.now}>{to}</span>
      </dd>
      <dd className={s.factDiff}>{diff ?? ""}</dd>
      <dd className={s.factExtra}>{extra ?? null}</dd>
    </div>
  );
}

/** The peak against the new limit, with a tick where the old one was. */
function PeakMeter({ peak, was, now }: { peak: number; was: number; now: number }) {
  return (
    <span className={s.peak}>
      <span className={s.peakTrack}>
        <span className={s.peakFill} style={{ width: `${Math.min(100, (peak / now) * 100)}%` }} />
        <span className={s.peakTick} style={{ left: `${Math.min(100, (was / now) * 100)}%` }} />
      </span>
      <span className={s.peakWords}>peak {n(peak)} of {n(was)} GiB</span>
    </span>
  );
}

// ---- MachineRequestCard --------------------------------------------------

export interface MachineRequestCardProps {
  readonly role: Role;
  /** "Implementer", "Brainstorm": who asks. */
  readonly who: string;
  readonly from: Size;
  readonly to: Size;
  /** The agent's words: what ran out. Quoted, so they cannot pass for the card's. */
  readonly reason: string;
  readonly peakGiB?: number | undefined;
  readonly showCost?: boolean | undefined;
  readonly askedAt: number;
  /** Settled: who decided, how, when, and their note to the agent. */
  readonly decision?: { readonly approved: boolean; readonly by: string; readonly at: number; readonly note?: string | undefined } | undefined;
  /** Another pool: approving starts a fresh agent. What it would lose, from its last diff. */
  readonly fresh?: { readonly pushed: string; readonly uncommitted: { readonly files: readonly string[]; readonly added: number; readonly removed: number } | null } | undefined;
  /** Someone else decides: the card says whom, and offers nothing. */
  readonly waitingOn?: string | undefined;
  /** A session's: only that member may decide; nothing to take over. */
  readonly onlyThey?: boolean | undefined;
  readonly onDecide?: ((approve: boolean, note: string) => void) | undefined;
  /** Another pool: ask it to push first; the request stays open. */
  readonly onAskToPush?: (() => void) | undefined;
}

/**
 * An agent asks for a larger machine. A request turn in QuestionCard's
 * grammar: while it waits on the reader it is the one loud turn (the
 * attention wash and bar), the size change and the agent's reason in
 * quotes, then a note back and Approve / Decline. Settled, it is a plain
 * row: "Approved · by Márcio, after 2m", and the note.
 */
export function MachineRequestCard(props: MachineRequestCardProps) {
  const { role, who, from, to, reason, peakGiB, showCost, askedAt, decision, fresh, waitingOn, onlyThey, onDecide, onAskToPush } = props;
  const [note, setNote] = useState("");
  const waiting = !decision;
  const someoneElse = waiting && waitingOn !== undefined;
  const form = waiting && !someoneElse && onDecide !== undefined;
  const state = waiting ? "waiting" : "answered";
  const noteId = useId();
  const lost = fresh?.uncommitted;
  return (
    <article className={cx(qc["root"], qc[state])} data-state={state} data-testid="machine-request"
      aria-label={someoneElse ? `${who} asks for a larger machine and is waiting for ${waitingOn} to decide`
        : waiting ? `${who} asks for a larger machine and is waiting for a decision` : `${who} asked for a larger machine`}>
      <div className={qc["gutter"]}><AgentAvatar role={role} size="chat" live={waiting} /></div>
      <div className={qc["main"]}>
        <header className={qc["header"]}>
          <span className={qc["name"]}>{who}</span>
          <span className={qc["verb"]}>{waiting ? "asks" : "asked"} for a larger machine</span>
          {waiting ? (
            <span role="status" aria-live="polite" className="ds-sr-only">
              {someoneElse ? `Blocked until ${waitingOn} decides.` : "Needs you. It waits until you decide."}
            </span>
          ) : (
            <span className={cx(qc["tag"], decision.approved ? qc["tagAnswered"] : qc["tagDismissed"])}>
              <Icon name={decision.approved ? "check" : "cross"} size={10} strokeWidth={2} />
              <span className="ds-cap">{decision.approved ? "Approved" : "Declined"}</span>
            </span>
          )}
          {waiting ? (
            <span className={qc["wait"]}>waiting <Duration since={askedAt} live /></span>
          ) : (
            <span className={qc["waited"]}>by {decision.by}, after <Duration since={askedAt} until={decision.at} /></span>
          )}
          <time className={qc["time"]} dateTime={new Date(askedAt).toISOString()}>
            {new Date(askedAt).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false })}
          </time>
        </header>
        <div className={s.requestBody}>
          <SizeChange from={from} to={to} peakGiB={peakGiB} showCost={showCost} compact={!waiting} />
          <Markdown source={reason.split("\n").map((l) => `> ${l}`).join("\n")} unmeasured className={qc["body"]} />
          {fresh && waiting ? (
            <Callout tone={lost ? "danger" : "attention"} data-testid="fresh-warning">
              <span className={s.freshLine}>
                <b>{to.name} is in another pool, so approving starts a fresh {who.toLowerCase()}.</b>{" "}
                Its conversation is lost{lost ? <>, and so is its uncommitted work: <code>{lost.files.join(", ")}</code> (+{lost.added} −{lost.removed})</> : null}.
                {" "}What it pushed (<code>{fresh.pushed}</code>) is kept, and the new one starts from it with the reason as its note.
              </span>
            </Callout>
          ) : null}
          {form ? (
            <div className={s.decide}>
              <Textarea id={noteId} label={`Note to the ${who.toLowerCase()} (optional)`} rows={1} maxRows={4} value={note}
                onChange={(e) => setNote(e.target.value)} placeholder={fresh ? "Commit and push first, then go ahead." : "Use it for the export tests only."} />
              <div className={s.decideRow}>
                <Button size="sm" variant="primary" className={qc["answerButton"]} leadingIcon={fresh ? "retry" : "arrow-up"}
                  onClick={() => onDecide(true, note)} data-testid="approve">
                  {fresh ? `Approve: start fresh on ${to.name}` : `Approve ${to.name}`}
                </Button>
                <Button size="sm" variant="secondary" onClick={() => onDecide(false, note)} data-testid="decline">Decline</Button>
                {fresh && lost && onAskToPush ? (
                  <Button size="sm" variant="quiet" leadingIcon="git-branch" onClick={onAskToPush} data-testid="ask-push">Ask it to push first</Button>
                ) : null}
                <span className={s.decideHint}>
                  {fresh ? `Later ${who.toLowerCase()} Runs in this task run on ${to.name} too.`
                    : `It moves now, keeping its conversation and uncommitted work. Later ${who.toLowerCase()} Runs in this ${role === "brainstorm" ? "session" : "task"} run on ${to.name} too.`}
                </span>
              </div>
            </div>
          ) : null}
          {someoneElse ? (
            <p className={qc["waitingOn"]} data-testid="waiting-on">
              <Icon name="hand" size={12} />
              Waiting for {waitingOn} to decide
              {" "}<span className={qc["takeOver"]}>· {onlyThey ? `Only ${waitingOn} can decide this one: it's their session` : "Take over this task to decide"}</span>
            </p>
          ) : null}
          {decision?.note ? (
            <p className={s.decisionNote} data-testid="decision-note"><b>{decision.by}:</b> “{decision.note}”</p>
          ) : null}
        </div>
      </div>
    </article>
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
 * "raised for this task" in secondary ink. The tooltip says who asked,
 * who approved, and how to reset it.
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
  /** "follows the implementer", or who asked and who approved. */
  readonly detail: ReactNode;
  /** Its settings' size, which Reset goes back to. */
  readonly settings: Size;
  readonly onReset?: (() => void) | undefined;
}

/**
 * The machines raised in a task (or a session): one row per role, its
 * face, "Implementer · Large", who asked and approved under it, and Reset.
 * ServersSummary's grammar, on the chrome shade, in the overview's aside.
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
              <Tooltip content={`Later ${r.label.toLowerCase()} Runs go back to ${r.settings.name}, its settings' size. One running now keeps ${r.size.name} until it ends.`}>
                <TextButton onClick={r.onReset} aria-label={`Reset the ${r.label.toLowerCase()} to ${r.settings.name}`}>Reset</TextButton>
              </Tooltip>
            ) : null}
          </li>
        ))}
      </ul>
    </aside>
  );
}

// ---- MoveProgress (StartProgress from github/design/agent-startup) -------------------

export interface MoveStep {
  readonly id: string;
  readonly label: string;
  readonly doneLabel: string;
  readonly state: "done" | "current" | "pending";
  readonly tookMs?: number | undefined;
  readonly since?: number | undefined;
}

/**
 * The startup design's row, where the agent's next turn will be, with the
 * verb "Moving to Large": the current stage and its clock, the total once
 * a stage has passed, and the stages as a stepper behind the line.
 */
export function MoveProgress({ verb, steps, startedAt, avatar, defaultOpen }: {
  readonly verb: string; readonly steps: readonly MoveStep[]; readonly startedAt: number; readonly avatar: ReactNode; readonly defaultOpen?: boolean;
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
                <Icon name={st.state === "done" ? "check" : st.state === "current" ? "circle-half" : "circle-dotted"} size={12} strokeWidth={2} />
              </span>
              <span className={s.stepLabel}>
                {st.state === "done" ? st.doneLabel : st.label}
                <span className="ds-sr-only">{st.state === "pending" ? ", to come" : st.state === "current" ? ", now" : ""}</span>
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

/** Whom a person is, for a face beside the decision. */
export type { Person };

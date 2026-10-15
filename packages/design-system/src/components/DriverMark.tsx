import { forwardRef, type ButtonHTMLAttributes, type HTMLAttributes, type ReactNode, type Ref } from "react";
import { DECISION_POINT_LABEL, type DecisionPoint } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { RowMenu, type RowMenuItem } from "../primitives/RowMenu.tsx";
import { Tooltip } from "../primitives/Tooltip.tsx";
import styles from "./DriverMark.module.css";

/**
 * Who drives a task's delivery, as shown: Deliver, the conductor, or Deliver
 * with a conductor live in Chat that answers and decides nothing. Display
 * only: the delivery stores two values (policy, conductor); the third is
 * policy with a live conductor (`driverMode`).
 */
export type DriverMode = "deliver" | "conductor" | "deliver-chat";

/** The mode a delivery's decider and its live conductor make. */
export function driverMode(decider: "policy" | "conductor", conductorLive: boolean): DriverMode {
  if (decider === "conductor") return "conductor";
  return conductorLive ? "deliver-chat" : "deliver";
}

/** A decision point as short as the chip holds it. */
export const DECISION_POINT_SHORT: Record<DecisionPoint, string> = {
  start: "start building?",
  after_implement: "after the implementer",
  after_review: "the review's findings",
  after_fix: "after the fix",
  before_pull_request: "open the PR?",
  pull_request_feedback: "the PR's feedback",
};

const WHO: Record<DriverMode, string> = { deliver: "Deliver", conductor: "Conductor", "deliver-chat": "Deliver · conductor in Chat" };
const DETAIL: Record<DriverMode, string> = {
  deliver: "runs to the pull request on its own",
  conductor: "each finished step comes back to it",
  "deliver-chat": "the conductor answers; Deliver decides",
};

/** A decision waited on, and on whom: "it" (the conductor deciding), "you", or the owner's first name. */
export interface DriverWaiting {
  readonly point: DecisionPoint;
  readonly on: string;
}

/** The chip's words: who, then what that means or what it waits on (short, and in full for its name). */
export function driverWords(mode: DriverMode, waiting?: DriverWaiting | undefined): { who: string; detail: string; long: string } {
  if (!waiting) return { who: WHO[mode], detail: DETAIL[mode], long: DETAIL[mode] };
  return { who: WHO[mode], detail: `waiting on ${waiting.on}: ${DECISION_POINT_SHORT[waiting.point]}`,
    long: `waiting on ${waiting.on}: ${DECISION_POINT_LABEL[waiting.point]}` };
}

/** Deliver's bolt, the conductor's glyph in its role colour, or both (the conductor's muted): shape carries it in grayscale. */
export function DriverIcons({ mode, size = 14 }: { readonly mode: DriverMode; readonly size?: number }) {
  return (
    <span className={styles["icons"]} aria-hidden>
      {mode === "conductor" ? null : <Icon name="zap" size={size} className={styles["zap"]} />}
      {mode === "deliver" ? null : <Icon name="conductor" size={size} className={cx(styles["cond"], mode === "deliver-chat" && styles["condQuiet"])} />}
    </span>
  );
}

export interface DriverChipProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  readonly mode: DriverMode;
  readonly waiting?: DriverWaiting | undefined;
  /** A button that opens the switch (the owner's); otherwise a read-only mark that takes focus for its tooltip. */
  readonly switchable: boolean;
  /** Read-only: why it cannot be switched, said in its accessible name. */
  readonly readOnlyReason?: string | undefined;
}

/**
 * Who drives the task, beside its status: a chip on the neutral tint (a
 * PR chip's shape), the driver's glyphs, its name in primary ink and one
 * muted line of what that means or what it waits on. Never a tone: who
 * decides is a fact, not a state.
 */
export const DriverChip = forwardRef<HTMLButtonElement, DriverChipProps>(function DriverChip({ mode, waiting, switchable, readOnlyReason, className, ...rest }, ref) {
  const w = driverWords(mode, waiting);
  const body = (
    <>
      <DriverIcons mode={mode} />
      <span className={styles["who"]}>{w.who}</span>
      <span className={cx(styles["detail"], waiting && styles["detailWaiting"])}>{" · "}{w.detail}</span>
      {switchable ? <Icon name="chevron-down" size={12} className={styles["chev"]} /> : null}
    </>
  );
  if (!switchable) {
    // A span still forwards the tooltip's trigger props and ref.
    return (
      <span ref={ref as unknown as Ref<HTMLSpanElement>} {...(rest as HTMLAttributes<HTMLSpanElement>)} className={cx(styles["chip"], styles["readOnly"], className)}
        tabIndex={0} role="img" data-testid="driver" data-driver={mode} data-switchable="false"
        aria-label={`${w.who}: ${w.long}${readOnlyReason ? `. ${readOnlyReason}` : ""}`}>
        {body}
      </span>
    );
  }
  return (
    <button ref={ref} type="button" className={cx(styles["chip"], className)} data-testid="driver" data-driver={mode} data-switchable="true"
      aria-label={`${w.who}: ${w.long}. Change who decides`} {...rest}>
      {body}
    </button>
  );
});

/** An ended task, or an earlier attempt: who decided last, muted, nothing to switch. */
export function DriverEnded({ decider, note, className }: { readonly decider: "policy" | "conductor"; readonly note?: ReactNode; readonly className?: string | undefined }) {
  const conductor = decider === "conductor";
  const words = conductor ? "Decided by the conductor" : "Decided by Deliver";
  return (
    <Tooltip content={note ?? (conductor ? "The conductor took the last decisions." : "Deliver's rules took the last decisions.")}>
      <span className={cx(styles["chip"], styles["ended"], className)} tabIndex={0} role="img" aria-label={words}
        data-testid="driver" data-driver={conductor ? "conductor" : "deliver"} data-ended="">
        <DriverIcons mode={conductor ? "conductor" : "deliver"} />
        <span>{words}</span>
      </span>
    </Tooltip>
  );
}

export interface DriverSwitchProps {
  readonly mode: DriverMode;
  readonly waiting?: DriverWaiting | undefined;
  /** Who is at work now, as a doer ("fixer"): whose end is the next decision point. */
  readonly running?: string | undefined;
  /** A person let Deliver finish it: a later Chat message takes nothing back. */
  readonly handedBack: boolean;
  /** The reader may switch (the owner, or anyone on a task nobody owns). */
  readonly canSwitch: boolean;
  /** Who may, for the reader who may not: "Ana". */
  readonly ownerName?: string | undefined;
  /** A switch is under way: the menu's choices wait. */
  readonly busy?: boolean | undefined;
  readonly open?: boolean | undefined;
  readonly onOpenChange?: ((open: boolean) => void) | undefined;
  readonly onSwitch: (to: "policy" | "conductor") => void;
  /** Rendered with the menu open for a static preview (gallery). */
  readonly forceMount?: true | undefined;
  readonly container?: HTMLElement | null | undefined;
}

/** What giving the decisions to the conductor does now, in a sentence. */
export function toConductorWords(mode: DriverMode, waiting: DriverWaiting | undefined, running: string | undefined): string {
  if (waiting) return "It takes the decision waiting now.";
  const when = running ? `when the ${running} finishes; the ${running} keeps running` : "at the next decision point";
  return `It takes over ${when}. ${mode === "deliver-chat" ? "The conductor already in Chat takes it." : "A conductor starts in Chat to take it."}`;
}

/** What letting Deliver finish does now, in a sentence. */
export function toDeliverWords(waiting: DriverWaiting | undefined, running: string | undefined): string {
  if (waiting?.point === "before_pull_request") return "Deliver opens the pull request now, and finishes on its own. You are asked to confirm.";
  if (waiting) return "Deliver takes the decision waiting now and runs to the pull request. The conductor stays in Chat to answer.";
  return `Deliver takes over ${running ? `when the ${running} finishes` : "at the next decision point"} and runs to the pull request. The conductor stays in Chat to answer; it decides nothing.`;
}

/**
 * The chip is the switch: for the owner it opens a two-way choice, the
 * current one ticked, each saying what changes; nothing runs is stopped or
 * restarted, the next decision is the chosen one's. For anyone else it is
 * the same chip, read-only, its tooltip saying whose it is.
 */
export function DriverSwitch({ mode, waiting, running, handedBack, canSwitch, ownerName, busy, open, onOpenChange, onSwitch, forceMount, container }: DriverSwitchProps) {
  if (!canSwitch) {
    const reason = `Only the owner can change who decides.${ownerName ? ` ${ownerName} owns this task.` : ""}`;
    return (
      <Tooltip content={reason} keepOnPress>
        <DriverChip mode={mode} waiting={waiting} switchable={false} readOnlyReason={reason} />
      </Tooltip>
    );
  }
  const deciding = mode === "conductor" ? "conductor" : "policy";
  const items: RowMenuItem[] = [
    {
      kind: "radio", id: "decides", label: "Who decides", value: deciding,
      onValueChange: (v) => {
        if (v !== deciding && (v === "policy" || v === "conductor")) onSwitch(v);
      },
      items: [
        { id: "policy", label: deciding === "policy" ? "Deliver decides" : "Let Deliver finish it", leading: <DriverIcons mode="deliver" />,
          description: deciding === "policy" ? "Runs to the pull request on its own." : toDeliverWords(waiting, running), descriptionWraps: true, disabled: busy === true },
        { id: "conductor", label: deciding === "conductor" ? "The conductor decides" : "Give decisions to the conductor", leading: <DriverIcons mode="conductor" />,
          description: deciding === "conductor" ? "Each finished step comes back to it, in Chat, with you." : toConductorWords(mode, waiting, running),
          descriptionWraps: true, disabled: busy === true },
      ],
    },
    { kind: "separator" },
    {
      kind: "note", id: "driver-rule",
      text: handedBack && deciding === "policy"
        ? "You let Deliver finish it: writing in Chat no longer takes the decisions back. Switch here."
        : deciding === "policy" ? "Writing in Chat also gives the conductor the decisions."
        : "Takes effect at the next decision point: after implement, review or fix, before the PR, on its feedback.",
    },
  ];
  return (
    <RowMenu items={items} label="Who decides" align="start" className={styles["menu"]}
      {...(open !== undefined ? { open } : {})} {...(onOpenChange ? { onOpenChange } : {})}
      {...(forceMount ? { forceMount } : {})} {...(container ? { container } : {})}
      trigger={<DriverChip mode={mode} waiting={waiting} switchable />} />
  );
}

/**
 * A board card's or a sidebar row's mark: the glyphs alone, the words in
 * the tooltip; the card or row says them in its own name (`driverLabel`).
 * In progress only, and never a switch.
 */
export function DriverGlyph({ mode, className }: { readonly mode: DriverMode; readonly className?: string | undefined }) {
  const w = driverWords(mode);
  return (
    <Tooltip content={<><b>{w.who}</b> · {w.detail}</>}>
      <span className={cx(styles["glyph"], className)} data-testid="driver-glyph" data-driver={mode} aria-hidden>
        <DriverIcons mode={mode} size={12} />
      </span>
    </Tooltip>
  );
}

/** What a card's or row's accessible name adds for its driver: "Deliver, runs to the pull request on its own". */
export function driverLabel(mode: DriverMode): string {
  const w = driverWords(mode);
  return `${w.who}, ${w.detail}`;
}

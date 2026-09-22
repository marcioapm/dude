/**
 * Status vocabulary — the single source of truth for how each domain state
 * is presented. `StatusBadge` and every other status-aware component read
 * from here, so a state never gets two different treatments.
 *
 * Every status maps to:
 *   tone      one of five semantic tones (never hue alone)
 *   glyph     a shape that carries the meaning without color
 *   emphasis  how loud the default treatment is
 *   motion    whether it is "alive" (running / waiting on you)
 *
 * Rules:
 * - Only `awaiting_input` defaults to `solid`. Nothing
 *   else may — that is what keeps it noticeable.
 * - `failed` is danger; `aborted` is neutral. Aborting is an operator
 *   decision, not an error, and must not look like one.
 * - Terminal states are still; only live states animate, and only via a
 *   slow pulse that reduced-motion turns off.
 */

import {
  ALL_RUN_STATUSES,
  ALL_SESSION_STATUSES,
  ALL_WORK_ITEM_STATUSES,
  type RunStatus,
  type SessionStatus,
  type WorkItemStatus,
} from "@dude/domain";
import type { ToneName } from "./palette.ts";

/*
 * The status *names* are domain truth and are imported, not re-declared.
 * Their tone, glyph and emphasis are design truth and live here.
 *
 * Because `STATUS_SPECS` below is keyed on the imported unions, adding a
 * status to the domain is a compile error here until it is given a
 * treatment — which is exactly the right failure. A forked copy would
 * instead render the new state as a raw enum string.
 */
export const RUN_STATUSES = ALL_RUN_STATUSES;
export const SESSION_STATUSES = ALL_SESSION_STATUSES;
export const WORK_ITEM_STATUSES = ALL_WORK_ITEM_STATUSES;
export type { RunStatus, SessionStatus, WorkItemStatus };

export type Status = RunStatus | SessionStatus | WorkItemStatus;

export type StatusGlyph =
  | "circle" // hollow — nothing has happened yet
  | "circle-dotted" // thinking / being processed
  | "clock" // scheduled for later
  | "circle-half" // starting up
  | "spinner" // actively running
  | "pause"
  | "check"
  | "cross"
  | "stop" // filled square — aborted, deliberate
  | "inbox"
  | "question" // needs a confirmation
  | "hand" // blocked on the operator
  | "eye" // under review
  | "merge"
  | "list"; // queued

export type StatusEmphasis = "subtle" | "tinted" | "solid";

export interface StatusSpec {
  readonly label: string;
  readonly tone: ToneName;
  readonly glyph: StatusGlyph;
  readonly emphasis: StatusEmphasis;
  /** True when the state is in progress and may animate. */
  readonly live: boolean;
  /** True when the system is blocked on a person. */
  readonly needsHuman: boolean;
  /** True when no further change will happen. */
  readonly terminal: boolean;
  readonly description: string;
}

export const STATUS_SPECS: Record<Status, StatusSpec> = {
  // -- shared / run / session -------------------------------------------
  pending: {
    label: "Pending",
    tone: "neutral",
    glyph: "circle",
    emphasis: "subtle",
    live: false,
    needsHuman: false,
    terminal: false,
    description: "Created, nothing has happened yet.",
  },
  scheduled: {
    label: "Scheduled",
    tone: "neutral",
    glyph: "clock",
    emphasis: "subtle",
    live: false,
    needsHuman: false,
    terminal: false,
    description: "Will start at a later time or when capacity frees.",
  },
  starting: {
    label: "Starting",
    tone: "info",
    glyph: "circle-half",
    emphasis: "tinted",
    live: true,
    needsHuman: false,
    terminal: false,
    description: "Workspace and agent are being provisioned.",
  },
  running: {
    label: "Running",
    tone: "info",
    glyph: "spinner",
    emphasis: "tinted",
    live: true,
    needsHuman: false,
    terminal: false,
    description: "An agent is actively working and consuming tokens.",
  },
  paused: {
    label: "Paused",
    tone: "attention",
    glyph: "pause",
    emphasis: "tinted",
    live: false,
    needsHuman: false,
    terminal: false,
    description: "Suspended by policy or operator; can resume.",
  },
  completed: {
    label: "Completed",
    tone: "success",
    glyph: "check",
    emphasis: "tinted",
    live: false,
    needsHuman: false,
    terminal: true,
    description: "Finished successfully.",
  },
  failed: {
    label: "Failed",
    tone: "danger",
    glyph: "cross",
    emphasis: "tinted",
    live: false,
    needsHuman: false,
    terminal: true,
    description: "Ended with an error.",
  },
  aborted: {
    label: "Aborted",
    tone: "neutral",
    glyph: "stop",
    emphasis: "tinted",
    live: false,
    needsHuman: false,
    terminal: true,
    description: "Stopped deliberately by an operator or policy.",
  },
  awaiting_input: {
    label: "Needs you",
    tone: "attention",
    glyph: "hand",
    emphasis: "solid",
    live: true,
    needsHuman: true,
    terminal: false,
    description: "Blocked on the operator. Nothing proceeds until you act.",
  },

  // -- work item ---------------------------------------------------------
  received: {
    label: "Received",
    tone: "neutral",
    glyph: "inbox",
    emphasis: "subtle",
    live: false,
    needsHuman: false,
    terminal: false,
    description: "Request accepted, not yet examined.",
  },
  intake: {
    label: "Intake",
    tone: "neutral",
    glyph: "circle-dotted",
    emphasis: "tinted",
    live: true,
    needsHuman: false,
    terminal: false,
    description: "Being analysed and broken down.",
  },
  awaiting_confirmation: {
    label: "Confirm plan",
    tone: "attention",
    glyph: "question",
    emphasis: "tinted",
    live: false,
    needsHuman: true,
    terminal: false,
    description: "A plan is ready; the operator must approve before work starts.",
  },
  queued: {
    label: "Queued",
    tone: "neutral",
    glyph: "list",
    emphasis: "subtle",
    live: false,
    needsHuman: false,
    terminal: false,
    description: "Approved and waiting for a worker.",
  },
  review: {
    label: "In review",
    tone: "info",
    glyph: "eye",
    emphasis: "tinted",
    live: false,
    needsHuman: false,
    terminal: false,
    description: "A PR is open and under review (agent or human).",
  },
  ready_to_merge: {
    label: "Ready to merge",
    tone: "success",
    glyph: "merge",
    emphasis: "tinted",
    live: false,
    needsHuman: false,
    terminal: false,
    description: "Checks and reviews passed; awaiting merge.",
  },
  done: {
    label: "Done",
    tone: "success",
    glyph: "check",
    emphasis: "tinted",
    live: false,
    needsHuman: false,
    terminal: true,
    description: "Merged and closed.",
  },
};

export const ALL_STATUSES: readonly Status[] = Object.keys(STATUS_SPECS) as Status[];

export function statusSpec(status: Status): StatusSpec {
  return STATUS_SPECS[status];
}

export const ACTOR_TYPES = ["system", "human", "agent", "integration"] as const;
export type ActorType = (typeof ACTOR_TYPES)[number];

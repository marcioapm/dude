/**
 * Activity vocabulary — what an agent turn is doing *right now*.
 *
 * `status.ts` answers "what state is this Session in" at the granularity the
 * workflow tracks. This table answers the finer question the operator asks
 * while watching a live transcript: is the model thinking, is text
 * arriving, is it stuck on a tool, is it backing off after a 429, is it
 * waiting on me. Each of these must be distinguishable *at a glance*, in
 * peripheral vision, and without colour — so each has its own tone, glyph
 * and, crucially, its own rhythm of motion.
 *
 *   thinking        dashed circle drifting slowly (3.2s)     nothing to show yet
 *   streaming       a caret blinking (1.0s, stepped)        tokens arriving
 *   tool            an indeterminate sweep (1.6s)           dispatched, result pending
 *   retrying        a depleting countdown ring              backoff between attempts
 *   awaiting_input  the needs-you ring (2.4s)               blocked on the operator
 *   completed / failed / aborted                            still
 *
 * Every loop divides by `--ds-motion-live`, so reduced motion freezes each
 * one in a legible pose instead of removing it: the dashed circle stays
 * dashed, the caret stays visible, the sweep bar becomes a static stripe,
 * the countdown keeps ticking once a second as text.
 */

import type { SessionStatus } from "@dude/domain";
import type { IconName } from "../icons/index.tsx";
import type { ToneName } from "./palette.ts";

export const ACTIVITY_KINDS = [
  "thinking",
  "streaming",
  "tool",
  "retrying",
  "awaiting_input",
  "completed",
  "failed",
  "aborted",
] as const;
export type ActivityKind = (typeof ACTIVITY_KINDS)[number];

/** Which looping motion a state uses. Each is a distinct rhythm. */
export type ActivityMotion = "drift" | "blink" | "sweep" | "countdown" | "ring" | "none";

export interface ActivitySpec {
  readonly label: string;
  readonly tone: ToneName;
  readonly glyph: IconName;
  readonly motion: ActivityMotion;
  readonly live: boolean;
  readonly needsHuman: boolean;
  readonly terminal: boolean;
  readonly description: string;
}

export const ACTIVITY_SPECS: Record<ActivityKind, ActivitySpec> = {
  thinking: {
    label: "Thinking",
    tone: "neutral",
    glyph: "circle-dotted",
    motion: "drift",
    live: true,
    needsHuman: false,
    terminal: false,
    description: "The model is generating; nothing has been emitted yet.",
  },
  streaming: {
    label: "Writing",
    tone: "info",
    glyph: "caret",
    motion: "blink",
    live: true,
    needsHuman: false,
    terminal: false,
    description: "Tokens are arriving and being rendered as they land.",
  },
  tool: {
    label: "Running tool",
    tone: "info",
    glyph: "terminal",
    motion: "sweep",
    live: true,
    needsHuman: false,
    terminal: false,
    description: "A tool call is in flight; the model is waiting on its result.",
  },
  retrying: {
    label: "Retrying",
    tone: "attention",
    glyph: "retry",
    motion: "countdown",
    live: true,
    needsHuman: false,
    terminal: false,
    description: "An upstream error (429 / 5xx / timeout). Backing off before the next attempt.",
  },
  awaiting_input: {
    label: "Needs you",
    tone: "attention",
    glyph: "hand",
    motion: "ring",
    live: true,
    needsHuman: true,
    terminal: false,
    description: "Blocked on the operator. Nothing proceeds until you answer.",
  },
  completed: {
    label: "Done",
    tone: "success",
    glyph: "check",
    motion: "none",
    live: false,
    needsHuman: false,
    terminal: true,
    description: "The turn finished.",
  },
  failed: {
    label: "Failed",
    tone: "danger",
    glyph: "cross",
    motion: "none",
    live: false,
    needsHuman: false,
    terminal: true,
    description: "The turn ended with an error.",
  },
  aborted: {
    label: "Aborted",
    tone: "neutral",
    glyph: "stop",
    motion: "none",
    live: false,
    needsHuman: false,
    terminal: true,
    description: "Stopped deliberately by an operator or policy.",
  },
};

export function activitySpec(kind: ActivityKind): ActivitySpec {
  return ACTIVITY_SPECS[kind];
}

/**
 * The activity a session's *status* implies when nothing finer is known.
 * Keyed on the domain union: a new SessionStatus is a compile error here
 * until it is mapped, which is the point.
 */
export const ACTIVITY_FOR_SESSION_STATUS: Record<SessionStatus, ActivityKind | null> = {
  pending: null,
  running: "thinking",
  awaiting_input: "awaiting_input",
  completed: "completed",
  failed: "failed",
  aborted: "aborted",
};

/**
 * A tool call that has run longer than this is promoted from "in flight"
 * to "slow": the elapsed time moves from muted to attention ink and the
 * sweep bar changes tone. A 40-second `bash` must not look like a 200ms
 * `read`. Consumers can override per call.
 */
export const TOOL_SLOW_AFTER_MS = 20_000;

/** Tool-call outcome. Mirrors what the harness reports on `agent.tool.completed`. */
export const TOOL_CALL_STATUSES = ["running", "completed", "failed", "aborted"] as const;
export type ToolCallStatus = (typeof TOOL_CALL_STATUSES)[number];

/**
 * The agent's own plan, as written by `todowrite`. Statuses are the ones
 * Claude Code and OpenCode emit in practice.
 */
export const TODO_STATUSES = ["pending", "in_progress", "completed", "cancelled"] as const;
export type TodoStatus = (typeof TODO_STATUSES)[number];

export interface TodoSpec {
  readonly label: string;
  readonly tone: ToneName;
  readonly glyph: IconName;
  readonly live: boolean;
  readonly done: boolean;
}

export const TODO_SPECS: Record<TodoStatus, TodoSpec> = {
  pending: { label: "Pending", tone: "neutral", glyph: "circle", live: false, done: false },
  in_progress: { label: "In progress", tone: "info", glyph: "circle-dotted", live: true, done: false },
  completed: { label: "Completed", tone: "success", glyph: "check", live: false, done: true },
  cancelled: { label: "Cancelled", tone: "neutral", glyph: "cross", live: false, done: true },
};

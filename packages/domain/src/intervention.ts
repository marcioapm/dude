import { z } from "zod";

/**
 * Human intervention — plan §24.
 *
 * Steering, pausing and aborting are workflow primitives and durable domain
 * objects, not chat messages. A human redirecting an agent must be as
 * auditable as the agent's own actions: who asked for what, when, and whether
 * it took effect.
 */

/**
 * How long a directive applies.
 *
 * The distinction is what separates "stop doing X" from "for this one step,
 * do Y" — without it, every correction would either leak into later work or
 * be forgotten immediately.
 */
export const directiveScopeSchema = z.enum(["run", "turn"]);
export type DirectiveScope = z.infer<typeof directiveScopeSchema>;

export const directiveSchema = z.object({
  id: z.string(),
  organizationId: z.string(),
  taskId: z.string().nullable(),
  runId: z.string().nullable(),
  text: z.string().min(1),
  scope: directiveScopeSchema,
  createdBy: z.string().nullable(),
  createdAt: z.string().datetime({ offset: true }),
  /** An earlier directive this one replaces; the earlier row is kept. */
  supersedes: z.string().nullable(),
  /** When the agent received it. Null means still queued. */
  deliveredAt: z.string().datetime({ offset: true }).nullable(),
});
export type Directive = z.infer<typeof directiveSchema>;

/**
 * What a human has asked of a Run.
 *
 * Deliberately separate from `RunStatus`: status says what the Run *is*,
 * control says what was *requested*. They disagree for the moment between a
 * pause being asked for and the runner acting on it, and conflating them
 * would make that window unrepresentable.
 */
export const runControlSchema = z.enum(["none", "pause_graceful", "pause_hard", "abort", "resume"]);
export type RunControl = z.infer<typeof runControlSchema>;

/**
 * Pause semantics (plan §24).
 *
 * `graceful` lets the current safe atomic action finish — an agent mid-edit
 * completes the edit rather than leaving a half-written file. `hard` aborts
 * the turn immediately, accepting that the workspace may be mid-change.
 *
 * Both leave the workspace intact and keep receiving webhooks; neither
 * discards work.
 */
export const pauseModeSchema = z.enum(["graceful", "hard"]);
export type PauseMode = z.infer<typeof pauseModeSchema>;

/** Signal names the workflow runtime uses for interventions. */
export const InterventionSignals = {
  Steered: "run.steered",
  Paused: "run.paused",
  Resumed: "run.resumed",
  Aborted: "run.aborted",
} as const;

/**
 * Is this control request still pending for the runner?
 *
 * `none` means nothing was asked, or the runner already acted and cleared it.
 */
export function hasPendingControl(control: RunControl): boolean {
  return control !== "none";
}

/** Map a pause mode onto the control value the runner polls for. */
export function controlForPause(mode: PauseMode): RunControl {
  return mode === "hard" ? "pause_hard" : "pause_graceful";
}

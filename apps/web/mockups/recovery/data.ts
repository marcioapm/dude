/*
 * The mockup's world: one task of the gallery's (WI-2408, control-plane ›
 * Human intervention), owned by Márcio, whose implementer stopped part way.
 * Deterministic but for "now", so ages read naturally.
 */

import type { AgentRole } from "@dude/domain";
import type { Person } from "../../../../packages/design-system/src/components/PersonAvatar.tsx";

export const P: Record<string, Person> = {
  marcio: { id: "u_marcio", name: "Márcio Martins", online: true },
  ana: { id: "u_ana", name: "Ana Ribeiro", online: true },
};

const MIN = 60_000;
export const NOW = Date.now();
export const ago = (min: number) => NOW - min * MIN;

/** How it stopped: the two cases differ in what is worth offering first. */
export type Stop = "aborted" | "failed";

export const TASK = {
  id: "wi_2408",
  key: "WI-2408",
  title: "Pause graceful vs hard: finish the current tool call first",
  project: "control-plane",
  epic: "Human intervention",
  branch: "dude/wi_2408/attempt-1",
  goal:
    "A **graceful** pause lets the agent finish the tool call it is in, then stops the Run; a **hard** pause stops it at once. Both keep the workspace, and resume continues the conversation.",
  criteria: [
    "`pause_graceful` waits for the running tool call to end (at most 2 minutes), then stops the lux Run",
    "`pause_hard` stops the lux Run straight away",
    "The transcript says which pause it was and who asked for it",
  ],
};

export interface MockRun {
  readonly id: string;
  readonly phase: "implement" | "review" | "fix" | "simplify";
  readonly role: AgentRole;
  readonly category?: string;
  readonly attempt: number;
  readonly status: "completed" | "failed" | "aborted" | "running";
  readonly model: string;
  readonly startedAt: number;
  readonly endedAt: number | null;
  readonly head?: string;
  readonly costUsd: number;
  readonly note?: string;
}

/** Why it stopped, as the run recorded it. */
export const STOPPED: Record<Stop, { by: Person | null; why: string; short: string; at: number }> = {
  aborted: { by: P["ana"]!, why: "It's rewriting the runner protocol — that's not what we asked for.", short: "Aborted by Ana", at: ago(95) },
  failed: { by: null, why: "the agent's run ended before finishing its task: host lost (lux-pool-a-03 stopped heartbeating)", short: "Host lost", at: ago(95) },
};

/** Attempt 1, as it stands when it stopped. */
export function firstAttempt(stop: Stop): MockRun[] {
  return [
    { id: "run_imp1", phase: "implement", role: "implementer", attempt: 1, status: stop, model: "claude-opus-5-5", startedAt: ago(142), endedAt: ago(95), head: "4be19c2", costUsd: 3.41 },
  ];
}

/** The page after each way back, for the "after" screens. */
export function afterResume(stop: Stop): MockRun[] {
  return [{ ...firstAttempt(stop)[0]!, status: "running", endedAt: null, costUsd: 3.62 }];
}
export function afterRetry(stop: Stop): MockRun[] {
  return [
    { id: "run_imp2", phase: "implement", role: "implementer", attempt: 1, status: "running", model: "claude-opus-5-5", startedAt: ago(1), endedAt: null, costUsd: 0.08, note: "again, from 4be19c2" },
    ...firstAttempt(stop),
  ];
}
export function afterStartOver(stop: Stop): MockRun[] {
  return [
    { id: "run_imp3", phase: "implement", role: "implementer", attempt: 2, status: "running", model: "claude-opus-5-5", startedAt: ago(1), endedAt: null, costUsd: 0.05 },
    ...firstAttempt(stop),
  ];
}

/** The stopped session's last turns, to show where a resume picks up. */
export const LAST_TURNS = [
  { role: "implementer" as const, at: ago(104), content: "`pause_graceful` now waits on the shim's `tool.ended` before stopping. Writing the test for the 2-minute cap." },
  { role: "implementer" as const, at: ago(98), content: "The cap needs the runner to report tool boundaries. I'll add a `tool_boundary` frame to the runner protocol so the shim can see them." },
];

/** What the stopped Run left behind, from lux and git. */
export const LEFT = {
  pushed: { sha: "4be19c2", files: 6, additions: 214, deletions: 38, at: ago(101) },
  unpushed: { files: 3, additions: 61, deletions: 9 },
  /** How long lux keeps a stopped Run's volumes (lux retention, per tenant). */
  keptFor: "7 days",
  keptUntil: "Thu 8 Oct",
  spent: 3.41,
};

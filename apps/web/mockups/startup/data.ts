/*
 * The startup mockup's world: one brainstorm session ("Usage-based billing
 * options") and one task Run (BILL-212's implementer), and the stage
 * vocabulary the proposal adds. Times are offsets from when the page
 * opened, so every clock on it ticks from a believable value.
 */

import type { Person } from "@dude/design-system";

/** lux's start stages, in order (lux docs/concepts.md, "Stages"). `running` is lux state `starting` until the shim is up. */
export const START_STAGES = ["waiting", "image", "volumes", "repositories", "container", "running"] as const;
export type StartStage = (typeof START_STAGES)[number];

/**
 * One map of words for a stage, shared by agents and branch previews:
 * `now` while it is the current stage, `done` once passed (the resume
 * tooltip's words, which `RESUME_PHASES` would read from here).
 */
export const STAGE_WORDS: Record<StartStage, { readonly now: string; readonly done: string; readonly failed: string }> = {
  waiting: { now: "Waiting for a machine", done: "Machine found", failed: "while waiting for a machine" },
  image: { now: "Getting the image ready", done: "Image ready", failed: "while getting the image ready" },
  volumes: { now: "Restoring its workspace", done: "Workspace restored", failed: "while restoring its workspace" },
  repositories: { now: "Checking out repositories", done: "Repositories checked out", failed: "while checking out repositories" },
  container: { now: "Starting the container", done: "Container started", failed: "while starting the container" },
  running: { now: "Starting the agent", done: "Agent started", failed: "while starting the agent" },
};

/** Past this long in a stage, a calm line says why it can take a while. Not a failure, not amber. */
export const SLOW_AFTER_MS: Record<StartStage, number> = {
  waiting: 60_000,
  image: 120_000,
  volumes: 60_000,
  repositories: 90_000,
  container: 60_000,
  running: 60_000,
};

export const SLOW_WORDS: Record<StartStage, string> = {
  waiting: "No machine free yet: dude is waiting for one.",
  image: "A large image can take a few minutes to pull the first time on a machine.",
  volumes: "A large workspace takes a while to restore.",
  repositories: "A large repository takes a while to check out.",
  container: "The container is taking longer than usual to start.",
  running: "The agent is taking longer than usual to come up.",
};

export const P: Record<string, Person> = {
  marcio: { id: "u_marcio", name: "Márcio Martins" },
  ana: { id: "u_ana", name: "Ana Ribeiro" },
};

/** Where a starting agent is, as the orchestrator would pass it to the web. */
export interface StartState {
  /** A fresh agent, or a parked one taken back up. */
  readonly kind: "start" | "resume";
  readonly stage: StartStage;
  /** lux's `stageSince`, as a time. */
  readonly stageSince: number;
  /** When this start began: the Run's submit, or the resume's request. */
  readonly startedAt: number;
  /** Stages already passed in this start, with how long each took. */
  readonly done: ReadonlyArray<{ readonly stage: StartStage; readonly tookMs: number }>;
  /** lux's `stateReason` while waiting ("waiting for capacity: …"), when it gave one. */
  readonly luxReason?: string | undefined;
  /** It failed in `stage`: lux's reason, when it gave one. */
  readonly failed?: { readonly reason: string | null; readonly at: number } | undefined;
}

export type Scenario = "fresh" | "resume" | "slow" | "failed" | "running" | "run";

export const SCENARIOS: ReadonlyArray<{ readonly value: Scenario; readonly label: string; readonly note: string }> = [
  { value: "fresh", label: "1 · Fresh", note: "A new session's first message: its agent waits about 5s for a machine, then gets the image ready. Replay runs it again from the start." },
  { value: "resume", label: "2 · Resumed", note: "A brainstorm parked while nobody wrote, woken by Ana's message: it waited, restored its workspace, and is checking out repositories. The row is open to show the stages." },
  { value: "slow", label: "3 · Slow", note: "A new session's agent has had no machine for 1:40: a calm line says so, with what lux says about capacity. Nothing turns amber; nothing is wrong yet." },
  { value: "failed", label: "4 · Failed", note: "lux could not pull the image. The transcript's failure names the stage and lux's reason, with Try again; the message says it was not delivered. The stages fold under it." },
  { value: "running", label: "5 · Running", note: "The agent is up: the cue is gone, Ana's message was read, the agent answers, and the start is folded into the existing “Taken back up in 14s” line (hover it for the stages)." },
  { value: "run", label: "6 · Run page", note: "A task's implementer starting: the same chip in the Run's header, the same row where its first turn will be, and the steer hint in the composer." },
];

const s = 1000;

/** Each scenario's start, relative to `now` (the page's open). */
export function startFor(scenario: Scenario, now: number): StartState | null {
  switch (scenario) {
    case "fresh":
      return { kind: "start", stage: "waiting", stageSince: now - 2 * s, startedAt: now - 2 * s, done: [] };
    case "resume":
      return {
        kind: "resume", stage: "repositories", stageSince: now - 12 * s, startedAt: now - 31 * s,
        done: [{ stage: "waiting", tookMs: 9_400 }, { stage: "image", tookMs: 1_200 }, { stage: "volumes", tookMs: 8_600 }],
      };
    case "slow":
      return {
        kind: "start", stage: "waiting", stageSince: now - 100 * s, startedAt: now - 100 * s, done: [],
        luxReason: "waiting for capacity: 2 hosts in its pool lack cpus (requested 4)",
      };
    case "failed":
      return {
        kind: "start", stage: "image", stageSince: now - 47 * s, startedAt: now - 53 * s,
        done: [{ stage: "waiting", tookMs: 5_800 }],
        failed: { reason: "pull failed: manifest unknown", at: now - 4 * s },
      };
    case "running":
      return null;
    case "run":
      return {
        kind: "start", stage: "container", stageSince: now - 3 * s, startedAt: now - 41 * s,
        done: [{ stage: "waiting", tookMs: 4_100 }, { stage: "image", tookMs: 22_300 }, { stage: "volumes", tookMs: 900 }, { stage: "repositories", tookMs: 10_700 }],
      };
  }
}

/** Scenario 1 as it plays: waiting, then the image 5s after the start, then the rest. */
export const FRESH_SCRIPT: ReadonlyArray<{ readonly atMs: number; readonly stage: StartStage }> = [
  { atMs: 5_000, stage: "image" },
  { atMs: 19_000, stage: "volumes" },
  { atMs: 19_600, stage: "repositories" },
  { atMs: 27_000, stage: "container" },
  { atMs: 29_500, stage: "running" },
];

export const SESSION = {
  title: "Usage-based billing options",
  linked: [{ key: "BILL", name: "billing", repositories: [{ name: "acme/billing", defaultBranch: "main" }] }],
  earlier: {
    ask: "Compare Stripe Billing, Orb and Metronome for usage-based billing. We meter API calls per organisation; what would each need from billing?",
    answer: "I read the three providers’ docs and `billing/meter/` in the linked repository.\n\n- **Stripe Billing** takes meter events per customer; our `UsageEvent` maps to it almost as is.\n- **Orb** wants an idempotency key per event; `meter/flush.go` would need to keep one.\n- **Metronome** prices in its own contracts; our plan table would move there.\n\nShall I propose an epic for Stripe first, behind a flag?",
  },
  first: "I want to plan an epic for moving our usage metering to Stripe Billing. Read billing/meter first and tell me what has to change.",
  wake: "Yes, propose the Stripe epic. Keep the flag per organisation, and leave Orb out for now.",
  reply: "Reading `billing/meter/flush.go` again before I write the epic: the flush keeps no per-event key today, so the epic starts there.",
};

export const RUN = {
  key: "BILL-212",
  title: "Retry failed webhook deliveries",
  prompt: "## Retry failed webhook deliveries\n\nWebhook deliveries that fail are dropped. Retry them with exponential backoff, up to 6 attempts over 2 hours.",
  steer: "Use the existing `jobs` table for the schedule; don't add a queue.",
};

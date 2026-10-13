/**
 * The stages an agent passes through while it starts, as lux reports them
 * (`run.stage`), and their words: one map shared by agents, branch
 * previews and the resume line's tooltip, so a person reads one vocabulary.
 * `park` leads a Run moved to another machine (a drain, a preemption, or a
 * resize in place): it is stopped there, its work kept, before it waits
 * for a host again.
 */

export const START_STAGES = ["park", "waiting", "image", "volumes", "repositories", "container", "running"] as const;
export type StartStage = (typeof START_STAGES)[number];

/** `now` while it is the current stage; `done` once passed. */
export const START_STAGE_WORDS: Record<StartStage, { readonly now: string; readonly done: string }> = {
  park: { now: "Stopping it, keeping its work", done: "Stopped, work kept" },
  waiting: { now: "Waiting for a machine", done: "Machine found" },
  image: { now: "Getting the image ready", done: "Image ready" },
  volumes: { now: "Restoring its workspace", done: "Workspace restored" },
  repositories: { now: "Checking out repositories", done: "Repositories checked out" },
  container: { now: "Starting the container", done: "Container started" },
  // lux's stage `running` while its state is still `starting`: the harness coming up.
  running: { now: "Starting the agent", done: "Agent started" },
};

/** A fresh agent's stages. */
export const FRESH_START_STAGES: readonly StartStage[] = ["waiting", "image", "volumes", "repositories", "container", "running"];
/** A parked agent taken back up: lux announces every stage on a resume, the image and checkouts included. */
export const RESUME_STAGES: readonly StartStage[] = ["waiting", "image", "volumes", "repositories", "container", "running"];
/** A Run moved to another machine: stopped with its work kept, then resumed there. */
export const MOVE_STAGES: readonly StartStage[] = ["park", ...RESUME_STAGES];

export function isStartStage(value: unknown): value is StartStage {
  return typeof value === "string" && (START_STAGES as readonly string[]).includes(value);
}

/**
 * A Run's status as it is shown: `scheduled` reads `starting` once lux has placed it, which is any start stage past
 * `waiting` (the `running` stage is lux's state `starting`). dude records `scheduled` until lux reports the Run
 * running; the stage is the ledger's, so the header and the run lists derive it here rather than from a column.
 */
export function shownRunStatus<S extends string>(status: S, stage: string | null | undefined): S | "starting" {
  return status === "scheduled" && isStartStage(stage) && stage !== "waiting" && stage !== "park" ? "starting" : status;
}

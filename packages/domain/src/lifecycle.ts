/**
 * Lifecycle (docs/design/tracking-work.md, Epic 1): why a person ended a
 * task (its resolution), and whether it is archived. The words here are the
 * orchestrator's (delivery/lifecycle.go): every place that says them says
 * them the same way.
 */

import type { TaskStatus } from "./hierarchy.ts";

export type TaskResolution = "wont_do" | "duplicate" | "cant_reproduce" | "nothing_to_merge";

export const TASK_RESOLUTIONS: readonly TaskResolution[] = ["wont_do", "duplicate", "cant_reproduce", "nothing_to_merge"];

/** The resolutions Close offers: Nothing to merge is Mark done's. */
export type CloseResolution = Exclude<TaskResolution, "nothing_to_merge">;

export const RESOLUTION_LABEL: Record<TaskResolution, string> = {
  wont_do: "Won't do", duplicate: "Duplicate", cant_reproduce: "Can't reproduce", nothing_to_merge: "Nothing to merge",
};

/** Why a start is refused, first that holds (task_start_refusal, migration 158). */
export type StartRefusal = "archived" | "closed" | "blocked";

/** A person closed it: a resolution other than delivery's Nothing to merge. */
export function closedBy(resolution: TaskResolution | null | undefined): resolution is CloseResolution {
  return Boolean(resolution) && resolution !== "nothing_to_merge";
}

/**
 * Ended, for lifecycle: done, or aborted with a resolution. A task that
 * stopped without one (failed, or aborted by Stop) has not: Close is still
 * offered on it, and Archive closes it on the way.
 */
export function lifecycleEnded(status: TaskStatus, resolution: TaskResolution | null | undefined): boolean {
  return status === "done" || (status === "aborted" && Boolean(resolution));
}

/** The words a start refused as archived or closed gets, on every path. */
export function startRefusalWords(reason: "archived" | "closed", taskKey: string, resolution: TaskResolution | null | undefined): string {
  if (reason === "archived") return `${taskKey} is archived: unarchive it to start it.`;
  return `${taskKey} was closed as ${resolution ? RESOLUTION_LABEL[resolution] : "closed"}: reopen it to start it.`;
}

/** What Close and Archive refuse, as the routes' 409 codes. */
export type LifecycleRefusalCode = "running" | "review" | "not_ended" | "kind" | "loop" | "unfinished" | "archived" | "conflict";

/** The menu's and the routes' words for a task's state that refuses Close and Archive. */
export const STOP_AGENTS_FIRST = "Stop its agents first";
export const PULL_REQUEST_OPEN = "It has a pull request open";
export const UNARCHIVE_FIRST = "Unarchive it first, then reopen it";

/** Which of the refusals a task's status gives Close and Archive, if any. */
export function lifecycleBusy(status: TaskStatus): "running" | "review" | null {
  if (status === "running" || status === "awaiting_input") return "running";
  if (status === "review" || status === "ready_to_merge") return "review";
  return null;
}

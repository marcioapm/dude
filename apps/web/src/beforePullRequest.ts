/**
 * Before the pull request: the one place its choices are named for every
 * screen that shows them (the project's Delivery page, the task dialog, the
 * task's Overview rail and the held panel), from @dude/domain's map, and the
 * one place a task is read as held at it or sent back from it.
 */

import { PULL_REQUEST_CHOICE, PULL_REQUEST_CHOICES, type PullRequestChoice } from "@dude/domain";
import type { TaskDetail } from "./api/client.ts";

type GateFacts = Pick<TaskDetail, "decider" | "awaitingDecision" | "hold" | "pullRequestOpened" | "status" | "escalation">;

/** Deliver holds the task before its first pull request, for its owner's answer. */
export const heldAtGate = (task: GateFacts): boolean =>
  task.decider !== "conductor" && !task.pullRequestOpened && task.awaitingDecision?.point === "before_pull_request";

/**
 * A fixer works on the feedback sent from the hold before the first pull
 * request: the hold's sentBack, while the round runs or is agent-reviewed. An
 * escalation, a reviewer's question or a stop leaves sentBack set (a Retry
 * goes back to the fix) with nobody at work.
 */
export const sentBackFromGate = (task: GateFacts): boolean =>
  task.decider !== "conductor" && !task.pullRequestOpened && !heldAtGate(task) && task.hold?.sentBack === true
  && !task.escalation && (task.status === "running" || task.status === "review");

/** Why a task's choice can no longer change: a pull request is open. */
export const PULL_REQUEST_OPEN_TEXT = "The pull request is already open.";

/** Why a finished task's choice can no longer change. */
export const TASK_DONE_TEXT = "The task is done.";

/** The label of the setting wherever it is shown. */
export const BEFORE_PR_LABEL = "Before the pull request";

/** A task's value: one of the three, or its project's. */
export type TaskPullRequestChoice = PullRequestChoice | "project";

/** The three choices as a Select lists them, each with its one line. */
export const choiceOptions = PULL_REQUEST_CHOICES.map((value) => ({
  value,
  label: PULL_REQUEST_CHOICE[value].label,
  description: PULL_REQUEST_CHOICE[value].hint,
}));

/** "Project default (Open it)". */
export const projectDefaultLabel = (project: PullRequestChoice) => `Project default (${PULL_REQUEST_CHOICE[project].label})`;

/** A task's choices: its project's first, then the three. */
export const taskChoiceOptions = (project: PullRequestChoice) => [
  { value: "project" as TaskPullRequestChoice, label: projectDefaultLabel(project), description: PULL_REQUEST_CHOICE[project].hint },
  ...choiceOptions.map((o) => ({ ...o, value: o.value as TaskPullRequestChoice })),
];

/** The one line of what Deliver will do for a task's value. */
export const choiceHint = (value: TaskPullRequestChoice, project: PullRequestChoice) =>
  PULL_REQUEST_CHOICE[value === "project" ? project : value].hint;

/** A task's stored choice (null: its project's) as its control reads it. */
export const taskChoice = (stored: PullRequestChoice | null | undefined): TaskPullRequestChoice => stored ?? "project";

/** What a person reads once a change is saved: what Deliver will now do. */
export function choiceToast(value: TaskPullRequestChoice, project: PullRequestChoice): string {
  switch (value === "project" ? project : value) {
    case "ask":
      return "Deliver will ask before opening the pull request.";
    case "draft":
      return "Deliver will open the pull request as a draft.";
    default:
      return "Deliver will open the pull request.";
  }
}

/**
 * Decide what a change on a pull request means, without a model.
 *
 * A webhook arriving is not a reason to spend tokens (plan §13.3). Most of
 * what happens on a PR — an approval, a green check, a bot's status comment —
 * just updates state. Only two things are worth waking a fixer for: a person
 * asking for a change, and a check that failed.
 *
 * Pure, so the rules can be tested without a forge and read in one place.
 */

import type { PullRequestFeedback, PullRequestStatus } from "./github.ts";

export interface PriorState {
  state: string;
  checks: string;
}

/** What the workflow is told. Nothing, most of the time. */
export type PrSignal =
  | { kind: "terminal"; state: "merged" | "closed" }
  | { kind: "actionable"; feedback: ActionableFeedback[] };

export interface ActionableFeedback {
  source: "review" | "checks";
  author?: string;
  body: string;
  path?: string;
}

/**
 * Words that carry approval or thanks and nothing else.
 *
 * A comment made *only* of these — in any order, with any punctuation — is a
 * courtesy, however it is phrased: "LGTM so far, thanks!" is as empty of a
 * request as "LGTM". A comment containing anything else might be a request,
 * so it is passed on.
 *
 * Deliberately a vocabulary rather than an attempt at understanding: a
 * comment that slips through costs one fix Run, bounded by the PR loop's own
 * budget, while a real request that got filtered out is silently ignored —
 * the worse failure. So the list stays short and the default is "actionable".
 */
const COURTESY_WORDS = new Set([
  "lgtm", "looks", "look", "good", "great", "nice", "fine", "ok", "okay",
  "to", "me", "so", "far", "thanks", "thank", "you", "ty", "cheers",
  "approved", "approve", "ship", "it", "work", "job", "well", "done",
  "all", "very", "really", "much", "this", "is", "the", "for", "now",
  "👍", "🚀", "✅", "🎉", "❤️",
]);

/** Question marks and imperatives are how requests are spelled. */
const LOOKS_LIKE_A_REQUEST = /\?|\b(please|could|can you|should|would|change|rename|fix|add|remove|why)\b/i;

function isCourtesy(body: string): boolean {
  if (LOOKS_LIKE_A_REQUEST.test(body)) return false;
  const words = body.toLowerCase().match(/[\p{L}\p{N}']+|\p{Extended_Pictographic}/gu) ?? [];
  return words.length > 0 && words.every((w) => COURTESY_WORDS.has(w));
}

/** Bots post status, coverage and deploy-preview comments; none are requests. */
function isBot(author: string): boolean {
  return author.endsWith("[bot]") || author.endsWith("-bot") || author === "github-actions";
}

export function isActionableComment(feedback: PullRequestFeedback, factoryLogins: readonly string[]): boolean {
  if (isBot(feedback.author)) return false;
  // The factory's own comments are not feedback on the factory's work.
  if (factoryLogins.includes(feedback.author)) return false;
  if (!feedback.body.trim()) return false;
  // A review that requested changes is a request by definition, however
  // briefly it is worded.
  if (feedback.kind === "changes_requested") return true;
  return !isCourtesy(feedback.body);
}

/**
 * Classify one poll's worth of change.
 *
 * Returns null when nothing warrants the workflow's attention — which is the
 * common case, and why this exists.
 */
export function classify(
  prior: PriorState,
  current: PullRequestStatus,
  newFeedback: readonly PullRequestFeedback[],
  factoryLogins: readonly string[] = [],
): PrSignal | null {
  if (current.state === "merged" || current.state === "closed") {
    return prior.state === current.state ? null : { kind: "terminal", state: current.state };
  }

  const actionable: ActionableFeedback[] = newFeedback
    .filter((f) => isActionableComment(f, factoryLogins))
    .map((f) => ({
      source: "review" as const,
      author: f.author,
      body: f.body,
      ...(f.path ? { path: f.path } : {}),
    }));

  // A check turning red is worth a fixer; one that was already red is not
  // news, and waking again for it would spend the loop's budget on a problem
  // the last fix already saw.
  if (current.checks === "failing" && prior.checks !== "failing") {
    actionable.push({
      source: "checks",
      body: "Continuous integration is failing on this branch. Find out why and fix it.",
    });
  }

  return actionable.length > 0 ? { kind: "actionable", feedback: actionable } : null;
}

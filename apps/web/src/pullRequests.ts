/**
 * A pull request in words, beyond what its chip and panel say (the design
 * system's PrChip and PullRequestPanel): why it cannot be merged yet, and
 * what happened to it, as the task's activity tells it.
 *
 * Pure, so the words are tested without a browser. Its state is
 * `prDisplayState`'s (in @dude/domain), computed by the API as `display`.
 */

import type { PersistedEvent, PullRequest } from "@dude/domain";
import { prCheckFailed } from "@dude/domain";

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

/** A reviewer's latest word, in a person's words. */
export function reviewWords(state: string): string {
  switch (state.toUpperCase()) {
    case "APPROVED":
      return "approved";
    case "COMMENTED":
      return "commented";
    case "CHANGES_REQUESTED":
      return "requested changes";
    case "REQUESTED":
      return "review requested";
    case "DISMISSED":
      return "review dismissed";
    default:
      return state.toLowerCase().replaceAll("_", " ");
  }
}

/** Why the Merge button is off, in a person's words; null when it is on. */
export function mergeBlockedBy(pr: PullRequest): string | null {
  if (pr.state !== "open") return pr.state === "draft" ? "It is a draft" : null;
  if (pr.display === "ready") return null;
  const why: string[] = [];
  const failing = pr.checks.filter(prCheckFailed).map((c) => c.name);
  if (pr.checkState === "failing") why.push(failing.length ? `${failing.join(", ")} failing` : "checks failing");
  if (pr.checkState === "pending") why.push("checks still running");
  if (pr.review === "changes_requested") why.push("changes requested");
  if (pr.review === "pending") why.push("nobody has approved it");
  if (pr.mergeable === "conflicting") why.push("it conflicts with its base");
  if (pr.unresolvedThreads > 0) why.push(`${plural(pr.unresolvedThreads, "thread")} unresolved`);
  return why.length ? `Blocked: ${why.join(", ")}` : "Not ready to merge";
}

export interface PullRequestActivity {
  /** A GitHub login or a name, when a person did it; null for dude or GitHub. */
  who: string | null;
  /** Whose name to look up among the organization's people: an actor id. */
  actorId: string | null;
  text: string;
  quote?: string | undefined;
}

const str = (v: unknown) => (typeof v === "string" ? v : "");

/**
 * One line of a task's activity for what happened to its pull request, or
 * null for a change not worth a line. `pr` names it (#41, or web#12 when a
 * task has several).
 */
export function pullRequestActivity(e: PersistedEvent, named: boolean): PullRequestActivity | null {
  const p = e.payload;
  const pr = `${named && p.repo ? str(p.repo) : ""}#${String(p.number ?? "")}`;
  const system = (text: string, quote?: string): PullRequestActivity => ({ who: null, actorId: null, text, quote });
  const person = (who: string, text: string, quote?: string): PullRequestActivity => ({ who: who || "Someone", actorId: null, text, quote });
  switch (e.eventType) {
    case "pull_request.opened": {
      const asked = Array.isArray(p.reviewersRequested) ? (p.reviewersRequested as string[]) : [];
      return system(`dude opened ${pr}${p.draft ? " as a draft" : ""}${asked.length ? `, asking ${asked.join(" and ")} to review` : ""}`);
    }
    case "pull_request.commented": {
      const ignored = p.ignored === "not_permitted" ? " — not acted on: they may not wake a fixer" : "";
      const verb = p.kind === "changes_requested" ? "requested changes on" : p.kind === "review" ? "reviewed" : p.kind === "line_comment" ? `commented on ${str(p.path)} in` : "commented on";
      return person(str(p.author), `${str(p.author) || "Someone"} ${verb} ${pr}${ignored}`, str(p.body));
    }
    case "pull_request.reviewed": {
      const reviews = Array.isArray(p.reviews) ? (p.reviews as Array<{ login: string; state: string }>) : [];
      if (reviews.length === 1) {
        const [r] = reviews as [{ login: string; state: string }];
        return person(r.login, `${r.login} ${reviewWords(r.state)}${r.state === "APPROVED" ? "" : " on"} ${pr}`);
      }
      if (reviews.length > 1) return system(reviews.map((r) => `${r.login} ${reviewWords(r.state)}`).join(", ") + ` on ${pr}`);
      return system(`The review on ${pr} is now ${str(p.to).replace("_", " ")}`);
    }
    case "pull_request.checks_changed": {
      const failing = Array.isArray(p.failing) ? (p.failing as string[]) : [];
      if (p.to === "failing") return system(`CI ${failing.length ? `${failing.join(", ")} ` : ""}failed on ${pr}`);
      if (p.to === "passing") return system(`Checks passed on ${pr}`);
      if (p.to === "pending") return system(`Checks started on ${pr}`);
      return null;
    }
    case "pull_request.pushed":
      return person(str(p.author), `${str(p.author) || "Someone"} pushed to ${pr} on GitHub — the next fix starts from it`);
    case "pull_request.mergeable_changed":
      if (p.to === "conflicting") return system(`${pr} conflicts with its base`);
      if (p.to === "behind") return system(`${pr} is ${plural(Number(p.behindBy ?? 0), "commit")} behind its base`);
      if (p.to === "clean" && p.from !== "unknown") return system(`${pr} is up to date with its base`);
      return null;
    case "pull_request.action": {
      const act = {
        merge: `merged ${pr}${p.method ? ` (${str(p.method)})` : ""}`,
        "update-branch": `updated ${pr}'s branch`,
        "rerun-failed": `re-ran ${plural(Number(p.rerun ?? 0), "failed check")} on ${pr}`,
        reviewers: `asked ${Array.isArray(p.logins) ? (p.logins as string[]).join(" and ") : "someone"} to review ${pr}`,
      }[str(p.action)];
      return act ? { who: null, actorId: e.actor.id, text: act } : null;
    }
    case "pull_request.merged":
      return system(`${pr} was merged`);
    case "pull_request.closed":
      return system(`${pr} was closed without merging`);
    case "pull_request.updated":
      return p.from === "closed" && p.to === "open" ? system(`${pr} was reopened`) : null;
    default:
      return null;
  }
}

/**
 * Why delivery stopped and asked for a person, in words.
 *
 * The workflow names its reason with a word ("stuck", "no_changes",
 * "implement_failed") and what it knew (delivery.escalate); this is the one
 * place that turns that into what the task's page and "Waiting on you" say
 * — the short form a row has room for, and the sentence the task explains
 * itself with.
 */

import type { Escalation } from "@dude/domain";

export interface EscalationWords {
  /** A few words, for a row: "Implementer failed". */
  short: string;
  /** A sentence, for the task's page. */
  sentence: string;
  /** The Run it is about, when the workflow named one: where to look. */
  runId: string | null;
}

/** "1 fix", "3 fixes": a count and its noun. */
export const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

/** The first line of an error, short enough for a sentence. */
export function shortError(error: string, max = 160): string {
  const line = error.split("\n")[0]!.trim();
  return line.length > max ? `${line.slice(0, max - 1)}…` : line;
}

export function escalationWords(e: Escalation): EscalationWords {
  const d = e.detail ?? {};
  const num = (k: string) => (typeof d[k] === "number" ? (d[k] as number) : 0);
  const runId = typeof d.runId === "string" ? d.runId : null;
  const error = typeof d.error === "string" && d.error ? `: ${shortError(d.error)}` : ".";
  const failed = (who: string) => ({ short: `${who} failed`, sentence: `The ${who.toLowerCase()} failed${error}`, runId });
  switch (e.reason) {
    case "stuck": {
      const n = Array.isArray(d.findingIds) ? d.findingIds.length : 0;
      return {
        short: n ? `Review stuck on ${plural(n, "finding")}` : "Review stuck",
        sentence: `The review got stuck: the fixer could not resolve ${n ? plural(n, "blocking finding") : "a blocking finding"} after repeated attempts.`,
        runId,
      };
    }
    case "exhausted":
      return {
        short: "Review rounds used up",
        sentence: `Blocking findings are still open after ${plural(num("iterations"), "review round")}, the most this project allows.`,
        runId,
      };
    case "no_changes":
      return { short: "No changes made", sentence: "The implementer finished without changing anything, so there was nothing to review.", runId };
    case "implement_failed":
      return failed("Implementer");
    case "fix_failed":
      return failed("Fixer");
    case "test_failed":
      return failed("Tester");
    case "pr_fix_failed":
      return { ...failed("Fixer"), sentence: `The fixer answering the pull request's feedback failed${error}` };
    case "pr_loop_exhausted": {
      const spent = Array.isArray(d.spent) ? (d.spent as string[]) : [];
      return spent.length > 0
        ? {
            short: "PR fix rounds used up",
            sentence: `${spent.length === 1 ? `The pull request in ${spent[0]} has` : `The pull requests in ${spent.join(", ")} have`} had ${plural(num("total"), "fix", "fixes")}, the most the organization allows for one.`,
            runId,
          }
        : {
            short: "PR fix rounds used up",
            sentence: `The pull request still has feedback in this review after ${plural(num("iterations"), "fix round")}, the most this project allows.`,
            runId,
          };
    }
    case "pull_request_conflict": {
      const which = typeof d.repo === "string" && d.repo ? `${d.repo} #${num("number")}` : `#${num("number")}`;
      return {
        short: "A pull request conflicts",
        sentence: `Pull request ${which} conflicts with its base. Resolve it on GitHub (or update the branch), then wait on it again.`,
        runId,
      };
    }
    case "ci_stuck": {
      const which = typeof d.repo === "string" && d.repo ? `${d.repo} #${num("number")}` : `#${num("number")}`;
      return {
        short: "CI is stuck",
        sentence: `Checks on pull request ${which} have been running far longer than they should: a runner may be gone, or a required check never starts.`,
        runId,
      };
    }
    case "pull_request_closed": {
      const parts = [
        num("merged") ? `${num("merged")} merged` : "",
        num("closed") ? `${num("closed")} closed without merging` : "",
        num("open") ? `${num("open")} still open` : "",
      ].filter(Boolean);
      return {
        short: "A pull request was closed",
        sentence: `Its pull requests went different ways${parts.length ? ` (${parts.join(", ")})` : ""}: whether the task is done is a person's call.`,
        runId,
      };
    }
    default: {
      const words = e.reason.replaceAll("_", " ");
      return { short: words.charAt(0).toUpperCase() + words.slice(1), sentence: `Delivery stopped: ${words}.`, runId };
    }
  }
}

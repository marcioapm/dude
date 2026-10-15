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
import { plural } from "@dude/design-system";

export interface EscalationWords {
  /** A few words, for a row: "Implementer failed". */
  short: string;
  /** A sentence, for the task's page. */
  sentence: string;
  /** The Run it is about, when the workflow named one: where to look. */
  runId: string | null;
}


/** The first line of an error, short enough for a sentence. */
export function shortError(error: string, max = 160): string {
  const line = error.split("\n")[0]!.trim();
  return line.length > max ? `${line.slice(0, max - 1)}…` : line;
}

/** The criteria a test escalation is about, as the gate named them. */
export function testCriteria(e: Escalation): Array<{ criterion: number; text: string; status: string }> {
  const list = e.detail?.criteria;
  if (!Array.isArray(list)) return [];
  return list.filter((c): c is { criterion: number; text: string; status: string } =>
    typeof c === "object" && c !== null && typeof c.criterion === "number" && typeof c.text === "string");
}

/** "#1 “it greets” and #3 “it pays”", short. */
function criteriaList(criteria: Array<{ criterion: number; text: string }>): string {
  const one = (c: { criterion: number; text: string }) => `#${c.criterion} “${shortError(c.text, 60)}”`;
  const shown = criteria.slice(0, 3).map(one);
  if (criteria.length > 3) shown.push(`${criteria.length - 3} more`);
  return shown.length > 1 ? `${shown.slice(0, -1).join(", ")} and ${shown.at(-1)}` : shown[0]!;
}

/** An action that ships past a criterion nobody saw work: it needs the person's reason. */
export function needsReason(reason: string, action: string): boolean {
  return (reason === "test_failed_criteria" && action === "accept") || (reason === "test_blocked" && action === "waive");
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
    case "test_failed_criteria":
    case "test_blocked": {
      const criteria = testCriteria(e);
      const named = criteria.length ? ` ${criteriaList(criteria)}` : "";
      const findings = Array.isArray(d.findingIds) ? d.findingIds.length : 0;
      if (e.reason === "test_failed_criteria") {
        return {
          short: criteria.length ? `${plural(criteria.length, "criterion", "criteria")} still failing` : "Test still failing",
          sentence: `The tester still finds${named || ` ${plural(findings, "blocking problem")}`} failing after ${plural(num("rounds"), "fix round")}. Send it back once more, or accept the failure with your reason.`,
          runId,
        };
      }
      return {
        short: "Test blocked",
        sentence: `The tester could not check${named || " the change"}${findings ? ": it could not run the application" : ""}. Tell it how (the project's notes) and try again, or waive what could not be checked with your reason.`,
        runId,
      };
    }
    case "review_failed": {
      const category = typeof d.category === "string" && d.category ? `${d.category} ` : "";
      return {
        short: `A ${category}reviewer failed`,
        sentence: `The ${category}reviewer failed${error === "." ? "." : `${error}.`} The round is not complete without it: try it again, or go on without it.`,
        runId,
      };
    }
    case "pr_fix_failed":
      return { ...failed("Fixer"), sentence: `The fixer answering the pull request's feedback failed${error}` };
    case "pr_loop_exhausted": {
      const spent = Array.isArray(d.spent) ? (d.spent as string[]) : [];
      return spent.length > 0
        ? {
            short: "PR fix rounds used up",
            sentence: `${spent.length === 1 ? `The pull request in ${spent[0]} has` : `The pull requests in ${spent.join(", ")} have`} had ${plural(num("total"), "fix", "fixes")}, the most the organisation allows for one.`,
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
    case "pull_request_moved": {
      const which = typeof d.repo === "string" && d.repo ? `${d.repo}${d.number ? ` #${num("number")}` : ""}` : `#${num("number")}`;
      return {
        short: "The pull request moved",
        sentence: `Someone pushed to pull request ${which} while dude checked a fix for it. The fix was not moved over their commit: fix the feedback again from what the branch holds now, or wait on the pull request without it.`,
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
    case "source_changed":
      // The orchestrator's reason names what changed and the two ways on.
      return { short: "Adopted work changed on GitHub", sentence: typeof d.reason === "string" && d.reason ? d.reason : "Delivery stopped: source changed.", runId };
    default: {
      const words = e.reason.replaceAll("_", " ");
      return { short: words.charAt(0).toUpperCase() + words.slice(1), sentence: `Delivery stopped: ${words}.`, runId };
    }
  }
}

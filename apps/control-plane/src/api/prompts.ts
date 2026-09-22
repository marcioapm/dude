/**
 * What each phase asks its agent to do.
 *
 * Kept out of the claim route because a prompt is the phase's contract with
 * the model, and reading six of them inline would bury the query that fetches
 * the work. Each is composed from what the ledger already knows — the work
 * item, the findings, the PR feedback — rather than asking a model to
 * summarize work another model already did.
 *
 * Per-project context is appended last, so a project can tell its tester how
 * to start the app and its reviewer what this codebase considers a defect,
 * without that text reaching every other role.
 */

import type { ActionableFeedback } from "../forge/classify.ts";

export interface PromptInput {
  title: string;
  goal: string;
  acceptanceCriteria: string[];
  /** Review phase: which reviewer flavour this Run is. */
  category?: string | null;
  /** Fix phase: the findings this Run must address. */
  findings?: ReadonlyArray<{
    severity: string;
    category: string;
    file: string | null;
    line: number | null;
    title: string;
    description: string;
    suggestedFix: string;
  }>;
  /** Fix phase: pull request feedback this Run must address. */
  prFeedback?: ReadonlyArray<ActionableFeedback>;
  /** Per-project, per-role context appended to the prompt. */
  context?: string | null;
}

/** The task, as every phase needs it stated. */
function task(input: PromptInput): string {
  const parts = [input.title];
  const goal = input.goal.trim();
  if (goal) parts.push(goal);
  if (input.acceptanceCriteria.length > 0) {
    parts.push(
      "Acceptance criteria:\n" + input.acceptanceCriteria.map((c) => `- ${c}`).join("\n"),
    );
  }
  return parts.join("\n\n");
}

/**
 * What each reviewer flavour is looking for.
 *
 * Narrow on purpose. A reviewer told to "look for problems" finds the same
 * generic ones every time; one told to look at a specific axis reads the
 * diff differently, which is the whole reason for fanning out.
 */
const REVIEW_FOCUS: Record<string, string> = {
  correctness:
    "Does this do what it claims? Look for logic errors, unhandled cases, " +
    "race conditions, and assumptions the code makes but does not check.",
  security:
    "Look for credentials that reach somewhere they should not, missing " +
    "authorization checks, injection, and data crossing a tenant boundary.",
  performance:
    "Look for work repeated per item that could be done once, queries in " +
    "loops, and anything that grows worse than linearly with real data.",
  frontend:
    "Look for accessibility gaps, layout that breaks at other sizes, state " +
    "that can render an impossible combination, and re-render storms.",
  database:
    "Look for migrations that lock or cannot be rolled back, missing " +
    "indexes on new query paths, and constraints the code assumes but the " +
    "schema does not enforce.",
  api: "Look for breaking changes to existing callers, inconsistent error " +
    "shapes, and endpoints that leak more than the caller should see.",
};

const FINDING_FORMAT = `
Report each finding as one YAML document, separated by \`---\`:

\`\`\`yaml
severity: blocking | high | medium | low | note
category: <your review category>
file: path/to/file.ts
line: 123
title: One line naming the problem
description: What is wrong and why it matters.
suggested_fix: What to do instead.
\`\`\`

Report nothing if you find nothing. A finding you are not confident in is a
\`note\`, not a \`blocking\` — a reviewer that cries wolf costs the next fix
attempt for nothing.`;

/** Compose the prompt for one phase. */
export function promptFor(phase: string, input: PromptInput): string {
  const sections: string[] = [];

  switch (phase) {
    case "investigate":
      sections.push(
        "Investigate this task before any code is written. Read the relevant " +
          "code, identify what will have to change, and report what you found. " +
          "Do not change anything.",
        task(input),
      );
      break;

    case "implement":
      sections.push(
        "Implement this task. Run the project's formatter, type checks and " +
          "tests before you finish — handing over code that does not build is " +
          "not finishing. Commit your work.",
        task(input),
      );
      break;

    case "review": {
      const category = input.category ?? "correctness";
      sections.push(
        `Review the changes on this branch for **${category}**.`,
        REVIEW_FOCUS[category] ?? REVIEW_FOCUS.correctness!,
        "You may run the code, run the tests, and write throwaway scripts to " +
          "check a hypothesis. Do not commit: your output is findings, and " +
          "someone else will make the change.",
        `The task under review:\n\n${task(input)}`,
        FINDING_FORMAT,
      );
      break;
    }

    case "fix": {
      sections.push("Address the feedback below. Commit your work.");

      if (input.findings?.length) {
        sections.push(
          "## Review findings\n\n" +
            input.findings
              .map((f) =>
                [
                  `### [${f.severity}] ${f.title}`,
                  f.file ? `\`${f.file}${f.line ? `:${f.line}` : ""}\`` : null,
                  f.description,
                  f.suggestedFix ? `Suggested: ${f.suggestedFix}` : null,
                ]
                  .filter(Boolean)
                  .join("\n\n"),
              )
              .join("\n\n"),
        );
      }

      if (input.prFeedback?.length) {
        sections.push(
          "## Pull request feedback\n\n" +
            input.prFeedback
              .map((c) =>
                [c.path ? `\`${c.path}\`` : null, c.author ? `**${c.author}**` : null, c.body]
                  .filter(Boolean)
                  .join(" — "),
              )
              .join("\n\n"),
        );
      }

      sections.push(
        "Fix only what is raised above. Widening the change makes the " +
          "re-review harder and risks new findings.",
        `The original task, for context:\n\n${task(input)}`,
      );
      break;
    }

    case "simplify":
      sections.push(
        "Simplify the changes on this branch without changing what they do.",
        "Remove needless complexity, improve names and structure, delete dead " +
          "code the change introduced, and consolidate obvious duplication.",
        "Do not widen the scope, do not add features, and do not change " +
          "behaviour. Run the tests: if they do not pass, your simplification " +
          "was not behaviour-preserving. Commit your work.",
        `The task this branch implements:\n\n${task(input)}`,
      );
      break;

    case "test":
      sections.push(
        "Exercise this change the way a person would. Start the application, " +
          "drive it in a browser, and confirm it does what the task asked.",
        "Record what you did: screenshots at each meaningful step, and a video " +
          "of the whole flow. Read the console and report anything it says.",
        "You are not looking for what the unit tests already cover. You are " +
          "looking for what they cannot: does the feature actually work when " +
          "used.",
        task(input),
        FINDING_FORMAT,
      );
      break;

    default:
      sections.push(task(input));
  }

  if (input.context?.trim()) {
    sections.push(`## Project notes\n\n${input.context.trim()}`);
  }

  return sections.join("\n\n");
}

/**
 * What the Judgements page computes: a decision's badge, how sure, and the
 * test question's answer as a line. Pure, so the words are tested apart
 * from the screen.
 */

import type { JudgementDecision, JudgementTestResult } from "@dude/domain";
import { formatDuration, formatTokens, type IconName } from "@dude/design-system";

export const DECISION: Record<JudgementDecision, { tone: "success" | "danger" | "attention"; icon: IconName; word: string }> = {
  yes: { tone: "success", icon: "check", word: "Yes" },
  no: { tone: "danger", icon: "cross", word: "No" },
  unsure: { tone: "attention", icon: "question", word: "Unsure" },
};

/** How sure, as a percentage: "93%"; "—" for none. */
export const sure = (confidence: number | null): string => (confidence === null ? "—" : `${Math.round(confidence * 100)}%`);

/** An error as a sentence of its own: its first letter capitalised, a full stop unless it ends in punctuation. */
export function errorSentence(error: string): string {
  const s = error.trim();
  if (!s) return "";
  const capitalised = s[0]!.toUpperCase() + s.slice(1);
  return /[.!?…]$/.test(capitalised) ? capitalised : `${capitalised}.`;
}

/** Whether an error already says there was no answer, so no lead-in says it before it. */
export const saysNoAnswer = (error: string | null): boolean => /^\s*no answer\b/i.test(error ?? "");

/** The test question's answer, as the callout's first line, after the model's name. */
export function testAnswerWords(r: JudgementTestResult): string {
  if (r.ok && r.decision) return ` answered in ${formatDuration(r.latencyMs)}: ${DECISION[r.decision].word}, ${sure(r.confidence)} sure.`;
  const why = errorSentence(r.error ?? "") || "No reason given.";
  if (saysNoAnswer(r.error)) return `: ${why}`;
  return ` gave no answer${r.latencyMs ? ` in ${formatDuration(r.latencyMs)}` : ""}. ${why}`;
}

/** The test's tokens, after "Not recorded in Recent judgements". */
export function testTokensWords(r: Pick<JudgementTestResult, "inputTokens" | "outputTokens">): string {
  const n = r.inputTokens + r.outputTokens;
  return n ? `; ${formatTokens(n)} tokens` : "";
}

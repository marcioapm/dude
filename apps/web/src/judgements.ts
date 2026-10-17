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

/** The test question's answer, as the callout's first line. */
export function testAnswerWords(r: JudgementTestResult): string {
  if (r.ok && r.decision) return `answered in ${formatDuration(r.latencyMs)}: ${DECISION[r.decision].word}, ${sure(r.confidence)} sure.`;
  return `gave no answer${r.latencyMs ? ` in ${formatDuration(r.latencyMs)}` : ""}: ${r.error ?? "no reason given"}.`;
}

/** The test's tokens, after "Not recorded in Recent judgements". */
export function testTokensWords(r: Pick<JudgementTestResult, "inputTokens" | "outputTokens">): string {
  const n = r.inputTokens + r.outputTokens;
  return n ? `; ${formatTokens(n)} tokens` : "";
}

/**
 * What the Judgements page computes: a decision's badge, how sure, how
 * long, how many tokens, and the test question's answer as a line. Pure,
 * so the words are tested apart from the screen.
 */

import type { JudgementCall, JudgementDecision, JudgementTestResult } from "@dude/domain";
import type { IconName } from "@dude/design-system";

export const DECISION: Record<JudgementDecision, { tone: "success" | "danger" | "attention"; icon: IconName; word: string }> = {
  yes: { tone: "success", icon: "check", word: "Yes" },
  no: { tone: "danger", icon: "cross", word: "No" },
  unsure: { tone: "attention", icon: "question", word: "Unsure" },
};

/** How sure, as a percentage: "93%"; "—" for none. */
export const sure = (confidence: number | null): string => (confidence === null ? "—" : `${Math.round(confidence * 100)}%`);

/** How long a call took: "1.8s". */
export const took = (ms: number): string => `${(ms / 1000).toFixed(1)}s`;

/** A call's tokens: "1,204 in · 96 out"; "—" when the proxy counted none. */
export function tokens(c: Pick<JudgementCall, "inputTokens" | "outputTokens">): string {
  if (!c.inputTokens && !c.outputTokens) return "—";
  return `${c.inputTokens.toLocaleString("en")} in · ${c.outputTokens.toLocaleString("en")} out`;
}

/** A call's tokens in and out together, as the table's column: "1,300"; "—" for none. */
export function totalTokens(c: Pick<JudgementCall, "inputTokens" | "outputTokens">): string {
  const n = c.inputTokens + c.outputTokens;
  return n ? n.toLocaleString("en") : "—";
}

/** The test question's answer, as the callout's first line. */
export function testAnswerWords(r: JudgementTestResult): string {
  if (r.ok && r.decision) return `answered in ${took(r.latencyMs)}: ${DECISION[r.decision].word}, ${sure(r.confidence)} sure.`;
  return `gave no answer${r.latencyMs ? ` in ${took(r.latencyMs)}` : ""}: ${r.error ?? "no reason given"}.`;
}

/** The test's tokens, after "Not recorded in Recent judgements". */
export function testTokensWords(r: Pick<JudgementTestResult, "inputTokens" | "outputTokens">): string {
  const n = r.inputTokens + r.outputTokens;
  return n ? `; ${n.toLocaleString("en")} tokens` : "";
}

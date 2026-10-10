/**
 * An agent's question as its form, wired: the person's draft kept in this
 * browser until it is sent, and a question turn's props from the
 * conversation's projection.
 *
 * The draft is per person and per question (localStorage, `dude.answer.<person>.<question>`):
 * a reload, or another task opened and back, finds the picks where they
 * were. Nothing reaches the agent before Send; a sent or settled question's
 * draft is dropped.
 */

import { useCallback, useEffect, useMemo } from "react";
import type { QuestionDraft } from "@dude/design-system/components";
import type { QuestionTurn } from "./api/conversation.ts";

const PREFIX = "dude.answer.";

export function draftKey(person: string | null | undefined, questionId: string): string {
  return `${PREFIX}${person ?? "anyone"}.${questionId}`;
}

/** The kept draft, if any and readable. */
export function readDraft(key: string): QuestionDraft | undefined {
  try {
    const raw = localStorage.getItem(key);
    if (!raw) return undefined;
    const d = JSON.parse(raw) as QuestionDraft;
    return typeof d === "object" && d !== null && Array.isArray(d.answers) ? d : undefined;
  } catch {
    return undefined;
  }
}

export function writeDraft(key: string, draft: QuestionDraft): void {
  try {
    localStorage.setItem(key, JSON.stringify(draft));
  } catch {
    // Storage full or off: the form still works, it just forgets on reload.
  }
}

export function dropDraft(key: string): void {
  try {
    localStorage.removeItem(key);
  } catch {
    // Nothing kept to drop.
  }
}

/**
 * The draft of one question for one person: what to start the form from,
 * how to keep changes, and how to forget it once sent. A question settled
 * (answered here or elsewhere, or ended) drops it.
 */
export function useAnswerDraft(person: string | null | undefined, question: Pick<QuestionTurn, "questionId" | "answeredAt" | "closedAt"> | null) {
  const key = question ? draftKey(person, question.questionId) : null;
  const settled = question !== null && (question.answeredAt !== null || question.closedAt !== null);
  useEffect(() => {
    if (key && settled) dropDraft(key);
  }, [key, settled]);
  const draft = useMemo(() => (key && !settled ? readDraft(key) : undefined), [key, settled]);
  const keep = useCallback((d: QuestionDraft) => {
    if (key) writeDraft(key, d);
  }, [key]);
  const forget = useCallback(() => {
    if (key) dropDraft(key);
  }, [key]);
  return { draft, keep, forget };
}

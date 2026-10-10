/**
 * An agent's question as its form, wired: the person's draft kept in this
 * browser until it is sent, and a question turn's props from the
 * conversation's projection.
 *
 * The draft is per person and per question (localStorage, `dude.answer.<person>.<question>`):
 * a reload, or another task opened and back, finds the picks where they
 * were. Nothing reaches the agent before Send; a draft is dropped once its
 * question is sent, settled, or ended unanswered.
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
 * The draft of one question for one person: its key (the form is keyed on
 * it, so it starts again from the person's own draft once they are known),
 * what to start the form from, how to keep changes, and how to forget it
 * once sent. A question settled (answered here or elsewhere, closed, or
 * `ended` with its Run) drops it — the person's and any kept before they
 * were known.
 */
export function useAnswerDraft(person: string | null | undefined, question: Pick<QuestionTurn, "questionId" | "answeredAt" | "closedAt">, ended: boolean) {
  const key = draftKey(person, question.questionId);
  const settled = ended || question.answeredAt !== null || question.closedAt !== null;
  const anyone = draftKey(null, question.questionId);
  const forget = useCallback(() => {
    dropDraft(key);
    dropDraft(anyone);
  }, [key, anyone]);
  useEffect(() => {
    if (settled) forget();
  }, [settled, forget]);
  // Picks made before the person was known carry over to their own key.
  const draft = useMemo(() => (settled ? undefined : readDraft(key) ?? readDraft(anyone)), [key, anyone, settled]);
  const keep = useCallback((d: QuestionDraft) => writeDraft(key, d), [key]);
  return { key, draft, keep, forget };
}

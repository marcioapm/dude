import { z } from "zod";

/**
 * Judgements: dude asks one model one question and reads back yes, no or
 * unsure, how sure it is and why. The organisation's admins pick the tier
 * that answers (organizations.judgement_tier_id); none is Off. Every call
 * sent is kept in model_calls (migration 141). Nothing asks for one yet.
 */

export const JUDGEMENT_DECISIONS = ["yes", "no", "unsure"] as const;
export type JudgementDecision = (typeof JUDGEMENT_DECISIONS)[number];

/** The question an admin's test asks; the orchestrator's judge.TestQuestion. */
export const JUDGEMENT_TEST_QUESTION = "Is the sky blue on a clear day?";

/** How far back Recent judgements reads. */
export const JUDGEMENT_WINDOW_DAYS = 7;
export const JUDGEMENT_PAGE_MAX = 100;

/** `GET /v1/judgements/settings`. */
export interface JudgementSettings {
  /** null: Off. */
  tierId: string | null;
  canEdit: boolean;
}

/** `PUT /v1/judgements/settings`. */
export const judgementSettingsInputSchema = z.object({ tierId: z.string().min(1).nullable() }).strict();
export type JudgementSettingsInput = z.infer<typeof judgementSettingsInputSchema>;

/** `POST /v1/judgements/test`: the tier as the page has it picked, saved or not. */
export const judgementTestInputSchema = z.object({ tierId: z.string().min(1) }).strict();
export type JudgementTestInput = z.infer<typeof judgementTestInputSchema>;

/** The test question's answer; never recorded. */
export interface JudgementTestResult {
  ok: boolean;
  decision: JudgementDecision | null;
  confidence: number | null;
  reasoning: string | null;
  model: string;
  latencyMs: number;
  inputTokens: number;
  outputTokens: number;
  /** Why there is no answer: the proxy's words, a timeout, or an answer that was not a decision. */
  error: string | null;
}

/** One judgement call as Recent judgements lists it. */
export interface JudgementCall {
  id: string;
  createdAt: string;
  /** The part of dude that asked. */
  asker: string;
  about: string | null;
  /** The question: the first line of the user message sent. */
  question: string;
  /** null when the call failed. */
  decision: JudgementDecision | null;
  confidence: number | null;
  reasoning: string | null;
  status: "ok" | "error";
  error: string | null;
  tierName: string;
  model: string;
  latencyMs: number;
  inputTokens: number;
  outputTokens: number;
}

/** `GET /v1/judgements/:id`: the call with what was sent and what came back. */
export interface JudgementCallDetail extends JudgementCall {
  request: { messages: Array<{ role: string; content: string }> } & Record<string, unknown>;
  responseText: string | null;
}

/** `GET /v1/judgements`. */
export interface JudgementCallsResponse {
  calls: JudgementCall[];
  summary: {
    /** Calls in the window, whatever the filter. */
    calls: number;
    since: string;
  };
  /** Every asker in the window, for the filter. */
  askers: string[];
  /** Pass as `before` for the next page; null on the last. */
  next: string | null;
}

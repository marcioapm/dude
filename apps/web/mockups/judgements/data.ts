/*
 * The judgement mockup's data: Acme's three tiers as they ship (Thinker,
 * Coder, Fast), who uses each, and a day of judgements. The questions are
 * illustrative: nothing in dude asks for a judgement yet.
 */

import type { Person } from "../../../../packages/design-system/src/components/PersonAvatar.tsx";

export const ORG = "Acme";

export const P: Record<string, Person> = {
  marcio: { id: "u_marcio", name: "Márcio Martins", online: true },
};

export interface Tier {
  readonly id: string;
  readonly name: string;
  readonly description: string;
  readonly model: string | null;
  readonly effort: string | null;
  readonly usedBy: string;
  readonly roles: readonly string[];
  readonly changed: string;
}

export const TIERS: readonly Tier[] = [
  { id: "mtr_thinker", name: "Thinker", description: "Reads, plans, judges and tidies. Slow and thorough.", model: "claude-opus-5-5", effort: "high",
    usedBy: "5 agents", roles: ["investigator", "reviewer", "simplifier", "qa_browser", "conductor"], changed: "Márcio · 3 days ago" },
  { id: "mtr_coder", name: "Coder", description: "Writes and fixes code for hours at a time.", model: "claude-fable-5-1", effort: null,
    usedBy: "2 agents", roles: ["implementer"], changed: "Márcio · 3 days ago" },
  { id: "mtr_fast", name: "Fast", description: "Small, mechanical jobs where speed beats depth.", model: "gpt-6-sol-mini", effort: "low",
    usedBy: "Judgements", roles: [], changed: "Márcio · 1 hour ago" },
];

export type Verdict = "yes" | "no" | "unsure";

export interface Judgement {
  readonly id: string;
  readonly at: string;
  /** What asked: the part of dude that wanted the decision. */
  readonly asker: string;
  /** Where it was about. */
  readonly about: string | null;
  readonly question: string;
  readonly verdict: Verdict | null;
  readonly confidence: number | null;
  readonly reasoning: string | null;
  readonly error: string | null;
  readonly tier: string;
  readonly model: string;
  readonly ms: number;
  readonly tokensIn: number;
  readonly tokensOut: number;
  readonly usd: number;
  readonly prompt: string;
  readonly raw: string | null;
}

const ago = (min: number) => new Date(Date.parse("2026-10-17T11:55:00Z") - min * 60_000).toISOString();

export const JUDGEMENTS: readonly Judgement[] = [
  {
    id: "jdg_01", at: ago(2), asker: "Delivery", about: "ABS-28 · abs#5477", tier: "Fast", model: "gpt-6-sol-mini",
    question: "Is this pull-request comment something the fixer should act on?",
    verdict: "no", confidence: 0.93, error: null, ms: 1840, tokensIn: 1_204, tokensOut: 96, usd: 0.0011,
    reasoning: "It is jervasion's status note that approval waits for CI on the exact head. It asks for nothing to change; acting on it would move the head and cancel the CI it is waiting for.",
    prompt: "A reviewer left this comment on the task's pull request. Decide whether the fixer should change the branch because of it.\n\nComment (jervasion, COMMENTED):\n> Approval withheld only pending exact-head CI. <!-- jervasion:ci-deferred -->\n\nAnswer yes if it asks for a change, no if it does not.",
    raw: "{\"decision\":\"no\",\"confidence\":0.93,\"reasoning\":\"It is jervasion's status note…\"}",
  },
  {
    id: "jdg_02", at: ago(14), asker: "Escalations", about: "ABS-31", tier: "Fast", model: "gpt-6-sol-mini",
    question: "Does the implementer's question need a person, or can the conductor answer it?",
    verdict: "yes", confidence: 0.81, error: null, ms: 2310, tokensIn: 2_870, tokensOut: 141, usd: 0.0024,
    reasoning: "It asks which of two billing rules the business wants. The task and its criteria do not say, and the code allows both.",
    prompt: "…", raw: "{\"decision\":\"yes\",\"confidence\":0.81,\"reasoning\":\"…\"}",
  },
  {
    id: "jdg_03", at: ago(41), asker: "CI", about: "ABS-28 · e2e 4", tier: "Fast", model: "gpt-6-sol-mini",
    question: "Is this failing check caused by the branch?",
    verdict: "unsure", confidence: 0.52, error: null, ms: 3105, tokensIn: 6_412, tokensOut: 188, usd: 0.0049,
    reasoning: "The failing test is a known flake on main, but the branch also changes the tooltip it waits for.",
    prompt: "…", raw: "{\"decision\":\"unsure\",\"confidence\":0.52,\"reasoning\":\"…\"}",
  },
  {
    id: "jdg_04", at: ago(63), asker: "Delivery", about: "ABS-27 · abs#5470", tier: "Fast", model: "gpt-6-sol-mini",
    question: "Is this pull-request comment something the fixer should act on?",
    verdict: null, confidence: null, reasoning: null, error: "No answer from the LLM proxy in 30s", ms: 30_000, tokensIn: 0, tokensOut: 0, usd: 0,
    prompt: "…", raw: null,
  },
  {
    id: "jdg_05", at: ago(95), asker: "Steering", about: "Session · Usage-based billing", tier: "Fast", model: "gpt-6-sol-mini",
    question: "Does this message change what the running agent was asked to do?",
    verdict: "yes", confidence: 0.96, error: null, ms: 1420, tokensIn: 980, tokensOut: 74, usd: 0.0009,
    reasoning: "It adds a requirement: invoices must also be exported as CSV.",
    prompt: "…", raw: "{\"decision\":\"yes\",\"confidence\":0.96,\"reasoning\":\"…\"}",
  },
];

export const TEST_QUESTION = "Is the sky blue on a clear day?";

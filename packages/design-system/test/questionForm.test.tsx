/**
 * The question's turn as its answer form, mounted: one question answers in
 * one click or in the person's words; several are tabs, answered then Next,
 * reviewed and sent together, Send off until each is answered; the keys;
 * the draft in and out; and the record an answered turn becomes.
 */

import { afterEach, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QuestionCard, type QuestionDraft, type QuestionItem, type QuestionSubmission } from "../src/components/QuestionCard.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;

afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

async function render(el: React.ReactElement) {
  if (!host) {
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
  }
  await act(async () => root!.render(<TooltipProvider>{el}</TooltipProvider>));
}

const q = <T extends Element = HTMLElement>(sel: string) => document.querySelector<T>(sel);
const all = (sel: string) => [...document.querySelectorAll<HTMLElement>(sel)];
const click = (el: Element | null) => act(async () => void (el as HTMLElement).click());
const key = (el: Element, k: string) => act(async () => void el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true })));
async function type(el: HTMLInputElement | HTMLTextAreaElement, value: string) {
  const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  await act(async () => {
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

const ONE: QuestionItem[] = [{ question: "Retry 4xx?", choices: [{ label: "5xx only", recommended: true }, { label: "Everything" }] }];
const FOUR: QuestionItem[] = [
  { header: "Retry scope", question: "Which failures?", choices: [{ label: "5xx only", description: "A 4xx is our bug.", recommended: true }, { label: "Everything" }] },
  { header: "Old route", question: "What about /pay?", choices: [{ label: "Fold it in" }, { label: "Leave it" }] },
  { header: "Tests", question: "Which layers?", multiple: true, choices: [{ label: "Unit" }, { label: "API" }, { label: "Browser" }] },
  { header: "Button", question: "What should it say?", choices: [{ label: "Split payment" }, { label: "Pay in parts" }] },
];

function form(items: QuestionItem[], sent: QuestionSubmission[], extra: Partial<React.ComponentProps<typeof QuestionCard>> = {}) {
  return <QuestionCard role="implementer" text="?" items={items} askedAt={Date.now() - 1000} onSubmit={(s) => void sent.push(s)} {...extra} />;
}

test("one question: a click on a choice is the answer; the suggestion is said, not picked", async () => {
  const sent: QuestionSubmission[] = [];
  await render(form(ONE, sent));
  expect(q('[role="tablist"]')).toBeNull();
  const radios = all('[role="radio"]');
  expect(radios.map((r) => r.textContent)).toEqual(["5xx onlyagent suggests", "Everything2", "Something else…3"]);
  expect(radios.every((r) => r.getAttribute("aria-checked") === "false")).toBe(true);
  // The choices are labelled by the question itself.
  const labelledBy = q('[role="radiogroup"]')!.getAttribute("aria-labelledby")!;
  expect(document.getElementById(labelledBy)?.textContent).toBe("Retry 4xx?");
  await click(radios[1]!);
  expect(sent).toEqual([{ answers: [{ choices: [1], text: "" }], note: "", attachmentIds: [] }]);
});

test("one question: Something else… opens a field in place, focused; Enter sends the words", async () => {
  const sent: QuestionSubmission[] = [];
  await render(form(ONE, sent));
  await click(q('[data-choice="own"]'));
  const field = q<HTMLInputElement>("[data-own-field]")!;
  expect(document.activeElement).toBe(field);
  expect(q<HTMLButtonElement>('[data-testid="question-answer"]')!.disabled).toBe(true);
  await type(field, "Retry 409 once");
  // Typing a digit in the field is words, not a pick.
  await key(field, "1");
  expect(sent).toEqual([]);
  await key(field, "Enter");
  expect(sent).toEqual([{ answers: [{ choices: [], text: "Retry 409 once" }], note: "", attachmentIds: [] }]);
});

test("one question with no choices is the field alone", async () => {
  const sent: QuestionSubmission[] = [];
  await render(form([{ question: "Which locale?" }], sent));
  expect(all('[role="radio"]')).toHaveLength(0);
  const field = q<HTMLInputElement>("[data-own-field]")!;
  await type(field, "pt-PT");
  await click(q('[data-testid="question-answer"]'));
  expect(sent).toEqual([{ answers: [{ choices: [], text: "pt-PT" }], note: "", attachmentIds: [] }]);
});

test("one question with several allowed: checkboxes, then Answer", async () => {
  const sent: QuestionSubmission[] = [];
  await render(form([FOUR[2]!], sent));
  const boxes = all('[role="checkbox"]');
  expect(boxes).toHaveLength(4);
  await click(boxes[2]!);
  await click(boxes[0]!);
  expect(sent).toEqual([]);
  expect(boxes[0]!.getAttribute("aria-checked")).toBe("true");
  await click(q('[data-testid="question-answer"]'));
  expect(sent).toEqual([{ answers: [{ choices: [0, 2], text: "" }], note: "", attachmentIds: [] }]);
});

test("several: picks do not send; Next and Back move; answered tabs are checked; Send waits for every answer", async () => {
  const sent: QuestionSubmission[] = [];
  const drafts: QuestionDraft[] = [];
  await render(form(FOUR, sent, { onDraftChange: (d) => void drafts.push(d) }));
  const tabs = () => all('[role="tab"]');
  expect(tabs().map((t) => t.textContent)).toEqual(["Retry scope", "Old route", "Tests", "Button", "Send · 0/4"]);
  expect(tabs()[0]!.getAttribute("aria-selected")).toBe("true");
  await click(all('[role="radio"]')[0]!);
  expect(sent).toEqual([]);
  expect(tabs()[0]!.dataset.done).toBe("true");
  await click(q('[data-testid="question-next"]'));
  expect(tabs()[1]!.getAttribute("aria-selected")).toBe("true");
  // Focus lands in the new tab's choices.
  expect(document.activeElement?.getAttribute("role")).toBe("radio");
  await click(q('[data-testid="question-back"]'));
  expect(tabs()[0]!.getAttribute("aria-selected")).toBe("true");
  await click(tabs()[2]!);
  await click(all('[role="checkbox"]')[0]!);
  await click(all('[role="checkbox"]')[1]!);
  // Several answered, one missing: the review says which, and Send is off.
  await click(tabs()[4]!);
  expect(all('[data-testid="review-answer"]').map((a) => a.textContent)).toEqual(["5xx only", "Not answered yet", "Unit; API", "Not answered yet"]);
  expect(q<HTMLButtonElement>('[data-testid="question-send"]')!.disabled).toBe(true);
  expect(q('[data-testid="question-hint"]')!.textContent).toBe("2 still to answer");
  await click(tabs()[1]!);
  await click(all('[role="radio"]')[1]!);
  await click(tabs()[3]!);
  await click(q('[data-choice="own"]'));
  await type(q<HTMLInputElement>("[data-own-field]")!, "Pay with two cards");
  // Enter in the field is Next: to the review.
  await key(q("[data-own-field]")!, "Enter");
  expect(tabs()[4]!.getAttribute("aria-selected")).toBe("true");
  expect(tabs()[4]!.textContent).toBe("Send · 4/4");
  await type(q<HTMLTextAreaElement>('[data-testid="question-note"]')!, "Keep it under 10s.");
  expect(q<HTMLButtonElement>('[data-testid="question-send"]')!.disabled).toBe(false);
  await click(q('[data-testid="question-send"]'));
  expect(sent).toEqual([{
    answers: [{ choices: [0], text: "" }, { choices: [1], text: "" }, { choices: [0, 1], text: "" }, { choices: [], text: "Pay with two cards" }],
    note: "Keep it under 10s.", attachmentIds: [],
  }]);
  expect(drafts.at(-1)).toMatchObject({ tab: 4, note: "Keep it under 10s." });
});

test("the keys: a number picks, arrows move between questions, Enter is Next", async () => {
  const sent: QuestionSubmission[] = [];
  await render(form(FOUR, sent));
  const f = q('[data-testid="question-form"]')!;
  await key(f, "2");
  expect(all('[role="radio"]')[1]!.getAttribute("aria-checked")).toBe("true");
  await key(f, "ArrowRight");
  expect(all('[role="tab"]')[1]!.getAttribute("aria-selected")).toBe("true");
  await key(f, "ArrowLeft");
  expect(all('[role="tab"]')[0]!.getAttribute("aria-selected")).toBe("true");
  await key(f, "Enter");
  expect(all('[role="tab"]')[1]!.getAttribute("aria-selected")).toBe("true");
  // 3 on a question of two choices is Something else…, opened and focused.
  await key(f, "3");
  expect(document.activeElement).toBe(q("[data-own-field]"));
  expect(sent).toEqual([]);
});

test("a kept draft comes back as it was left", async () => {
  const sent: QuestionSubmission[] = [];
  await render(form(FOUR, sent, { draft: { tab: 2, answers: [{ choices: [1], own: null }, { choices: [], own: "Later" }, { choices: [2], own: null }, { choices: [], own: null }], note: "n" } }));
  expect(all('[role="tab"]')[2]!.getAttribute("aria-selected")).toBe("true");
  expect(all('[role="checkbox"]')[2]!.getAttribute("aria-checked")).toBe("true");
  expect(all('[role="tab"]').map((t) => t.dataset.done ?? "")).toEqual(["true", "true", "true", "", ""]);
});

test("answered, the turn is the record: each question with its answer, own words marked as the person's", async () => {
  await render(<QuestionCard role="implementer" text="?" items={FOUR} askedAt="2026-10-10T11:04:00Z" answeredAt="2026-10-10T11:10:05Z" answeredBy="marcio"
    answers={[{ choices: [0], text: "" }, { choices: [1], text: "" }, { choices: [0, 1], text: "" }, { choices: [], text: "Pay with two cards" }]} />);
  expect(q("article")!.dataset.state).toBe("answered");
  expect(q("header")!.textContent).toContain("asked 4 questions");
  expect(q("header")!.textContent).toContain("by marcio, after");
  expect(all('[data-testid="record-answer"]').map((a) => a.textContent)).toEqual(["5xx only", "Leave it", "Unit; API", "“Pay with two cards”in marcio's words"]);
  expect(q('[data-testid="question-form"]')).toBeNull();
});

test("someone else's: no form, the choices muted, how to take it over", async () => {
  await render(form(FOUR, [], { waitingOn: "Ana" }));
  expect(q('[data-testid="question-form"]')).toBeNull();
  expect(q('[data-testid="waiting-on"]')!.textContent).toBe("Waiting for Ana to answer · Take over this task to answer");
});

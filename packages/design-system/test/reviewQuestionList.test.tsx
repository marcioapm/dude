/**
 * ReviewQuestionList: each open question with its fields and the finding
 * it is about, and nothing at all when there are none. Mounted in happy-dom.
 */

import { afterEach, expect, test } from "bun:test";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ReviewQuestionList } from "../src/components/ReviewQuestionList.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
});

async function mount(node: ReactNode): Promise<HTMLElement> {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(node));
  return host;
}

const text = (el: Element | null | undefined) => el?.textContent?.replace(/\s+/g, " ").trim() ?? "";

test("each question reads with who can answer it, what it is about, why it matters and what was checked", async () => {
  const el = await mount(<ReviewQuestionList questions={[
    { id: "q1", category: "correctness", question: "Do invoices close in UTC?", neededFrom: "product",
      about: <a href="#f1" data-testid="about-link">Totals off by a cent</a>, whyItMatters: "Decides F1.", evidenceChecked: "Read the config." },
    { id: "q2", category: "security", question: "Who may download it?" },
  ]} />);
  const items = [...el.querySelectorAll("[data-question]")];
  expect(items.map((i) => i.getAttribute("data-question"))).toEqual(["q1", "q2"]);
  const fields = [...items[0]!.querySelectorAll("dt")].map((dt) => [text(dt), text(dt.nextElementSibling)]);
  expect(fields).toEqual([["Needed from", "product"], ["About", "Totals off by a cent"], ["Why it matters", "Decides F1."], ["Checked", "Read the config."]]);
  expect(text(items[0]!)).toContain("Do invoices close in UTC?");
  expect(items[0]!.querySelectorAll("[data-testid=about-link]").length).toBe(1);
  expect(items[1]!.querySelectorAll("dt").length).toBe(0);
  expect(text(el.querySelector("header"))).toBe("Questions from reviewers2 open");
});

test("none: nothing rendered", async () => {
  const el = await mount(<ReviewQuestionList data-testid="rq" questions={[]} />);
  expect(el.querySelectorAll("[data-testid=rq]").length).toBe(0);
  expect(el.childElementCount).toBe(0);
});

const setText = async (el: HTMLTextAreaElement, value: string) => {
  await act(async () => {
    const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
    set.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
};
const press = async (el: Element) => {
  await act(async () => (el as HTMLElement).click());
};

test("the owner answers: Answer stays disabled until there is text, and sends it trimmed", async () => {
  const sent: Array<[string, string]> = [];
  const el = await mount(<ReviewQuestionList questions={[{ id: "q1", category: "correctness", question: "UTC?" }]}
    onAnswer={async (id, a) => { sent.push([id, a]); }} onWithdraw={async () => {}} />);
  const send = el.querySelector<HTMLButtonElement>("[data-testid=review-question-answer-send]")!;
  expect(send.disabled).toBe(true);
  await setText(el.querySelector<HTMLTextAreaElement>("[data-testid=review-question-answer-text]")!, "  In UTC.  ");
  expect(send.disabled).toBe(false);
  await press(send);
  expect(sent).toEqual([["q1", "In UTC."]]);
});

test("withdraw asks why first, and is refused without a reason", async () => {
  const withdrawn: Array<[string, string]> = [];
  const el = await mount(<ReviewQuestionList questions={[{ id: "q1", category: "correctness", question: "UTC?" }]}
    onAnswer={async () => {}} onWithdraw={async (id, r) => { withdrawn.push([id, r]); }} />);
  await press(el.querySelector("[data-testid=review-question-withdraw]")!);
  const confirm = el.querySelector<HTMLButtonElement>("[data-testid=review-question-withdraw-confirm]")!;
  expect(confirm.disabled).toBe(true);
  await press(confirm);
  expect(withdrawn).toEqual([]);
  await setText(el.querySelector<HTMLTextAreaElement>("[data-testid=review-question-reason]")!, "The code is gone.");
  await press(confirm);
  expect(withdrawn).toEqual([["q1", "The code is gone."]]);
});

test("anyone but the owner sees the form disabled, saying why", async () => {
  const el = await mount(<ReviewQuestionList questions={[{ id: "q1", category: "correctness", question: "UTC?" }]}
    onAnswer={async () => {}} onWithdraw={async () => {}} notAllowed="Only Ana answers." />);
  expect(el.querySelector<HTMLTextAreaElement>("[data-testid=review-question-answer-text]")!.disabled).toBe(true);
  expect(el.querySelector<HTMLButtonElement>("[data-testid=review-question-withdraw]")!.disabled).toBe(true);
  expect(text(el.querySelector("[data-testid=review-question-not-allowed]"))).toBe("Only Ana answers.");
});

test("a refused answer says why and keeps the text", async () => {
  const el = await mount(<ReviewQuestionList questions={[{ id: "q1", category: "correctness", question: "UTC?" }]}
    onAnswer={async () => { throw new Error("reviewer question q1 is answered already"); }} onWithdraw={async () => {}} />);
  const box = el.querySelector<HTMLTextAreaElement>("[data-testid=review-question-answer-text]")!;
  await setText(box, "In UTC.");
  await press(el.querySelector("[data-testid=review-question-answer-send]")!);
  expect(text(el.querySelector("[role=alert]"))).toBe("reviewer question q1 is answered already");
  expect(box.value).toBe("In UTC.");
});

test("an answered question folds to its answer and who gave it; a withdrawn one is left out; no form without handlers", async () => {
  const el = await mount(<ReviewQuestionList questions={[
    { id: "q1", category: "correctness", question: "UTC?", status: "answered", answer: "In UTC.", answeredBy: { kind: "person", name: "Ana" } },
    { id: "q2", category: "api", question: "v1?", status: "answered", answer: "No.", answeredBy: { kind: "conductor" } },
    { id: "q3", category: "tests", question: "Old?", status: "withdrawn" },
    { id: "q4", category: "tests", question: "New?" },
  ]} />);
  const items = [...el.querySelectorAll("[data-question]")];
  expect(items.map((i) => [i.getAttribute("data-question"), i.getAttribute("data-status")])).toEqual([["q1", "answered"], ["q2", "answered"], ["q4", "open"]]);
  expect(text(items[0]!.querySelector("[data-testid=review-question-answered-by]"))).toBe("Ana answered:");
  expect(text(items[0]!.querySelector("[data-testid=review-question-answer]"))).toBe("In UTC.");
  expect(text(items[1]!.querySelector("[data-testid=review-question-answered-by]"))).toBe("The conductor answered:");
  expect(items[0]!.querySelectorAll("dt").length).toBe(0);
  expect(el.querySelectorAll("[data-testid=review-question-form]").length).toBe(0);
  expect(text(el.querySelector("header"))).toBe("Questions from reviewers1 open");
});

/**
 * The Findings tab's "Questions from reviewers": the attempt's questions
 * above the findings, each open one with its fields, the finding it is
 * about by title and, for the owner, a form to answer or withdraw it; an
 * answered one folded to its answer; nothing when none is left to show.
 */

import { afterEach, beforeEach, expect, spyOn, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { act, click, facetCheckbox, mount, settle, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { REVIEW_QUESTIONS, TASK_ID } from "../src/fixtures/data.ts";
import { PeopleProvider } from "../src/people.tsx";
import { TaskScreen } from "../src/screens/TaskScreen.tsx";

let mounted: Array<() => Promise<void>> = [];
beforeEach(() => localStorage.clear());
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  localStorage.clear();
});

async function findingsTab(client: FixtureClient, attempt?: number) {
  const { container, unmount } = await mount(
    <TooltipProvider><ToastProvider><PeopleProvider client={client}>
      <TaskScreen client={client} taskId={TASK_ID} tab="findings" attempt={attempt} onOpenRun={() => {}} onBack={() => {}} />
    </PeopleProvider></ToastProvider></TooltipProvider>,
  );
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=task-screen]"), "the task page");
  await settle();
  return container;
}

const text = (el: Element | null | undefined) => el?.textContent?.replace(/\s+/g, " ").trim() ?? "";

test("the open question shows above the findings with its fields and the finding it is about; a withdrawn one does not", async () => {
  const page = await findingsTab(new FixtureClient("d"));
  const list = await until(() => page.querySelector<HTMLElement>("[data-testid=review-questions]"), "the reviewers' questions");
  const items = [...list.querySelectorAll("[data-question]")];
  expect(items.map((i) => i.getAttribute("data-question"))).toEqual(["rvq_1"]);
  const fields = [...items[0]!.querySelectorAll("dt")].map((dt) => [text(dt), text(dt.nextElementSibling)]);
  expect(fields).toEqual([
    ["Needed from", "product"],
    ["About", "Invoice option shown on monthly plans"],
    ["Why it matters", "f1 is blocking only if monthly plans must never see it."],
    ["Checked", "Read PaymentStep.tsx and the plan settings; neither says."],
  ]);
  expect(text(items[0])).toContain("Is the invoice option meant for annual plans only?");
  expect(items[0]!.querySelectorAll("[data-testid=review-question-about]").length).toBe(1);
  // Above the findings; the owner (the fixture's reader) can answer it.
  const findings = page.querySelector("[data-testid=findings]")!;
  expect(list.compareDocumentPosition(findings) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  expect(items[0]!.querySelectorAll("[data-testid=review-question-form] textarea").length).toBe(1);
});

test("about scrolls only its referenced finding", async () => {
  const page = await findingsTab(new FixtureClient("d"));
  const target = spyOn(page.querySelector<HTMLElement>('[data-finding="f1"]')!, "scrollIntoView");
  const other = spyOn(page.querySelector<HTMLElement>('[data-finding="f4"]')!, "scrollIntoView");
  try {
    await click(page.querySelector('[data-testid="review-question-about"]')!);
    expect(target).toHaveBeenCalledTimes(1);
    expect(target).toHaveBeenCalledWith({ block: "center" });
    expect(other).not.toHaveBeenCalled();
  } finally {
    target.mockRestore();
    other.mockRestore();
  }
});

for (const facet of [
  { group: "Finding status", label: "Open" },
  { group: "Finding severity", label: "Note" },
]) test(`about reveals and scrolls only its finding hidden by ${facet.group}`, async () => {
  const page = await findingsTab(new FixtureClient("d"));
  await click(facetCheckbox(page.querySelector(`[aria-label="${facet.group}"]`)!, facet.label));
  expect(page.querySelectorAll('[data-finding="f1"]').length).toBe(0);
  const otherRow = page.querySelector<HTMLElement>('[data-finding="f4"]')!;
  Object.defineProperty(otherRow, "scrollIntoView", { configurable: true, writable: true, value: () => {} });
  const other = spyOn(otherRow, "scrollIntoView");
  const scroll = spyOn(HTMLElement.prototype, "scrollIntoView");
  try {
    await click(page.querySelector('[data-testid="review-question-about"]')!);
    const target = page.querySelector<HTMLElement>('[data-finding="f1"]');
    expect(page.querySelectorAll('[data-finding="f1"]').length).toBe(1);
    expect(scroll).toHaveBeenCalledTimes(1);
    expect(scroll).toHaveBeenCalledWith({ block: "center" });
    expect(scroll.mock.contexts).toEqual([target]);
    expect(other).not.toHaveBeenCalled();
  } finally {
    other.mockRestore();
    scroll.mockRestore();
  }
});

for (const attempt of [1, 2]) test(`attempt ${attempt} shows only its own open reviewer question`, async () => {
  const client = new FixtureClient("d");
  const task = await client.getTask(TASK_ID);
  const review = task.runs.find((run) => run.id === "run_d2")!;
  client.getTask = async () => ({ ...task, runs: [
    ...task.runs.map((run) => ({ ...run, attempt: 1 })),
    { ...review, id: "run_second_review", attempt: 2 },
  ] });
  client.reviewQuestions = async () => ({ questions: [
    { ...REVIEW_QUESTIONS[0]!, id: "question_first", runId: review.id, question: "First attempt question?" },
    { ...REVIEW_QUESTIONS[0]!, id: "question_second", runId: "run_second_review", question: "Second attempt question?" },
  ] });
  const page = await findingsTab(client, attempt);
  const questions = [...page.querySelectorAll('[data-testid="review-questions"] [data-question]')];
  expect(questions.map((q) => q.getAttribute("data-question"))).toEqual([attempt === 1 ? "question_first" : "question_second"]);
  expect(text(questions[0])).toContain(attempt === 1 ? "First attempt question?" : "Second attempt question?");
});

test("with no open question, nothing is shown", async () => {
  const client = new FixtureClient("d");
  client.reviewQuestions = async () => ({ questions: [] });
  const page = await findingsTab(client);
  expect(page.querySelectorAll("[data-testid=review-questions]").length).toBe(0);
});

async function typeArea(el: Element, text: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, text);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

test("the owner answers: the forwarder is called, and the question then shows its answer, folded", async () => {
  const client = new FixtureClient("d");
  const sent: Array<[string, string]> = [];
  const answer = client.answerReviewQuestion.bind(client);
  client.answerReviewQuestion = (id, text) => { sent.push([id, text]); return answer(id, text); };
  const page = await findingsTab(client);
  const item = await until(() => page.querySelector('[data-testid=review-questions] [data-question="rvq_1"]'), "the open question");
  await typeArea(item.querySelector("[data-testid=review-question-answer-text]")!, "Annual plans only.");
  await click(item.querySelector("[data-testid=review-question-answer-send]")!);
  const answered = await until(() => page.querySelector('[data-question="rvq_1"][data-status="answered"]'), "the answered question");
  expect(sent).toEqual([["rvq_1", "Annual plans only."]]);
  expect(text(answered.querySelector("[data-testid=review-question-answer]"))).toBe("Annual plans only.");
  expect(text(answered.querySelector("[data-testid=review-question-answered-by]"))).toBe("A person answered:");
  expect(answered.querySelectorAll("textarea").length).toBe(0);
});

test("withdraw needs a reason, then calls the forwarder and the question leaves the list", async () => {
  const client = new FixtureClient("d");
  const withdrawn: Array<[string, string]> = [];
  const withdraw = client.withdrawReviewQuestion.bind(client);
  client.withdrawReviewQuestion = (id, reason) => { withdrawn.push([id, reason]); return withdraw(id, reason); };
  const page = await findingsTab(client);
  const item = await until(() => page.querySelector('[data-testid=review-questions] [data-question="rvq_1"]'), "the open question");
  await click(item.querySelector("[data-testid=review-question-withdraw]")!);
  const confirm = item.querySelector<HTMLButtonElement>("[data-testid=review-question-withdraw-confirm]")!;
  expect(confirm.disabled).toBe(true);
  await typeArea(item.querySelector("[data-testid=review-question-reason]")!, "Invoices are gone from checkout.");
  await click(confirm);
  await settle();
  expect(withdrawn).toEqual([["rvq_1", "Invoices are gone from checkout."]]);
  expect(page.querySelectorAll("[data-testid=review-questions]").length).toBe(0);
});

test("anyone but the owner sees the form disabled, saying whose it is", async () => {
  const client = new FixtureClient("d");
  const task = await client.getTask(TASK_ID);
  client.getTask = async () => ({ ...task, owner: { ...task.owner!, id: "u_ana", name: "Ana Souza" } });
  const page = await findingsTab(client);
  const item = await until(() => page.querySelector('[data-testid=review-questions] [data-question="rvq_1"]'), "the open question");
  expect(item.querySelector<HTMLTextAreaElement>("[data-testid=review-question-answer-text]")!.disabled).toBe(true);
  expect(text(item.querySelector("[data-testid=review-question-not-allowed]"))).toBe("Only Ana, its owner, answers its reviewers' questions.");
});

test("a task held by an open question says so where it waits on a person", async () => {
  const client = new FixtureClient("d");
  const task = await client.getTask(TASK_ID);
  client.getTask = async () => ({ ...task, status: "awaiting_input", escalation: null, heldByQuestions: true });
  const page = await findingsTab(client);
  const hold = await until(() => page.querySelector("[data-testid=questions-hold]"), "the hold notice");
  expect(text(hold)).toContain("A reviewer's question holds the pull request.");
  await click(page.querySelector('[data-testid=review-questions] [data-question="rvq_1"] [data-testid=review-question-withdraw]')!);
  await typeArea(page.querySelector("[data-testid=review-question-reason]")!, "Moot.");
  await click(page.querySelector("[data-testid=review-question-withdraw-confirm]")!);
  await settle();
  expect(page.querySelectorAll("[data-testid=questions-hold]").length).toBe(0);
});

for (const resolution of [null, "wont_do"] as const) test(`an aborted task ${resolution ? "closed" : "stopped"} shows its reviewers' questions appropriately`, async () => {
  const client = new FixtureClient("d");
  const task = await client.getTask(TASK_ID);
  client.getTask = async () => ({ ...task, status: "aborted", resolution, heldByQuestions: false });
  const page = await findingsTab(client);
  const list = await until(() => page.querySelector('[data-testid=review-questions]'), "the reviewers' questions");
  expect(text(list.querySelector("header"))).toContain(resolution ? "1 unanswered when it closed" : "1 open");
  expect(list.querySelectorAll("[data-testid=review-question-form]").length).toBe(resolution ? 0 : 1);
  expect(list.querySelectorAll("[data-question]").length).toBe(1);
  expect(text(list)).toContain("Is the invoice option meant for annual plans only?");
  expect(page.querySelectorAll("[data-testid=questions-hold]").length).toBe(0);
});

test("an agent's question asked while a reviewer's question is open shows no hold notice", async () => {
  const client = new FixtureClient("d");
  const task = await client.getTask(TASK_ID);
  // Waiting on a person, with the reviewer's question open, but the delivery is not parked by it.
  client.getTask = async () => ({ ...task, status: "awaiting_input", escalation: null, heldByQuestions: false });
  const page = await findingsTab(client);
  await until(() => page.querySelector('[data-testid=review-questions] [data-question="rvq_1"]'), "the open question");
  expect(page.querySelectorAll("[data-testid=questions-hold]").length).toBe(0);
});

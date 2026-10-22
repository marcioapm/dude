/**
 * The Findings tab's "Questions from reviewers": the attempt's open
 * questions above the findings, each with its fields and the finding it is
 * about by title; nothing when none is open.
 */

import { afterEach, beforeEach, expect, spyOn, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { click, facetCheckbox, mount, settle, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { TASK_ID } from "../src/fixtures/data.ts";
import { PeopleProvider } from "../src/people.tsx";
import { TaskScreen } from "../src/screens/TaskScreen.tsx";

let mounted: Array<() => Promise<void>> = [];
beforeEach(() => localStorage.clear());
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  localStorage.clear();
});

async function findingsTab(client: FixtureClient) {
  const { container, unmount } = await mount(
    <TooltipProvider><ToastProvider><PeopleProvider client={client}>
      <TaskScreen client={client} taskId={TASK_ID} tab="findings" onOpenRun={() => {}} onBack={() => {}} />
    </PeopleProvider></ToastProvider></TooltipProvider>,
  );
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=task-screen]"), "the task page");
  await until(() => container.querySelector("[data-testid=findings]"), "the findings");
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
  // Above the findings, and no control to answer it.
  const findings = page.querySelector("[data-testid=findings]")!;
  expect(list.compareDocumentPosition(findings) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  expect(list.querySelectorAll("textarea, input").length).toBe(0);
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

test("with no open question, nothing is shown", async () => {
  const client = new FixtureClient("d");
  client.reviewQuestions = async () => ({ questions: [] });
  const page = await findingsTab(client);
  expect(page.querySelectorAll("[data-testid=review-questions]").length).toBe(0);
});

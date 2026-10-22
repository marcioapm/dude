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

/**
 * A proposed task is read on its card: its goal clamped to three lines
 * behind Show all (only when it overflows), its acceptance criteria in a
 * closed disclosure under it. Other kinds draw their detail as before.
 * Mounted in happy-dom, with layout given by a stubbed ResizeObserver.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ProposalCard, ProposalWaiting, foldedWords, nothingLeftToFile, toFileWords, yoursToFile, type ProposalCardItem } from "../src/components/Brainstorm.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
});

async function mount(el: React.ReactElement): Promise<HTMLElement> {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(el));
  return host;
}

/** Layout as a browser would give it: 20px lines; a text past 300 characters is 8 lines tall. */
async function withLayout(run: (measure: () => Promise<void>) => Promise<void>) {
  const original = globalThis.ResizeObserver;
  const measures: Array<() => void> = [];
  globalThis.ResizeObserver = class {
    constructor(private callback: ResizeObserverCallback) {}
    observe(target: Element) {
      for (const el of [target, target.firstElementChild]) if (el) (el as HTMLElement).style.lineHeight = "20px";
      Object.defineProperty(target, "scrollHeight", { configurable: true, get: () => ((target.textContent?.length ?? 0) > 300 ? 160 : 40) });
      measures.push(() => this.callback([], this as unknown as ResizeObserver));
    }
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
  try {
    await run(() => act(async () => measures.forEach((m) => m())));
  } finally {
    globalThis.ResizeObserver = original;
  }
}

const LONG_GOAL = "The nightly's coverage step fails before any test runs: setUpClass inserts a goal into the analytics table, and the warehouse refuses it "
  + "with a NULL in copy_index. The table is cloned with LIKE, which keeps NOT NULL but drops the source's DEFAULT 0, so a row inserted without copy_index "
  + "is refused. Clone it with the source table's defaults, then dispatch the nightly on the fix branch to show the step gets past setup.";
const CRITERIA = ["The test creates the table with the source's column defaults.", "The nightly gets past setUpClass.", "No change to the production schema."];
const project = { key: "AN", name: "Analytics" };

function Card({ items }: { readonly items: ProposalCardItem[] }) {
  return <ProposalCard items={items} selected={new Set()} filingAs="Ana" onToggle={() => undefined} onFile={() => undefined} />;
}
const button = (el: HTMLElement, name: string) => [...el.querySelectorAll("button")].find((b) => b.textContent === name);

describe("ProposalCard task items", () => {
  test("a long goal is clamped to three lines with Show all, which toggles aria-expanded", async () => {
    await withLayout(async (measure) => {
      const el = await mount(<Card items={[{ kind: "task", title: "Clone with defaults", detail: LONG_GOAL, criteria: CRITERIA, project, canFile: true }]} />);
      await measure();
      const goal = el.querySelector('[data-testid="item-goal"]')!;
      expect(goal.textContent).toBe(LONG_GOAL);
      const inner = goal.parentElement as HTMLElement;
      expect(inner.style.maxHeight).toBe("60px");
      const show = button(el, "Show all")!;
      expect(show.getAttribute("aria-expanded")).toBe("false");
      await act(async () => show.click());
      expect(button(el, "Show all")).toBeUndefined();
      expect(button(el, "Show less")!.getAttribute("aria-expanded")).toBe("true");
      expect(inner.style.maxHeight).toBe("");
      await act(async () => button(el, "Show less")!.click());
      expect(inner.style.maxHeight).toBe("60px");
    });
  });

  test("a short goal has no toggle and renders whole", async () => {
    await withLayout(async (measure) => {
      const el = await mount(<Card items={[{ kind: "task", title: "Short", detail: "Find which fallback ran.", project, canFile: true }]} />);
      await measure();
      expect(el.querySelector('[data-testid="item-goal"]')!.textContent).toBe("Find which fallback ran.");
      expect(button(el, "Show all")).toBeUndefined();
      expect((el.querySelector('[data-testid="item-goal"]')!.parentElement as HTMLElement).style.maxHeight).toBe("");
    });
  });

  test("the acceptance criteria are a closed disclosure titled with their count, listing each", async () => {
    const el = await mount(<Card items={[{ kind: "task", title: "Clone with defaults", detail: "Short.", criteria: CRITERIA, project, canFile: true }]} />);
    const details = el.querySelector<HTMLDetailsElement>('[data-testid="item-criteria"]')!;
    expect(details.tagName).toBe("DETAILS");
    expect(details.open).toBe(false);
    expect(details.querySelector("summary")!.textContent).toBe("Acceptance criteria3");
    expect([...details.querySelectorAll("li")].map((li) => li.textContent)).toEqual(CRITERIA);
  });

  test("other kinds draw their detail as before: no clamp, no criteria", async () => {
    await withLayout(async (measure) => {
      const el = await mount(<Card items={[
        { kind: "epic", title: "Nightlies", detail: LONG_GOAL, criteria: CRITERIA, project, canFile: true },
        { kind: "comment", title: "Comment on AN-1", taskKey: "AN-1", detail: LONG_GOAL, canFile: true },
      ]} />);
      await measure();
      expect(el.querySelectorAll('[data-testid="item-goal"]').length).toBe(0);
      expect(el.querySelectorAll('[data-testid="item-criteria"]').length).toBe(0);
      expect(button(el, "Show all")).toBeUndefined();
      expect(el.textContent!.split(LONG_GOAL).length - 1).toBe(2);
    });
  });
});

describe("ProposalCard replaced by a later proposal", () => {
  const replaced = (title: string): ProposalCardItem =>
    ({ kind: "task", title, project, canFile: false, replaced: true, why: "Replaced by a later proposal" });

  test("its unfiled items fold it to its line, which says so; filed ones stay filed", async () => {
    const el = await mount(<Card items={[replaced("a"), replaced("b")]} />);
    expect(el.querySelector('[data-testid="proposal-fold"]')!.textContent).toBe("Proposed work · 2 tasks · replaced by a later proposal");
    expect(el.querySelectorAll('[data-testid="file-proposal"]').length).toBe(0);
    expect(nothingLeftToFile([replaced("a")])).toBe(true);
    expect(yoursToFile([replaced("a")])).toEqual([]);
    const done: ProposalCardItem = { kind: "task", title: "c", project, canFile: true, filed: { by: "Ana", key: "AN-123" } };
    expect(foldedWords([done, replaced("a")])).toEqual(["1 filed", "1 replaced by a later proposal"]);
    expect(foldedWords([done, replaced("a"), { kind: "edit", title: "e", canFile: false, blockedFor: "you" }]))
      .toEqual(["1 filed", "1 for others to file", "1 replaced by a later proposal"]);
  });
});

describe("ProposalWaiting", () => {
  const mine: ProposalCardItem = { kind: "task", title: "a", project, canFile: true };
  const theirs: ProposalCardItem = { kind: "edit", title: "Edit AN-1", taskKey: "AN-1", canFile: false, blockedFor: "you", why: "Only Márcio can file this" };
  const done: ProposalCardItem = { ...mine, filed: { by: "Ana", key: "AN-123" } };

  test("what waits on the person looking: their unfiled items, none for a reader or what is someone else's", () => {
    expect(toFileWords([mine, mine])).toBe("2 tasks");
    expect(toFileWords([mine, { ...theirs, canFile: true }])).toBe("1 task, 1 edit");
    expect(toFileWords([done, mine])).toBe("1 task");
    expect(toFileWords([done, theirs])).toBe("");
    expect(toFileWords([mine], true)).toBe("");
  });

  test("the band: the waiting mark's words above the card, which sits in it unchanged", async () => {
    const el = await mount(<ProposalWaiting toFile="2 tasks"><Card items={[mine, mine]} /></ProposalWaiting>);
    const band = el.querySelector('[data-testid="proposal-waiting"]')!;
    expect(band.getAttribute("aria-label")).toBe("Proposed work waiting for you to file: 2 tasks");
    expect(band.querySelector('[data-testid="proposal-waiting-mark"]')!.textContent).toBe("Waiting for you to file 2 tasks");
    expect(band.querySelector('[data-testid="proposal-card"]')).toBeTruthy();
  });
});

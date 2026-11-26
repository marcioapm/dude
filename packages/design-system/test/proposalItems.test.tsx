/**
 * A proposed task is read on its card: its goal clamped to three lines
 * behind Show all (only when it overflows), its acceptance criteria in a
 * closed disclosure under it. Other kinds draw their detail as before.
 * Mounted in happy-dom, with layout given by a stubbed ResizeObserver.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ProposalCard, type ProposalCardItem } from "../src/components/Brainstorm.tsx";

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

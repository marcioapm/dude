import { afterEach, describe, expect, test } from "bun:test";
import { Timeline, TimelineItem } from "@dude/design-system/components";
import { GitHubBody } from "../src/GitHubBody.tsx";
import { activityLines } from "../src/screens/TaskScreen.tsx";
import type { PersistedEvent } from "@dude/domain";
import { act, click, mount } from "./dom.ts";

let unmount: (() => Promise<void>) | undefined;
afterEach(async () => { await unmount?.(); unmount = undefined; });

describe("GitHub bodies in Activity", () => {
  test("a CodeRabbit quote renders native nested disclosures, Markdown and no HTML text or pixels", async () => {
    const body = `**Actionable comments posted: 2**
<!-- autofix_checkbox_start -->
- [ ] <!-- {"checkboxId":"4b0d0e0a"} --> Fix comments
<!-- autofix_checkbox_end -->
<details open>
<summary><strong>Review info</strong></summary>
<dl><dd>
<details><summary>Run configuration</summary>
- **Review profile**: CHILL
</details>
</dd></dl>
</details>
<a href="https://example.com"><picture><source srcset="https://example.com/pixel"><img src="https://example.com/pixel"></picture></a>
<!-- coderabbit-review-publication v1 -->`;
    const people = { names: new Map(), byId: new Map(), you: null } as unknown as Parameters<typeof activityLines>[1];
    const lines = activityLines([{
      eventId: "comment", eventType: "pull_request.commented", occurredAt: "2026-10-16T10:00:00Z",
      payload: { author: "coderabbitai", body, number: 41 }, actor: { id: null }, runId: null,
    } as unknown as PersistedEvent], people, [], [], 1);
    const page = await mount(<Timeline>{lines.map((line) => <TimelineItem key={line.id} who={line.who} quote={line.quote}>{line.text}</TimelineItem>)}</Timeline>);
    unmount = page.unmount;
    const root = page.container;
    expect([...root.querySelectorAll("summary")].map((el) => el.textContent)).toEqual(["Review info", "Run configuration"]);
    expect(root.querySelectorAll("details details").length).toBe(1);
    expect(root.querySelectorAll("details[open]").length).toBe(0);
    expect(root.querySelectorAll("strong").length).toBe(2);
    expect(root.querySelectorAll("img, picture, source").length).toBe(0);
    expect(root.textContent).not.toContain("<details>");
    expect(root.textContent).not.toContain("<!--");
    expect(root.textContent).not.toContain("Fix comments");
    expect(root.querySelectorAll(".githubBody button").length).toBe(0);
  });

  test("overflow offers Show all and Show less, and remeasures when the quote gets shorter", async () => {
    // happy-dom has no layout; give the measured element browser-sized boxes.
    const original = globalThis.ResizeObserver;
    const measures: Array<() => void> = [];
    globalThis.ResizeObserver = class {
      constructor(private callback: ResizeObserverCallback) {}
      observe(target: Element) {
        (target as HTMLElement).style.lineHeight = "24px";
        Object.defineProperty(target, "scrollHeight", { configurable: true, get: () => (target.textContent?.length ?? 0) > 1000 ? 1200 : 24 });
        measures.push(() => this.callback([], this as unknown as ResizeObserver));
      }
      unobserve() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver;
    try {
      const long = "### Verification\n\n" + "- Reviewed `DataFreshness.java:32-43`; all tests pass.\n".repeat(80);
      const page = await mount(<GitHubBody source={long} />);
      unmount = page.unmount;
      await act(async () => measures.forEach((measure) => measure()));
      const toggle = page.container.querySelector<HTMLButtonElement>("button")!;
      expect(toggle.textContent).toBe("Show all");
      expect(toggle.getAttribute("aria-expanded")).toBe("false");
      const content = page.container.querySelector<HTMLElement>(".githubSections")!;
      expect(content.style.maxHeight).toBe("288px");
      await click(toggle);
      expect(toggle.textContent).toBe("Show less");
      expect(toggle.getAttribute("aria-expanded")).toBe("true");
      expect(content.style.maxHeight).toBe("");
      await click(toggle);
      expect(content.style.maxHeight).toBe("288px");
      Object.defineProperty(content, "scrollHeight", { configurable: true, value: 24 });
      await act(async () => measures.forEach((measure) => measure()));
      expect(page.container.querySelectorAll("button").length).toBe(0);
    } finally {
      globalThis.ResizeObserver = original;
    }
  });
});

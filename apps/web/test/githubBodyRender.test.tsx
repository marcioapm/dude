import { afterEach, describe, expect, test } from "bun:test";
import { useState } from "react";
import { Timeline, TimelineItem } from "@dude/design-system/components";
import { GitHubBody } from "../src/GitHubBody.tsx";
import { activityLines } from "../src/screens/TaskScreen.tsx";
import type { PersistedEvent } from "@dude/domain";
import { act, click, mount } from "./dom.ts";

let unmount: (() => Promise<void>) | undefined;
const toggle = (root: HTMLElement, name = "Show all") =>
  [...root.querySelectorAll<HTMLButtonElement>('button, [role="button"]')].find((button) => button.textContent === name);
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
    await click(root.querySelector("summary")!);
    expect(root.querySelector("details")!.open).toBe(true);
    expect(root.textContent).toContain("Run configuration");
    expect(root.querySelectorAll("details[open]").length).toBe(1);
    expect([...root.querySelector("blockquote")!.querySelectorAll("strong")].map((el) => el.textContent)).toEqual(["Actionable comments posted: 2", "Review profile"]);
    expect(root.querySelectorAll("img, picture, source").length).toBe(0);
    expect(root.textContent).not.toContain("<details>");
    expect(root.textContent).not.toContain("<!--");
    expect(root.textContent).not.toContain("Fix comments");
    expect(toggle(root)).toBeUndefined();
  });

  test("unsafe HTML and Markdown URLs render as text while HTTPS remains a link", async () => {
    const page = await mount(<GitHubBody source={'<a href="https://example.com">https</a> <a href="javascript:alert(1)">js</a> <a href="data:text/html,x">data</a> <a href="javascript&#58;alert(1)">entity</a> <a href="&#106;avascript:alert(1)">numeric</a> [md](javascript:alert(1))'} />);
    unmount = page.unmount;
    const links = [...page.container.querySelectorAll("a")];
    expect(links.map((link) => [link.textContent, link.getAttribute("href")])).toEqual([["https", "https://example.com"]]);
    expect(page.container.textContent).toContain("js");
    expect(page.container.textContent).toContain("data");
    expect(page.container.textContent).toContain("entity");
    expect(page.container.textContent).toContain("numeric");
    expect(page.container.textContent).toContain("md");
  });

  // happy-dom has no layout: a 24px line, 1200px tall for over 1000 characters or an open disclosure, 24px otherwise.
  async function withLayout(run: (measure: () => Promise<void>) => Promise<void>) {
    const original = globalThis.ResizeObserver;
    const measures: Array<() => void> = [];
    globalThis.ResizeObserver = class {
      constructor(private callback: ResizeObserverCallback) {}
      observe(target: Element) {
        (target as HTMLElement).style.lineHeight = "24px";
        Object.defineProperty(target, "scrollHeight", {
          configurable: true,
          get: () => (target.textContent?.length ?? 0) > 1000 || target.querySelector("details[open]") ? 1200 : 24,
        });
        measures.push(() => this.callback([], this as unknown as ResizeObserver));
      }
      unobserve() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver;
    try {
      await run(() => act(async () => measures.forEach((measure) => measure())));
    } finally {
      globalThis.ResizeObserver = original;
    }
  }

  // Mounted once; `show` rerenders the same GitHubBody with another source.
  async function mountBody(source: string) {
    let setSource: (source: string) => void = () => {};
    function Harness() {
      const [current, set] = useState(source);
      setSource = set;
      return <GitHubBody source={current} />;
    }
    const page = await mount(<Harness />);
    unmount = page.unmount;
    return { container: page.container, show: (next: string) => act(async () => setSource(next)) };
  }

  const long = "### Verification\n\n" + "- Reviewed `DataFreshness.java:32-43`; all tests pass.\n".repeat(80) + "\n[Run log](https://example.com/log)";

  test("overflow clamps with Show all and Show less, and remeasures when the quote changes", async () => {
    await withLayout(async (measure) => {
      const page = await mountBody(long);
      await measure();
      const button = (name?: string) => toggle(page.container, name);
      const content = page.container.querySelector<HTMLElement>(".githubSections")!;
      expect(button()!.getAttribute("aria-expanded")).toBe("false");
      expect(content.style.maxHeight).toBe("288px");
      expect(content.style.overflow).toBe("hidden");
      await click(button()!);
      expect(button()).toBeUndefined();
      expect(button("Show less")!.getAttribute("aria-expanded")).toBe("true");
      expect(content.style.maxHeight).toBe("");
      expect(content.style.overflow).toBe("");
      await click(button("Show less")!);
      expect(content.style.maxHeight).toBe("288px");
      expect(content.style.overflow).toBe("hidden");
      await click(button()!);
      await page.show("Short now.");
      await measure();
      expect(button()).toBeUndefined();
      expect(button("Show less")).toBeUndefined();
      await page.show(long);
      await measure();
      expect(button()!.getAttribute("aria-expanded")).toBe("false");
      expect(content.style.overflow).toBe("hidden");
    });
  });

  test("a single newline is a line break, as on GitHub", async () => {
    const page = await mountBody("First line\nSecond line");
    expect(page.container.querySelectorAll("p br").length).toBe(1);
  });

  test("opening a disclosure in a short quote makes it overflow", async () => {
    await withLayout(async (measure) => {
      const page = await mountBody("<details><summary>Review info</summary>Run configuration</details>");
      await measure();
      expect(toggle(page.container)).toBeUndefined();
      const details = page.container.querySelector("details")!;
      await act(async () => {
        details.open = true;
        details.dispatchEvent(new Event("toggle"));
      });
      expect(toggle(page.container)).toBeDefined();
    });
  });

  test("focus expands the quote only when the focused element is below the clamp", async () => {
    await withLayout(async (measure) => {
      const page = await mountBody("[Top](https://example.com/top)\n\n" + long);
      await measure();
      const content = page.container.querySelector<HTMLElement>(".githubSections")!;
      const top = page.container.querySelector<HTMLAnchorElement>("a[href='https://example.com/top']")!;
      const tail = page.container.querySelector<HTMLAnchorElement>("a[href='https://example.com/log']")!;
      // The clamp shows 0-288px; the top link sits in it, the tail link far below.
      const bottomAt = (el: Element, bottom: number) => { el.getBoundingClientRect = () => ({ bottom }) as DOMRect; };
      bottomAt(content, 288);
      bottomAt(top, 24);
      bottomAt(tail, 2000);
      await act(async () => top.focus());
      expect(content.style.overflow).toBe("hidden");
      expect(toggle(page.container)!.getAttribute("aria-expanded")).toBe("false");
      await act(async () => tail.focus());
      expect(content.style.overflow).toBe("");
      expect(toggle(page.container, "Show less")!.getAttribute("aria-expanded")).toBe("true");
    });
  });
});

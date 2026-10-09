import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { renderToStaticMarkup } from "react-dom/server";
import { ChatEvent, eventDetail, eventHasDetail, summarizeEventData } from "../src/components/ChatEvent.tsx";
import { ChatProgress, progressFraction } from "../src/components/ChatProgress.tsx";
import { ChatNotice } from "../src/components/ChatNotice.tsx";

describe("summarizeEventData", () => {
  test("scalars as-is", () => {
    expect(summarizeEventData(87.4)).toBe("87.4");
    expect(summarizeEventData("ok")).toBe("ok");
    expect(summarizeEventData(true)).toBe("true");
    expect(summarizeEventData(null)).toBe("null");
    expect(summarizeEventData(undefined)).toBe("");
  });
  test("objects as up to three key=value pairs, then …", () => {
    expect(summarizeEventData({ passed: 42, failed: 1 })).toBe("passed=42 failed=1");
    expect(summarizeEventData({ a: 1, b: 2, c: 3, d: 4 })).toBe("a=1 b=2 c=3 …");
    expect(summarizeEventData({})).toBe("{}");
  });
  test("nested values stay short", () => {
    expect(summarizeEventData({ env: "staging", services: ["api", "web"], meta: { x: 1 } })).toBe("env=staging services=[2] meta={…}");
    expect(summarizeEventData([1, 2, 3])).toBe("[3]");
  });
});

describe("eventHasDetail / eventDetail", () => {
  test("only non-empty structures and long strings expand", () => {
    expect(eventHasDetail(87.4)).toBe(false);
    expect(eventHasDetail(null)).toBe(false);
    expect(eventHasDetail({})).toBe(false);
    expect(eventHasDetail([])).toBe(false);
    expect(eventHasDetail({ a: 1 })).toBe(true);
    expect(eventHasDetail("x".repeat(81))).toBe(true);
    expect(eventHasDetail("short")).toBe(false);
  });
  test("detail is pretty JSON, or the string itself", () => {
    expect(eventDetail({ a: [1] })).toBe('{\n  "a": [\n    1\n  ]\n}');
    expect(eventDetail("raw text")).toBe("raw text");
    expect(eventDetail(undefined)).toBe("undefined");
  });
});

describe("ChatEvent markup", () => {
  test("a scalar event has no disclosure button", () => {
    const html = renderToStaticMarkup(<ChatEvent type="coverage" data={87.4} at="2026-09-24T10:00:00Z" role="implementer" />);
    expect(html).not.toContain("aria-expanded");
    expect(html).toContain("coverage");
    expect(html).toContain("87.4");
  });
  test("an object event is a disclosure button; expanded shows the JSON with aria-controls", () => {
    const closed = renderToStaticMarkup(<ChatEvent type="tests.finished" data={{ passed: 42, failed: 1 }} at="2026-09-24T10:00:00Z" role="reviewer" />);
    expect(closed).toMatch(/<button[^>]*aria-expanded="false"/);
    expect(closed).not.toContain("aria-controls");
    expect(closed).not.toContain("<pre");
    const open = renderToStaticMarkup(<ChatEvent type="tests.finished" data={{ passed: 42, failed: 1 }} at="2026-09-24T10:00:00Z" role="reviewer" defaultExpanded />);
    expect(open).toMatch(/<button[^>]*aria-expanded="true"[^>]*aria-controls="/);
    expect(open).toContain("&quot;passed&quot;: 42");
    expect(open).toContain('aria-label="Reviewer"');
  });
  test("the whole row is the toggle: its time and chevron are inside the button, as on a tool call", () => {
    const html = renderToStaticMarkup(<ChatEvent type="tests.finished" data={{ passed: 42 }} at="2026-09-24T10:00:00Z" role="reviewer" />);
    const button = html.match(/<button[\s\S]*?<\/button>/)?.[0] ?? "";
    expect(button).toContain("<time");
    expect(button).toContain("<svg");
    expect(html.slice(html.indexOf("</button>"))).not.toContain("<time");
  });
});

describe("progressFraction", () => {
  test("clamped ratio when both are known and positive", () => {
    expect(progressFraction(3, 10)).toBe(0.3);
    expect(progressFraction(12, 10)).toBe(1);
    expect(progressFraction(-1, 10)).toBe(0);
  });
  test("indeterminate otherwise", () => {
    expect(progressFraction(null, 10)).toBeNull();
    expect(progressFraction(3, null)).toBeNull();
    expect(progressFraction(3, 0)).toBeNull();
    expect(progressFraction(Number.NaN, 10)).toBeNull();
  });
});

describe("ChatProgress markup", () => {
  const T = "2026-09-24T10:00:00Z";
  test("determinate: progressbar with min/max/now, the count and the step", () => {
    const html = renderToStaticMarkup(<ChatProgress done={3} of={10} step="Running tests" at={T} startedAt={T} role="implementer" />);
    expect(html).toMatch(/role="progressbar"[^>]*aria-valuemin="0"/);
    expect(html).toMatch(/aria-valuemax="10"/);
    expect(html).toMatch(/aria-valuenow="3"/);
    expect(html).toContain("3 of 10");
    expect(html).toContain("Running tests");
    expect(html).toContain('aria-busy="true"');
    expect(html).toMatch(/width:30%/);
  });
  test("indeterminate: progressbar without values, a sweep, the step as valuetext", () => {
    const html = renderToStaticMarkup(<ChatProgress done={null} of={null} step="Installing" at={T} startedAt={T} role="implementer" />);
    expect(html).toContain('role="progressbar"');
    expect(html).not.toContain("aria-valuenow");
    expect(html).toContain('aria-valuetext="Installing"');
    // The bar holds a sweep span with no inline width, where a fill would carry one.
    expect(html).toMatch(/role="progressbar"[^>]*><span><\/span><\/div>/);
  });
  test("ended: not busy, frozen where it got to; complete says Finished, short says Stopped", () => {
    const done = renderToStaticMarkup(<ChatProgress done={10} of={10} step="Tests" at={T} startedAt={T} role="implementer" ended />);
    expect(done).not.toContain("aria-busy");
    expect(done).toContain("Finished");
    expect(done).toContain('data-progress="ended"');
    const short = renderToStaticMarkup(<ChatProgress done={6} of={10} step="Tests" at={T} startedAt={T} role="implementer" ended />);
    expect(short).toContain("Stopped");
    expect(short).toMatch(/width:60%/);
  });
});

describe("ChatProgress motion", () => {
  const css = readFileSync(`${import.meta.dir}/../src/components/ChatProgress.module.css`, "utf8");
  test("every loop divides by --ds-motion-live and only runs while .running", () => {
    const loops = [...css.matchAll(/animation:[^;]+/g)].map((m) => m[0]);
    expect(loops.length).toBeGreaterThan(0);
    for (const l of loops) expect(l).toContain("--ds-motion-live");
    for (const l of loops) expect(l).toMatch(/--ds-cadence-/);
    expect(css).not.toMatch(/^\.sweep\s*\{[^}]*animation/m);
    expect(css).not.toMatch(/^\.fill\s*\{[^}]*animation/m);
    expect(css).toMatch(/^\.running \.sweep\s*\{[^}]*animation/m);
    expect(css).toMatch(/^\.running \.fill\s*\{[^}]*animation/m);
  });
});

describe("ChatNotice", () => {
  test("a stopped turn carries an error glyph and kind", () => {
    const html = renderToStaticMarkup(
      <ChatNotice kind="stopped" by="dude"
        text="Stopped Brainstorm's turn: bash was open for 10 min." at="2026-10-08T10:00:00Z" />,
    );
    expect(html).toContain('data-kind="stopped"');
    expect(html).toContain('data-icon="warning"');
    expect(html).toContain("Stopped Brainstorm&#x27;s turn");
  });
  test("a note in the transcript's margin, not a message", () => {
    const html = renderToStaticMarkup(
      <ChatNotice kind="parked" text="Parked while it waits for you." at="2026-09-25T10:00:00Z" />,
    );
    expect(html).toContain('role="note"');
    expect(html).toContain('data-kind="parked"');
    expect(html).toContain("Parked while it waits for you.");
    expect(html).toContain('data-icon="pause"');
  });
});

/**
 * A wrapping TriggerSummary, read from the computed style with the shipped
 * stylesheet loaded (happy-dom has no layout): its words may wrap, but the
 * glyph and the first word sit in one box that does not, so a narrow cell
 * never leaves the glyph alone on a line.
 */

import { afterAll, beforeAll, expect, mock, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { cssModuleAsWritten } from "./cssModule.ts";

mock.module(...cssModuleAsWritten(new URL("../src/components/Automations.module.css", import.meta.url)));

const { TriggerSummary } = await import("../src/components/Automations.tsx");

let style: HTMLStyleElement | null = null;
let root: Root | null = null;
let host: HTMLElement | null = null;

beforeAll(async () => {
  style = document.createElement("style");
  style.textContent = await Bun.file(new URL("../src/components/Automations.module.css", import.meta.url)).text();
  document.head.append(style);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(
    <>
      <span data-testid="wrap-github"><TriggerSummary wrap trigger={{ kind: "github", event: "pull_request", actions: ["opened", "synchronize", "reopened"] }} /></span>
      <span data-testid="wrap-schedule"><TriggerSummary wrap trigger={{ kind: "schedule", cron: "0 3 * * *", tz: "Europe/Lisbon" }} /></span>
    </>,
  ));
});

afterAll(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  style?.remove();
});

for (const [id, first, rest] of [["wrap-github", "pull_request", "opened"], ["wrap-schedule", "Daily", "Lisbon"]] as const) {
  test(`${id}: the glyph and "${first}" do not break apart; the rest wraps`, () => {
    const summary = host!.querySelector(`[data-testid=${id}] > span`)!;
    expect(getComputedStyle(summary).whiteSpace).toBe("normal");
    const glyph = summary.querySelector("svg")!;
    const lead = glyph.parentElement!;
    expect(lead).not.toBe(summary);
    expect(getComputedStyle(lead).whiteSpace).toBe("nowrap");
    expect(lead.textContent).toContain(first);
    expect(lead.textContent).not.toContain(rest);
  });
}

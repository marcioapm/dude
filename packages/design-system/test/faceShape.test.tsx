/**
 * The conversation agents' faces are round: the brainstorm's and the
 * conductor's avatar, and the bulb face of a brainstorm session's row. An
 * ordinary agent keeps the rounded square, as the control.
 *
 * The real components render with their CSS-module class names kept as
 * written; the shipped stylesheets and tokens.css are loaded into the
 * document, and the corner radius is read from the computed style, so the
 * cascade (size classes, role classes, token values) is what is checked.
 */

import { afterAll, beforeAll, expect, mock, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

// Under bun test a CSS module maps to {}: keep each class name as written.
// Bun 1.4.2 cannot scope or restore module mocks (mock.restore() leaves them installed).
// Safe today: no other test imports these maps directly or relies on their empty exports.
const asWritten = () => ({ default: new Proxy({}, { get: (_, key) => (typeof key === "string" ? key : undefined) }) });
mock.module("../src/components/AgentAvatar.module.css", asWritten);
mock.module("../src/components/Brainstorm.module.css", asWritten);

const { AgentAvatar } = await import("../src/components/AgentAvatar.tsx");
const { SessionRow } = await import("../src/components/Brainstorm.tsx");

const SHEETS = ["tokens/tokens.css", "components/AgentAvatar.module.css", "components/Brainstorm.module.css"];
const styleEls: HTMLStyleElement[] = [];
let root: Root | null = null;
let host: HTMLElement | null = null;

beforeAll(async () => {
  for (const path of SHEETS) {
    const el = document.createElement("style");
    el.textContent = await Bun.file(new URL(`../src/${path}`, import.meta.url)).text();
    document.head.append(el);
    styleEls.push(el);
  }
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(
    <div>
      {(["conductor", "brainstorm", "implementer"] as const).flatMap((role) =>
        (["xs", "sm", "md", "lg", "chat"] as const).map((size) => <AgentAvatar key={`${role}-${size}`} role={role} size={size} data-face={`${role}-${size}`} />))}
      <ul><SessionRow title="Billing" summary="Nothing filed yet" projects={[]} state="Talking" onOpen={() => undefined} /></ul>
    </div>,
  ));
});

afterAll(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  for (const el of styleEls) el.remove();
});

const token = (name: string) => getComputedStyle(document.documentElement).getPropertyValue(name).trim();
const radius = (el: Element) => getComputedStyle(el).borderTopLeftRadius;

test("the tokens resolve to two different corners", () => {
  expect(token("--ds-radius-full")).not.toBe("");
  expect(token("--ds-radius-face-agent")).not.toBe("");
  expect(token("--ds-radius-full")).not.toBe(token("--ds-radius-face-agent"));
});

test("the brainstorm's and the conductor's avatars are round at every size", () => {
  for (const role of ["conductor", "brainstorm"]) {
    for (const size of ["xs", "sm", "md", "lg", "chat"]) {
      const face = host!.querySelector(`[data-face="${role}-${size}"]`)!;
      expect(radius(face), `${role} ${size}`).toBe(token("--ds-radius-full"));
    }
  }
});

test("an ordinary agent keeps the rounded square", () => {
  for (const size of ["xs", "sm", "md", "lg", "chat"]) {
    const face = host!.querySelector(`[data-face="implementer-${size}"]`)!;
    expect(radius(face), `implementer ${size}`).toBe(token("--ds-radius-face-agent"));
  }
});

test("a brainstorm session's row face is round", () => {
  const face = host!.querySelector(".rowFace")!;
  expect(face.querySelector("[data-icon='brainstorm']")).toBeTruthy();
  expect(radius(face)).toBe(token("--ds-radius-full"));
});

/**
 * Rules held in code rather than stylesheets:
 * - Meaning never lives in hue alone: every status, todo state and triage
 *   group has a tone, a glyph and a label.
 * - Nothing is loud but "needs you": only a state that waits on a person may
 *   be `solid`.
 * - Buttons come in four kinds. A fifth is how a screen ends up with three
 *   kinds of grey button again; `Button.tsx` is the one place they live.
 */

import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { STATUS_SPECS, TODO_SPECS, TRIAGE_SPECS } from "../src/tokens/index.ts";

describe("status", () => {
  test("every status has a tone, a glyph and a label", () => {
    for (const [status, spec] of Object.entries(STATUS_SPECS)) {
      expect(spec.tone, status).toBeTruthy();
      expect(spec.glyph, status).toBeTruthy();
      expect(spec.label.trim().length, status).toBeGreaterThan(0);
    }
  });

  test("only what waits on a person is solid", () => {
    for (const [status, spec] of Object.entries(STATUS_SPECS)) {
      if (spec.emphasis === "solid") expect(spec.needsHuman, status).toBe(true);
    }
  });

  test("todo states and triage groups carry a glyph and a label too", () => {
    for (const [k, spec] of Object.entries(TODO_SPECS)) {
      expect(spec.glyph, k).toBeTruthy();
      expect(spec.label, k).toBeTruthy();
    }
    for (const [k, spec] of Object.entries(TRIAGE_SPECS)) {
      expect(spec.label, k).toBeTruthy();
      expect(spec.tone, k).toBeTruthy();
    }
  });
});

describe("buttons", () => {
  const src = readFileSync(`${import.meta.dir}/../src/primitives/Button.tsx`, "utf8");

  test("come in four kinds", () => {
    const union = /export type ButtonVariant =([^;]+);/.exec(src)![1]!;
    const kinds = [...union.matchAll(/"([a-z-]+)"/g)].map((m) => m[1]);
    expect(kinds).toEqual(["primary", "secondary", "quiet", "danger"]);
  });

  test("are drawn by their fill, never an outline", () => {
    const css = readFileSync(`${import.meta.dir}/../src/primitives/Button.module.css`, "utf8");
    expect(css).not.toMatch(/--btn-border|\bborder:/);
  });
});

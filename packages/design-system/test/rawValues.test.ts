/**
 * The rules a stylesheet can be read for, beyond radii and borders:
 * - colours and shadows come from tokens, never a literal (`#fff`,
 *   `rgba(…)`), so light and dark stay a pair; a mask's `#000` is an alpha
 *   channel, not a colour, and is allowed;
 * - clickable things show a pointer: a module never sets `cursor: default`
 *   on something that has a hover (the one static head is listed);
 * - the web app styles layout only, and borrows everything else from the
 *   design system.
 */

import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { declarations, ROOT, stylesheets } from "./stylesheets.ts";

const COLOUR_LITERAL = /#[0-9a-fA-F]{3,8}\b|\brgba?\(|\bhsla?\(/;
const MASK = /^(-webkit-)?mask(-image)?$/;

describe("raw values", () => {
  for (const path of stylesheets()) {
    test(`${path} takes its colours from tokens`, () => {
      const bad = declarations(readFileSync(`${ROOT}/${path}`, "utf8"), /^[a-z-]+$/)
        .filter(([, prop, value]) => !MASK.test(prop) && !prop.startsWith("--") && COLOUR_LITERAL.test(value))
        .map(([selector, prop, value]) => `${selector} { ${prop}: ${value} }`);
      expect(bad).toEqual([]);
    });
  }
});

describe("pointer", () => {
  const STATIC = new Set(["packages/design-system/src/components/Sidebar.module.css .attentionHeadStatic"]);
  for (const path of stylesheets()) {
    test(`${path} never takes the pointer away from something clickable`, () => {
      const bad = declarations(readFileSync(`${ROOT}/${path}`, "utf8"), /^cursor$/)
        .filter(([selector, , value]) => value === "default" && !STATIC.has(`${path} ${selector}`))
        .map(([selector]) => selector);
      expect(bad).toEqual([]);
    });
  }

  test("the base stylesheet gives every clickable element a pointer", () => {
    const base = readFileSync(`${ROOT}/packages/design-system/src/styles/base.css`, "utf8");
    const rule = declarations(base, /^cursor$/).find(([, , v]) => v === "pointer");
    expect(rule).toBeDefined();
    for (const el of ["button", "summary", '[role="button"]', '[role="tab"]', '[role="menuitem"]', "a[href]"]) {
      expect(rule![0].split(",").map((s) => s.trim())).toContain(el);
    }
  });
});

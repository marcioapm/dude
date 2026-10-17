/**
 * The overlay's stylesheet takes every colour from the design system's
 * tokens: no hex, rgb()/hsl()/oklch() or named colour of its own (the
 * README: components consume only the semantic roles). `transparent` and
 * `currentColor` are not colours of their own and are allowed. Its radii
 * are the DS's roles.
 */

import { describe, expect, test } from "bun:test";
import { CSS } from "../css.ts";

const NAMED = ["white", "black", "red", "green", "blue", "yellow", "orange", "purple", "violet", "gray", "grey", "silver", "pink", "navy", "teal", "magenta", "cyan"];

/** The raw colours in `css`, comments aside. */
export function rawColours(css: string): string[] {
  const clean = css.replace(/\/\*[\s\S]*?\*\//g, "");
  const out = [...clean.matchAll(/#[0-9a-fA-F]{3,8}\b|\b(?:rgba?|hsla?|oklch|oklab|lab|lch|hwb|color)\(/g)].map((m) => m[0]);
  for (const n of NAMED) for (const m of clean.matchAll(new RegExp(`[:\\s,(]${n}\\b`, "gi"))) out.push(m[0].trim());
  return out;
}

describe("the overlay's stylesheet", () => {
  test("has no colour of its own: every one is a design-system token", () => {
    expect(rawColours(CSS)).toEqual([]);
  });

  test("the check sees each kind of raw colour", () => {
    expect(rawColours(".a{color:#7c5cff;background:rgba(0,0,0,.3);border-color:hsl(0 0% 0%);outline:2px solid white}")).toEqual(["#7c5cff", "rgba(", "hsl(", "white"]);
    expect(rawColours(".a{color:var(--ds-color-accent);background:transparent;fill:currentColor}")).toEqual([]);
  });

  test("its radii are the design system's roles", () => {
    const radii = [...CSS.matchAll(/border-radius:([^;}]+)/g)].map((m) => m[1]!.trim());
    for (const r of radii) for (const part of r.split(/\s+/)) expect([part, /^var\(--ds-radius-(mark|control|float|full)\)$/.test(part)]).toEqual([part, true]);
  });
});

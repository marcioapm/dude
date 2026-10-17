/**
 * The overlay's copy of the design system's tokens (tokens.gen.ts) is the
 * design system's: regenerated from @dude/design-system's source it is
 * byte for byte the committed file, so a token changed in the DS without
 * `bun overlay/build-tokens.ts` fails here. Every `var(--ds-…)` the overlay's
 * stylesheet reads is one the copy carries, so none falls back to nothing.
 */

import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { flattenTheme } from "@dude/design-system/tokens";
import { OVERLAY_THEME_TOKENS, TOKENS_MODULE, compactTokenNames, overlayTokensCss, overlayTokensModule } from "../build-tokens.ts";
import { CSS } from "../css.ts";
import { TOKENS_CSS } from "../tokens.gen.ts";

describe("the overlay's tokens", () => {
  test("are the design system's, regenerated: the committed copy is not stale", () => {
    expect(readFileSync(TOKENS_MODULE, "utf8")).toBe(overlayTokensModule());
    expect(TOKENS_CSS).toBe(overlayTokensCss());
  });

  test("carry each theme's value of every token they name", () => {
    for (const mode of ["dark", "light"] as const) {
      const block = TOKENS_CSS.split("\n").find((l) => l.startsWith(mode === "dark" ? ".root,.root[data-theme=dark]" : ".root[data-theme=light]"))!;
      const all = flattenTheme(mode);
      for (const k of OVERLAY_THEME_TOKENS) expect(block).toContain(`--ds-${k}:${all[k]};`.replace(/;$/, ""));
    }
  });

  test("build-time name compaction keeps declarations and references paired", () => {
    const compact = compactTokenNames(`${TOKENS_CSS}\n${CSS}`);
    const declared = new Set([...compact.matchAll(/(--d[a-z0-9]+):/g)].map((m) => m[1]));
    const referenced = [...compact.matchAll(/var\((--d[a-z0-9]+)/g)].map((m) => m[1]);
    expect(referenced.length).toBeGreaterThan(0);
    expect(referenced.filter((n) => !declared.has(n))).toEqual([]);
    expect(compact).not.toContain("--ds-");
    expect(() => compactTokenNames("var(--ds-not-a-token)")).toThrow();
  });

  test("name every custom property the stylesheet reads", () => {
    const read = new Set([...CSS.matchAll(/var\(--ds-([a-z0-9-]+)/g)].map((m) => m[1]!));
    const carried = new Set([...TOKENS_CSS.matchAll(/--ds-([a-z0-9-]+):/g)].map((m) => m[1]!));
    expect([...read].filter((n) => !carried.has(n))).toEqual([]);
  });
});

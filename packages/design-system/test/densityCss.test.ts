/**
 * The density blocks in the generated stylesheet: what the browser gets,
 * parsed into rules, with every expected value read from `densityTokens`.
 */

import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { buildTokensCss, TOKENS_CSS_URL } from "../scripts/build-tokens.ts";
import { DENSITIES, densityTokens, type DensityToken } from "../src/tokens/density.ts";

/** Top-level rules (at-rules skipped) as selector -> custom property -> value. */
function topLevelRules(css: string): Map<string, Map<string, string>> {
  const rules = new Map<string, Map<string, string>>();
  const src = css.replace(/\/\*[\s\S]*?\*\//g, "");
  let i = 0;
  while (i < src.length) {
    const open = src.indexOf("{", i);
    if (open < 0) break;
    const selector = src.slice(i, open).trim();
    let depth = 1;
    let j = open + 1;
    while (depth > 0 && j < src.length) {
      if (src[j] === "{") depth++;
      else if (src[j] === "}") depth--;
      j++;
    }
    if (!selector.startsWith("@")) {
      const decls = rules.get(selector) ?? new Map<string, string>();
      for (const d of src.slice(open + 1, j - 1).split(";")) {
        const colon = d.indexOf(":");
        if (colon > 0) decls.set(d.slice(0, colon).trim(), d.slice(colon + 1).trim());
      }
      rules.set(selector, decls);
    }
    i = j;
  }
  return rules;
}

const css = buildTokensCss();
const rules = topLevelRules(css);
const keys = Object.keys(densityTokens.comfortable) as DensityToken[];

describe("generated density CSS", () => {
  test("the committed tokens.css is the generator's output", () => {
    expect(readFileSync(TOKENS_CSS_URL, "utf8")).toBe(css);
  });

  test("each density has a block with exactly its data-density selector", () => {
    for (const d of DENSITIES) expect(rules.has(`[data-density="${d}"]`), d).toBe(true);
  });

  test("each density block sets every density token to that density's value", () => {
    for (const d of DENSITIES) {
      const block = rules.get(`[data-density="${d}"]`) ?? new Map<string, string>();
      for (const k of keys) expect(block.get(`--ds-${k}`), `${d} ${k}`).toBe(densityTokens[d][k]);
    }
  });

  test(":root defaults every density token to comfortable", () => {
    // Plain `:root` rules merge here; the theme rules use a selector list and are separate keys.
    const root = rules.get(":root") ?? new Map<string, string>();
    for (const k of keys) expect(root.get(`--ds-${k}`), k).toBe(densityTokens.comfortable[k]);
  });
});

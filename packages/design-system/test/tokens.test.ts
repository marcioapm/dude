/**
 * Every design token used is defined. A `var(--ds-…)` naming a token that
 * does not exist resolves to nothing, silently: a gap of zero, a colour of
 * none (found as a status badge touching the text beside it, from a
 * `--ds-space-10` no scale ever had). The app's stylesheet is checked too.
 */

import { describe, expect, test } from "bun:test";
import { Glob } from "bun";
import { readFileSync } from "node:fs";

const root = `${import.meta.dir}/../..`;
const sources = [
  ...new Glob("design-system/src/**/*.{css,ts,tsx}").scanSync(root),
  ...new Glob("../apps/web/src/**/*.{css,ts,tsx}").scanSync(root),
].filter((path) => !path.includes("node_modules"));

const read = (path: string) => readFileSync(`${root}/${path}`, "utf8");
const defined = new Set(sources.flatMap((path) => [...read(path).matchAll(/(--ds-[\w-]+)\s*:/g)].map((m) => m[1]!)));

describe("design tokens", () => {
  test("are found", () => {
    expect(defined.size).toBeGreaterThan(20);
  });

  test("used are defined", () => {
    const missing: string[] = [];
    for (const path of sources) {
      // A use with a fallback, or a name built at runtime (`--ds-ansi-${n}`), is not checked.
      for (const [, name] of read(path).matchAll(/var\((--ds-[\w-]*\w)\s*\)/g)) {
        if (!defined.has(name!)) missing.push(`${path}: ${name}`);
      }
    }
    expect(missing).toEqual([]);
  });
});

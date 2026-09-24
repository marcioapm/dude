/**
 * Keyframes are defined once, globally (styles/base.css), and used from CSS
 * modules. A module that names one plainly gets it renamed to a local hash
 * that matches nothing: the animation silently never runs — and a Radix
 * popup waiting for its exit animation to end never unmounts, leaving an
 * invisible layer that blocks every click (found as a dialog whose select
 * froze it). So every use of a shared keyframe must say global(...).
 */

import { describe, expect, test } from "bun:test";
import { Glob } from "bun";
import { readFileSync } from "node:fs";

const globalKeyframes = [...readFileSync(`${import.meta.dir}/../src/styles/base.css`, "utf8").matchAll(/@keyframes\s+([\w-]+)/g)].map(
  (m) => m[1]!,
);

describe("shared keyframes", () => {
  test("are found", () => {
    expect(globalKeyframes.length).toBeGreaterThan(0);
  });

  for (const path of new Glob("src/**/*.module.css").scanSync(`${import.meta.dir}/..`)) {
    test(`${path} uses them as global()`, () => {
      const css = readFileSync(`${import.meta.dir}/../${path}`, "utf8");
      for (const name of globalKeyframes) {
        const bare = new RegExp(`animation(-name)?:\\s*${name}\\b`);
        expect(bare.test(css)).toBe(false);
      }
    });
  }
});

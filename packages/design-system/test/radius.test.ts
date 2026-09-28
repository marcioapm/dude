/**
 * Radii are roles (`src/tokens/scale.ts`): structure is square, what you
 * touch is `control`, what floats is `float`, a small inline mark is `mark`,
 * and faces carry their shape — people `full`, agents and projects a
 * `face-*` corner. A literal radius (`8px`, `2px`) or a retired size
 * (`--ds-radius-md`) is how a stylesheet drifts back to rounding everything,
 * so none is allowed here.
 */

import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { declarations, ROOT, stylesheets } from "./stylesheets.ts";

const ALLOWED = /^(0|inherit|50%|var\(--ds-radius-(none|mark|control|float|full|face-(agent|project))\))$/;

describe("radii", () => {
  const files = stylesheets();

  test("are found", () => {
    expect(files.length).toBeGreaterThan(40);
  });

  for (const path of files) {
    test(`${path} uses only radius roles`, () => {
      const bad = declarations(readFileSync(`${ROOT}/${path}`, "utf8"), /^border(-[a-z]+)*-radius$/)
        .filter(([, , value]) => !value.split(/\s+/).every((part) => ALLOWED.test(part)))
        .map(([selector, , value]) => `${selector} { border-radius: ${value} }`);
      expect(bad).toEqual([]);
    });
  }
});

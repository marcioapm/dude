import { describe, expect, test } from "bun:test";
import { densityTokens, type DensityToken } from "../src/tokens/density.ts";

const px = (v: string) => (v.endsWith("px") ? Number(v.slice(0, -2)) : Number.NaN);
const keys = Object.keys(densityTokens.compact) as DensityToken[];

describe("density", () => {
  test("compact is never roomier than comfortable", () => {
    for (const k of keys) {
      const [c, k2] = [px(densityTokens.comfortable[k]), px(densityTokens.compact[k])];
      if (Number.isNaN(c)) continue;
      expect(k2, k).toBeLessThanOrEqual(c);
    }
  });

  test("text and the default radius lose at most 1px", () => {
    for (const k of keys.filter((k) => k.startsWith("text-") || k.startsWith("radius-"))) {
      expect(px(densityTokens.comfortable[k]) - px(densityTokens.compact[k]), k).toBeLessThanOrEqual(1);
    }
  });

  test("already-small tokens hold in both densities", () => {
    const small = ["text-2xs", "text-mono", "size-control-sm", "size-row-compact", "size-icon-sm", "size-icon-md", "size-icon-lg", "radius-sm", "radius-xs"];
    for (const k of small) expect(keys as string[]).not.toContain(k);
  });

  test("the large layout spacing shrinks by a visible amount", () => {
    for (const k of ["space-main-pad", "space-chat-gap", "space-panel-gap", "size-avatar-chat", "size-row-default"] as const) {
      expect(px(densityTokens.comfortable[k]) - px(densityTokens.compact[k]), k).toBeGreaterThanOrEqual(4);
    }
  });
});

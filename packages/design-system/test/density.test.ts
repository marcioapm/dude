import { describe, expect, test } from "bun:test";
import { densityTokens, type DensityToken } from "../src/tokens/density.ts";
import { fontSize, lineHeight, size, spaceNamed } from "../src/tokens/scale.ts";

const px = (v: string) => (v.endsWith("px") ? Number(v.slice(0, -2)) : Number.NaN);
const keys = Object.keys(densityTokens.compact) as DensityToken[];
const at = (d: "comfortable" | "compact", k: DensityToken) => px(densityTokens[d][k]);

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
    const small = [
      "text-2xs",
      "text-xs",
      "text-sm",
      "text-mono",
      "size-control-sm",
      "size-row-compact",
      "size-badge-sm",
      "size-badge-md",
      "size-chip",
      "size-icon-sm",
      "size-icon-md",
      "size-icon-lg",
      "radius-sm",
      "radius-xs",
    ];
    for (const k of small) expect(keys as string[]).not.toContain(k);
  });

  test("the large layout spacing shrinks by a visible amount", () => {
    for (const k of ["space-main-pad", "space-chat-gap", "space-panel-gap", "space-card-pad", "size-avatar-chat", "size-row-default"] as const) {
      expect(at("comfortable", k) - at("compact", k), k).toBeGreaterThanOrEqual(4);
    }
  });

  test("compact hits the targets that make it read denser at a glance", () => {
    expect(at("compact", "size-avatar-chat")).toBe(32);
    expect(at("compact", "space-chat-gap")).toBe(10);
    expect(at("compact", "size-row-default")).toBe(28);
    expect(at("compact", "space-main-pad")).toBe(16);
    expect(at("compact", "space-card-pad")).toBe(8);
  });
});

describe("chat metrics (Discord)", () => {
  test("comfortable text column starts at 72px: gutter 16 + avatar 40 + gap 16", () => {
    expect(at("comfortable", "space-chat-pad-x") + at("comfortable", "size-avatar-chat") + at("comfortable", "space-chat-avatar-gap")).toBe(72);
  });

  test("chat text is 16px on a 22px line", () => {
    expect(fontSize.prose * lineHeight.chat).toBe(22);
  });

  test("speakers are ~17px apart, same-author turns 2px", () => {
    expect(spaceNamed.chatGap).toBe(17);
  });
});

describe("badges and chips", () => {
  // Label boxes are trimmed to cap height and centred, so each size needs
  // room for the cap height plus at least 2px of air above and below.
  const pairs: ReadonlyArray<readonly [string, number, number]> = [
    ["badge-sm / 2xs", size.badgeSm, fontSize["2xs"]],
    ["badge-md / xs", size.badgeMd, fontSize.xs],
    ["chip / 2xs", size.chip, fontSize["2xs"]],
  ];
  test("heights fit their text with at least 2px either side", () => {
    for (const [name, h, fs] of pairs) expect(h - fs, name).toBeGreaterThanOrEqual(4);
  });
});

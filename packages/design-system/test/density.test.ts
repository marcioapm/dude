import { describe, expect, test } from "bun:test";
import { BIG_TICKET_TOKENS, LARGE_LAYOUT_TOKENS, densityTokens, type DensityToken } from "../src/tokens/density.ts";
import { fontSize, lineHeight, size, space } from "../src/tokens/scale.ts";

/** A length in px or em as a number; NaN for anything else (`ch`, unitless). */
const num = (v: string) => (/^-?[\d.]+(px|em)$/.test(v) ? Number.parseFloat(v) : Number.NaN);
const px = (v: string) => (v.endsWith("px") ? Number(v.slice(0, -2)) : Number.NaN);
const keys = Object.keys(densityTokens.compact) as DensityToken[];
const at = (d: "comfortable" | "compact", k: DensityToken) => px(densityTokens[d][k]);

describe("density", () => {
  test("compact is never roomier than comfortable", () => {
    // `measure-message` is in `ch` and is meant to widen: compact text is smaller.
    // Unitless leading is checked on its own below.
    const lengths = keys.filter((k) => k !== "measure-message" && !k.startsWith("leading-"));
    for (const k of lengths) {
      const [c, k2] = [num(densityTokens.comfortable[k]), num(densityTokens.compact[k])];
      expect(Number.isNaN(c) || Number.isNaN(k2), `${k} parses`).toBe(false);
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
      "text-nav",
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
    for (const k of LARGE_LAYOUT_TOKENS) {
      expect(at("comfortable", k) - at("compact", k), k).toBeGreaterThanOrEqual(4);
    }
  });

  test("compact hits the targets that make it read denser at a glance", () => {
    expect(at("compact", "size-avatar-chat")).toBe(32);
    expect(at("compact", "space-chat-gap")).toBe(8);
    expect(at("compact", "size-row-item")).toBe(26);
    expect(at("compact", "size-row-item-sm")).toBe(24);
    expect(at("compact", "space-main-pad")).toBe(12);
    expect(at("compact", "space-card-pad")).toBe(6);
    expect(at("compact", "space-attention-row-pad-y")).toBe(3);
    expect(at("compact", "space-aside-y")).toBe(2);
  });

  test("the big-ticket spacing loses at least a third in compact", () => {
    for (const k of BIG_TICKET_TOKENS) {
      expect(at("compact", k) / at("comfortable", k), k).toBeLessThanOrEqual(2 / 3);
    }
  });

  test("leading tightens in compact and chat lines land on whole pixels", () => {
    const lead = (d: "comfortable" | "compact", k: "leading-chat" | "leading-prose") => Number(densityTokens[d][k]);
    expect(lead("compact", "leading-chat")).toBeLessThan(lead("comfortable", "leading-chat"));
    expect(lead("compact", "leading-prose")).toBeLessThan(lead("comfortable", "leading-prose"));
    for (const d of ["comfortable", "compact"] as const) {
      const line = at(d, "text-prose") * lead(d, "leading-chat");
      expect(Math.abs(line - Math.round(line)), d).toBeLessThan(0.01);
    }
  });
});

describe("chat metrics (Discord)", () => {
  test("comfortable text column starts at 72px: gutter 16 + avatar 40 + gap 16", () => {
    expect(at("comfortable", "space-chat-pad-x") + at("comfortable", "size-avatar-chat") + at("comfortable", "space-chat-avatar-gap")).toBe(72);
  });

  test("chat text is 16px on a 22px line", () => {
    expect(fontSize.prose * lineHeight.chat).toBe(22);
  });

  test("a new speaker sits further from the previous turn than a same-author turn", () => {
    // A turn's top margin is `chat-gap - space-2`; a continued turn's is 0.
    for (const d of ["comfortable", "compact"] as const) {
      expect(at(d, "space-chat-gap") - space[2], d).toBeGreaterThan(0);
    }
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

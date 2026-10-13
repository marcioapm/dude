/**
 * The pair-distance half of the README's "How the colours were chosen"
 * contract, computed from the live token values with no dependencies:
 * sRGB → linear, Machado 2009 protanopia / deuteranopia at severity 1.0
 * (applied in linear RGB, clamped), linear → OKLab, ΔE = Euclidean
 * distance × 100.
 *
 * Roles that wear one colour by design (the brainstorm wears the
 * conductor's) are one slot: identity between them is the glyph's job.
 */

import { describe, expect, test } from "bun:test";
import { AGENT_ROLE_NAMES, TONE_NAMES, accent, peer, roleColourSlot, roleColors, tones, type AgentRoleName } from "../src/tokens/palette.ts";
import { themeColors } from "../src/tokens/themes.ts";
import { tints } from "../src/tokens/tints.ts";

type Vec3 = readonly [number, number, number];
type Mat3 = readonly [Vec3, Vec3, Vec3];
type Vision = "normal" | "protan" | "deutan";

const MODES = ["dark", "light"] as const;

const PAIR_BAR: Record<Vision, number> = { normal: 15, protan: 8, deutan: 8 };
// A role colour this close to the attention fg reads as "needs you".
const ATTENTION_FLOOR = 3;

// Machado, Oliveira & Fernandes 2009, severity 1.0, linear RGB.
const MACHADO: Record<Exclude<Vision, "normal">, Mat3> = {
  protan: [
    [0.152286, 1.052583, -0.204868],
    [0.114503, 0.786281, 0.099216],
    [-0.003882, -0.048116, 1.051998],
  ],
  deutan: [
    [0.367322, 0.860646, -0.227968],
    [0.280085, 0.672501, 0.047413],
    [-0.01182, 0.04294, 0.968881],
  ],
};

function linearRgb(hex: string): Vec3 {
  const ch = (i: number) => {
    const v = Number.parseInt(hex.slice(i, i + 2), 16) / 255;
    return v <= 0.04045 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4);
  };
  return [ch(1), ch(3), ch(5)];
}

const clamp01 = (v: number) => Math.min(1, Math.max(0, v));

function apply(m: Mat3, [r, g, b]: Vec3): Vec3 {
  return [
    clamp01(m[0][0] * r + m[0][1] * g + m[0][2] * b),
    clamp01(m[1][0] * r + m[1][1] * g + m[1][2] * b),
    clamp01(m[2][0] * r + m[2][1] * g + m[2][2] * b),
  ];
}

// Ottosson's OKLab from linear sRGB.
function oklab([r, g, b]: Vec3): Vec3 {
  const l = Math.cbrt(0.4122214708 * r + 0.5363015264 * g + 0.0514459929 * b);
  const m = Math.cbrt(0.2119034982 * r + 0.6806995019 * g + 0.107396993 * b);
  const s = Math.cbrt(0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b);
  return [
    0.2104542553 * l + 0.793617785 * m - 0.0040720468 * s,
    1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s,
    0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s,
  ];
}

function deltaE(a: string, b: string, vision: Vision): number {
  const see = (hex: string) => {
    const lin = linearRgb(hex);
    return oklab(vision === "normal" ? lin : apply(MACHADO[vision], lin));
  };
  const [p, q] = [see(a), see(b)];
  return Math.hypot(p[0] - q[0], p[1] - q[1], p[2] - q[2]) * 100;
}

const VISIONS: readonly Vision[] = ["normal", "protan", "deutan"];
const fmt = (n: number) => n.toFixed(1);

/**
 * One entry per colour slot; roles that share one by design (`ROLE_COLOUR_OF`)
 * are named together ("conductor/brainstorm"). Grouped by the declaration,
 * not by equal hex, so two slots that collide by accident still fail.
 */
function roleSlots(mode: (typeof MODES)[number]): Array<{ name: string; fg: string }> {
  const bySlot = new Map<string, string[]>();
  for (const r of AGENT_ROLE_NAMES) {
    const slot = roleColourSlot(r);
    bySlot.set(slot, [...(bySlot.get(slot) ?? []), r]);
  }
  return [...bySlot].map(([slot, roles]) => ({ name: roles.join("/"), fg: roleColors[mode][slot as AgentRoleName].fg }));
}

describe("role colour slots", () => {
  // The one alias by design is the brainstorm wearing the conductor's colour;
  // any other alias would silently drop that role from the pair checks.
  test("every role but the brainstorm owns its colour, and the brainstorm wears the conductor's", () => {
    const resolved = Object.fromEntries(AGENT_ROLE_NAMES.map((r) => [r, roleColourSlot(r)]));
    const expected = Object.fromEntries(AGENT_ROLE_NAMES.map((r) => [r, r === "brainstorm" ? "conductor" : r]));
    expect(resolved).toEqual(expected);
  });

  test("there are exactly six distinct colour slots in each mode", () => {
    for (const mode of MODES) expect(roleSlots(mode).map((s) => s.name), mode).toHaveLength(6);
  });
});

describe("role colour pair distance (OKLab ΔE×100, Machado 2009 at severity 1.0)", () => {
  for (const mode of MODES) {
    test(`${mode}: every pair of distinct role colours clears normal ≥ 15, protan ≥ 8, deutan ≥ 8`, () => {
      const slots = roleSlots(mode);
      const failures: string[] = [];
      for (let i = 0; i < slots.length; i++) {
        for (let j = i + 1; j < slots.length; j++) {
          const a = slots[i]!;
          const b = slots[j]!;
          const d = VISIONS.map((v) => deltaE(a.fg, b.fg, v));
          if (VISIONS.some((v, k) => d[k]! < PAIR_BAR[v])) {
            failures.push(`${mode} ${a.name} ${a.fg} vs ${b.name} ${b.fg}: normal/protan/deutan ${d.map(fmt).join(" / ")}`);
          }
        }
      }
      expect(failures).toEqual([]);
    });

    // Needs-you is drawn in the attention fg (text), mark (dot, diamond) and
    // solid (the filled pill): a role must stand clear of all three.
    test(`${mode}: no role colour is within ΔE ${ATTENTION_FLOOR} of the attention fg, mark or solid (normal vision)`, () => {
      const failures: string[] = [];
      for (const r of AGENT_ROLE_NAMES) {
        const fg = roleColors[mode][r].fg;
        for (const slot of ["fg", "mark", "solid"] as const) {
          const target = tones[mode].attention[slot];
          const d = deltaE(fg, target, "normal");
          if (d < ATTENTION_FLOOR) {
            failures.push(`${mode} ${r} ${fg} vs attention ${slot} ${target}: normal ${fmt(d)}`);
          }
        }
      }
      expect(failures).toEqual([]);
    });
  }
});

/** `color-mix(in srgb, top p, bottom)` flattened to a hex, as the browser composites a wash. */
function mix(top: string, bottom: string, p: number): string {
  const ch = (h: string, i: number) => Number.parseInt(h.slice(i, i + 2), 16);
  return `#${[1, 3, 5].map((i) => Math.round(ch(top, i) * p + ch(bottom, i) * (1 - p)).toString(16).padStart(2, "0")).join("")}`;
}

// The peer colour is never a mark of identity: it arrives only as a wash
// under a role-coloured rail, so its solid is held to a lower bar than the
// roles' 15 / 8 / 8. No hue in the free bands (near 192 or 320) clears that
// against all twelve neighbours at the roles' lightness.
const PEER_SOLID_BAR: Record<Vision, number> = { normal: 9, protan: 7, deutan: 7 };
// The closest two existing message washes stand 1.42 apart (light steer vs
// failed fill, protan); a peer wash keeps about that distance from each.
const PEER_WASH_BAR = 1;
// And reads as a wash at all: the steer wash stands 2.85 off the dark surface.
const PEER_WASH_FROM_SURFACE = 2.5;

describe("the peer colour (OKLab ΔE×100, Machado 2009 at severity 1.0)", () => {
  for (const mode of MODES) {
    test(`${mode}: the peer solid clears normal ≥ 9, protan ≥ 7, deutan ≥ 7 from every tone solid, the accent and every role`, () => {
      const others: Array<[string, string]> = [
        ...TONE_NAMES.map((t): [string, string] => [`tone ${t}`, tones[mode][t].solid]),
        ["accent", accent[mode].base],
        ...roleSlots(mode).map((s): [string, string] => [`role ${s.name}`, s.fg]),
      ];
      const failures: string[] = [];
      for (const [name, hex] of others) {
        const d = VISIONS.map((v) => deltaE(peer[mode].solid, hex, v));
        if (VISIONS.some((v, k) => d[k]! < PEER_SOLID_BAR[v])) failures.push(`${mode} peer ${peer[mode].solid} vs ${name} ${hex}: ${d.map(fmt).join(" / ")}`);
      }
      expect(failures).toEqual([]);
    });

    test(`${mode}: the peer wash stands ≥ ${PEER_WASH_BAR} from the steer wash, the question wash and the failed fill, and ≥ ${PEER_WASH_FROM_SURFACE} off the surface`, () => {
      const surface = themeColors[mode].surface;
      const wash = mix(peer[mode].solid, surface, tints.peer);
      const others: Array<[string, string]> = [
        ["steer", mix(accent[mode].base, surface, tints.highlight)],
        ["question", mix(tones[mode].attention.solid, surface, tints.highlight)],
        ["failed", mix(tones[mode].danger.bg, surface, tints.failedFill)],
      ];
      const failures: string[] = [];
      for (const [name, hex] of others) {
        const d = VISIONS.map((v) => deltaE(wash, hex, v));
        if (d.some((x) => x < PEER_WASH_BAR)) failures.push(`${mode} peer wash ${wash} vs ${name} ${hex}: ${d.map(fmt).join(" / ")}`);
      }
      expect(failures).toEqual([]);
      expect(deltaE(wash, surface, "normal"), `${mode} peer wash off the surface`).toBeGreaterThanOrEqual(PEER_WASH_FROM_SURFACE);
    });
  }
});

describe("the distance itself", () => {
  test("identical colours are 0 apart and black to white is 100 in normal vision", () => {
    expect(deltaE("#5a358c", "#5a358c", "normal")).toBe(0);
    expect(deltaE("#000000", "#ffffff", "normal")).toBeCloseTo(100, 1);
  });

  test("both simulations keep neutrals neutral and flatten the red–green axis", () => {
    for (const v of ["protan", "deutan"] as const) {
      expect(deltaE("#808080", "#808080", v)).toBe(0);
      const grey = oklab(apply(MACHADO[v], linearRgb("#808080")));
      expect(Math.hypot(grey[1], grey[2]), `${v} grey stays grey`).toBeLessThan(0.002);
      const red = oklab(apply(MACHADO[v], linearRgb("#ff0000")));
      const redNormal = oklab(linearRgb("#ff0000"));
      expect(Math.abs(red[1]), `${v} red loses most of its a*`).toBeLessThan(Math.abs(redNormal[1]) / 3);
    }
  });

  // Expected values below come from an independent calculation (generic
  // matrix code, Ottosson's published linear-sRGB→LMS coefficients), not
  // from these helpers; 2 decimals absorbs the ~4e-8 coefficient rounding.
  test("sRGB decodes on both sides of the 0.04045 knee", () => {
    expect(linearRgb("#0a0a0a")[0]).toBeCloseTo(0.003035269835488375, 12);
    expect(linearRgb("#0b0b0b")[0]).toBeCloseTo(0.003346535763899161, 12);
  });

  test("protan and deutan are the right matrices: an orange is a pinned, different distance from grey in each", () => {
    expect(deltaE("#e07020", "#808080", "protan")).toBeCloseTo(12.011007, 2);
    expect(deltaE("#e07020", "#808080", "deutan")).toBeCloseTo(15.531374, 2);
  });

  test("simulated channels outside [0, 1] are clipped: pure blue against black", () => {
    // Blue simulates to linear RGB (−0.205, 0.099, 1.052) protan and
    // (−0.228, 0.047, 0.969) deutan; unclipped it would be 62.67 / 130.49.
    expect(deltaE("#0000ff", "#000000", "protan")).toBeCloseTo(59.699417, 2);
    expect(deltaE("#0000ff", "#000000", "deutan")).toBeCloseTo(56.706183, 2);
  });
});

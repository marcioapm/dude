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
import { AGENT_ROLE_NAMES, roleColourSlot, roleColors, tones, type AgentRoleName } from "../src/tokens/palette.ts";

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

    test(`${mode}: no role colour is within ΔE ${ATTENTION_FLOOR} of the attention fg (normal vision)`, () => {
      const attention = tones[mode].attention.fg;
      const failures = AGENT_ROLE_NAMES.flatMap((r) => {
        const fg = roleColors[mode][r].fg;
        const d = deltaE(fg, attention, "normal");
        return d < ATTENTION_FLOOR ? [`${mode} ${r} ${fg} vs attention ${attention}: normal ${fmt(d)}`] : [];
      });
      expect(failures).toEqual([]);
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
});

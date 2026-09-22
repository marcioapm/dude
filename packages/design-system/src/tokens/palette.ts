/**
 * Raw palette — the only place hue/lightness/chroma numbers live.
 *
 * Everything is authored in OKLCH so that lightness is perceptual. Semantic
 * roles (see ./themes.ts) pick from these ramps; components never reference
 * a ramp step directly.
 *
 * Design intent
 * - Neutrals are very slightly cool (hue 250, chroma ~0.006). A perfectly
 *   gray dark UI reads muddy; a strongly tinted one reads "branded". The
 *   tint is there to make white text sit calmly, not to be noticed.
 * - Dark surfaces are tiered by lightness, not by shadow. Elevation in dark
 *   mode = lighter surface + hairline border.
 * - Five status tones only: neutral, info, attention, success, danger.
 *   Every domain status maps onto one of these plus a glyph.
 */

import { ALL_AGENT_ROLES, type AgentRole } from "@dude/domain";
import { oklch, toHex, type Oklch } from "./oklch.ts";

// ---------------------------------------------------------------------------
// Neutral ramp (0 = darkest, 12 = lightest). Used for both themes.
// ---------------------------------------------------------------------------

const NEUTRAL_HUE = 250;
const NEUTRAL_CHROMA = 0.007;

const neutralL = [
  0.11, // 0 near-black
  0.145, // 1 app canvas (dark)
  0.175, // 2 surface (dark)
  0.21, // 3 raised (dark)
  0.25, // 4 overlay (dark) / border-subtle
  0.3, // 5 border
  0.38, // 6 border-strong / text-disabled(dark)
  0.5, // 7 text-muted (both)
  0.62, // 8 text-secondary (dark) / text-muted(light)
  0.75, // 9
  0.84, // 10 border (light)
  0.92, // 11 border-subtle (light) / text (dark)
  0.965, // 12 canvas (light)
] as const;

export type NeutralRamp = readonly [
  string, string, string, string, string, string, string,
  string, string, string, string, string, string,
];

export const neutral: NeutralRamp = neutralL.map((l, i) =>
  toHex(oklch(l, i >= 11 ? NEUTRAL_CHROMA * 0.5 : NEUTRAL_CHROMA, NEUTRAL_HUE)),
) as unknown as NeutralRamp;

export const white = "#ffffff";
export const black = "#000000";

// ---------------------------------------------------------------------------
// Tones. Each tone has a light and a dark instance with five slots.
//
//   fg       text/icon on the theme's surface, contrast >= 4.5:1
//   bg       subtle tint behind fg (badges, table row highlights)
//   border   hairline for outlined treatments
//   solid    vivid fill for "loud" treatments; carries `onSolid` text
//   onSolid  text on `solid`
//
// Lightness is deliberately different between success and danger in each
// mode so the two are separable under protan/deutan simulation even before
// the glyph is read.
// ---------------------------------------------------------------------------

export type ToneName = "neutral" | "info" | "attention" | "success" | "danger";
export const TONE_NAMES: readonly ToneName[] = ["neutral", "info", "attention", "success", "danger"];

export interface ToneInstance {
  readonly fg: string;
  readonly bg: string;
  readonly border: string;
  readonly solid: string;
  readonly onSolid: string;
}

interface ToneSpec {
  readonly hue: number;
  readonly chroma: number;
}

const TONE_HUES: Record<ToneName, ToneSpec> = {
  neutral: { hue: NEUTRAL_HUE, chroma: 0.012 },
  info: { hue: 248, chroma: 0.15 },
  attention: { hue: 78, chroma: 0.16 },
  success: { hue: 158, chroma: 0.14 },
  danger: { hue: 22, chroma: 0.19 },
};

/** Per-tone lightness for each slot; fg lightness is the CVD lever. */
const DARK_L: Record<ToneName, { fg: number; bg: number; border: number; solid: number }> = {
  neutral: { fg: 0.74, bg: 0.24, border: 0.34, solid: 0.52 },
  info: { fg: 0.74, bg: 0.25, border: 0.4, solid: 0.55 },
  attention: { fg: 0.82, bg: 0.27, border: 0.46, solid: 0.8 },
  success: { fg: 0.8, bg: 0.25, border: 0.4, solid: 0.7 },
  danger: { fg: 0.7, bg: 0.25, border: 0.42, solid: 0.55 },
};

/**
 * Light-mode fg lightness was chosen by search: [0.40, 0.49, 0.55, 0.40] is
 * the assignment that clears all-pairs CVD ΔE >= 8 and normal ΔE >= 15 on
 * white while keeping every fg >= 4.5:1. Do not nudge these by eye.
 */
const LIGHT_L: Record<ToneName, { fg: number; bg: number; border: number; solid: number }> = {
  neutral: { fg: 0.45, bg: 0.94, border: 0.84, solid: 0.55 },
  info: { fg: 0.4, bg: 0.94, border: 0.8, solid: 0.55 },
  attention: { fg: 0.49, bg: 0.94, border: 0.78, solid: 0.78 },
  success: { fg: 0.55, bg: 0.94, border: 0.8, solid: 0.6 },
  danger: { fg: 0.4, bg: 0.95, border: 0.82, solid: 0.58 },
};

function tone(name: ToneName, mode: "light" | "dark"): ToneInstance {
  const { hue, chroma } = TONE_HUES[name];
  const L = mode === "dark" ? DARK_L[name] : LIGHT_L[name];
  const bgChroma = name === "neutral" ? chroma : mode === "dark" ? chroma * 0.35 : chroma * 0.25;
  const borderChroma = name === "neutral" ? chroma : mode === "dark" ? chroma * 0.6 : chroma * 0.5;
  const solid: Oklch = oklch(L.solid, name === "neutral" ? chroma : chroma, hue);
  // Text on the solid fill: black on bright fills, white on deep fills.
  const onSolid = L.solid >= 0.68 ? black : white;
  return {
    fg: toHex(oklch(L.fg, chroma, hue)),
    bg: toHex(oklch(L.bg, bgChroma, hue)),
    border: toHex(oklch(L.border, borderChroma, hue)),
    solid: toHex(solid),
    onSolid,
  };
}

export const tones: Record<"light" | "dark", Record<ToneName, ToneInstance>> = {
  light: {
    neutral: tone("neutral", "light"),
    info: tone("info", "light"),
    attention: tone("attention", "light"),
    success: tone("success", "light"),
    danger: tone("danger", "light"),
  },
  dark: {
    neutral: tone("neutral", "dark"),
    info: tone("info", "dark"),
    attention: tone("attention", "dark"),
    success: tone("success", "dark"),
    danger: tone("danger", "dark"),
  },
};

// ---------------------------------------------------------------------------
// Agent role colors — categorical identity, never status. Six fixed slots in
// a fixed order. Identity is carried primarily by the glyph and initial in
// AgentAvatar; color is the secondary cue.
// ---------------------------------------------------------------------------

export type AgentRoleName = AgentRole;

export const AGENT_ROLE_NAMES: readonly AgentRoleName[] = ALL_AGENT_ROLES;

const ROLE_HUES: Record<AgentRoleName, number> = {
  orchestrator: 300, // violet — conducts, sits above the others
  investigator: 232, // blue — reads and searches
  implementer: 170, // teal — builds
  reviewer: 40, // orange — scrutinises
  simplifier: 120, // green-yellow — prunes
  qa_browser: 350, // pink — pokes the UI
};

/**
 * Per-role lightness, found by search so that all 15 pairs clear CVD ΔE >= 8
 * and normal-vision ΔE >= 15 in each mode. Lightness varies on purpose: it is
 * what keeps violet/blue and teal/green apart under deutan simulation.
 */
const ROLE_L: Record<"light" | "dark", Record<AgentRoleName, number>> = {
  dark: {
    orchestrator: 0.62,
    investigator: 0.8,
    implementer: 0.68,
    reviewer: 0.62,
    simplifier: 0.86,
    qa_browser: 0.8,
  },
  light: {
    orchestrator: 0.42,
    investigator: 0.62,
    implementer: 0.47,
    reviewer: 0.47,
    simplifier: 0.62,
    qa_browser: 0.62,
  },
};

export interface RoleColor {
  readonly fg: string;
  readonly bg: string;
  readonly solid: string;
  readonly onSolid: string;
}

function roleColor(role: AgentRoleName, mode: "light" | "dark"): RoleColor {
  const h = ROLE_HUES[role];
  const l = ROLE_L[mode][role];
  if (mode === "dark") {
    return {
      fg: toHex(oklch(l, 0.12, h)),
      bg: toHex(oklch(0.27, 0.05, h)),
      solid: toHex(oklch(l, 0.12, h)),
      onSolid: l >= 0.7 ? black : white,
    };
  }
  return {
    fg: toHex(oklch(l, 0.14, h)),
    bg: toHex(oklch(0.94, 0.04, h)),
    solid: toHex(oklch(l, 0.14, h)),
    onSolid: white,
  };
}

export const roleColors: Record<"light" | "dark", Record<AgentRoleName, RoleColor>> = {
  light: Object.fromEntries(AGENT_ROLE_NAMES.map((r) => [r, roleColor(r, "light")])) as Record<
    AgentRoleName,
    RoleColor
  >,
  dark: Object.fromEntries(AGENT_ROLE_NAMES.map((r) => [r, roleColor(r, "dark")])) as Record<
    AgentRoleName,
    RoleColor
  >,
};

// ---------------------------------------------------------------------------
// Human identity colours — for HumanAvatar initials. Eight slots, picked by
// hashing the person's name, so the same person is the same colour on every
// screen without a profile record.
//
// Deliberately *low chroma* (about half of a role colour): a human must never
// be confusable with an agent, and hue alone cannot guarantee that when both
// sets span the wheel. So humans differ on three channels at once — round
// with a ring (shape), letters not a glyph (mark), and muted not vivid
// (saturation). Colour here is only to tell two people apart in a stack.
// ---------------------------------------------------------------------------

export const IDENTITY_SLOTS = 8;
const IDENTITY_HUES: readonly number[] = [20, 65, 110, 155, 200, 245, 290, 335];

export interface IdentityColor {
  readonly fg: string;
  readonly bg: string;
}

function identityColor(hue: number, mode: "light" | "dark"): IdentityColor {
  return mode === "dark"
    ? { fg: toHex(oklch(0.84, 0.07, hue)), bg: toHex(oklch(0.3, 0.035, hue)) }
    : { fg: toHex(oklch(0.42, 0.09, hue)), bg: toHex(oklch(0.93, 0.03, hue)) };
}

export const identityColors: Record<"light" | "dark", readonly IdentityColor[]> = {
  light: IDENTITY_HUES.map((h) => identityColor(h, "light")),
  dark: IDENTITY_HUES.map((h) => identityColor(h, "dark")),
};

// ---------------------------------------------------------------------------
// Accent (interactive). Same hue as info so the UI has one "blue".
// ---------------------------------------------------------------------------

export const accent = {
  light: {
    base: toHex(oklch(0.52, 0.16, 248)),
    hover: toHex(oklch(0.47, 0.16, 248)),
    active: toHex(oklch(0.42, 0.16, 248)),
    subtle: toHex(oklch(0.94, 0.035, 248)),
    ring: toHex(oklch(0.6, 0.17, 248)),
    text: toHex(oklch(0.48, 0.16, 248)),
  },
  dark: {
    base: toHex(oklch(0.64, 0.15, 248)),
    hover: toHex(oklch(0.69, 0.15, 248)),
    active: toHex(oklch(0.59, 0.15, 248)),
    subtle: toHex(oklch(0.26, 0.05, 248)),
    ring: toHex(oklch(0.72, 0.15, 248)),
    text: toHex(oklch(0.76, 0.13, 248)),
  },
} as const;

// ---------------------------------------------------------------------------
// Diff colors — deliberately their own tokens, not the success/danger tones.
// A diff is not a status; it must be readable line after line for minutes.
// ---------------------------------------------------------------------------

export const diff = {
  light: {
    addBg: toHex(oklch(0.95, 0.045, 150)),
    addBgStrong: toHex(oklch(0.88, 0.09, 150)),
    addFg: toHex(oklch(0.42, 0.12, 150)),
    delBg: toHex(oklch(0.95, 0.035, 20)),
    delBgStrong: toHex(oklch(0.88, 0.08, 20)),
    delFg: toHex(oklch(0.48, 0.16, 20)),
    hunkBg: toHex(oklch(0.94, 0.03, 248)),
    hunkFg: toHex(oklch(0.48, 0.1, 248)),
  },
  dark: {
    addBg: toHex(oklch(0.23, 0.045, 150)),
    addBgStrong: toHex(oklch(0.33, 0.09, 150)),
    addFg: toHex(oklch(0.82, 0.12, 150)),
    delBg: toHex(oklch(0.23, 0.04, 20)),
    delBgStrong: toHex(oklch(0.33, 0.09, 20)),
    delFg: toHex(oklch(0.78, 0.13, 20)),
    hunkBg: toHex(oklch(0.23, 0.03, 248)),
    hunkFg: toHex(oklch(0.72, 0.08, 248)),
  },
} as const;

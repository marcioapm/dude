/**
 * Raw palette — the only place hue/lightness/chroma numbers live.
 *
 * Everything is authored in OKLCH so that lightness is perceptual. Semantic
 * roles (see ./themes.ts) pick from these ramps; components never reference
 * a ramp step directly.
 *
 * Design intent
 * - Neutrals are very slightly cool (hue 250, chroma ~0.0035, close to
 *   grey) and the text ramp is soft, Discord/Obsidian-style:
 *   primary text sits around 11–12:1 on its surface rather than 14:1+, so
 *   primary/secondary/muted read as close, even shades rather than a
 *   bright-white-on-black spike. Every slot that carries read content still
 *   clears 4.5:1.
 * - Dark surfaces are tiered by lightness, not by shadow, and are charcoal
 *   rather than near-black (canvas L 0.235, surface 0.27 — Obsidian and
 *   Discord territory). Elevation = a lighter surface; a hairline, where
 *   one is kept, sits close in lightness to the surface under it.
 * - Tinted backgrounds (tone/role/identity/diff bg, accent-subtle) sit a
 *   few steps above the surface so they read as a tint, not a hole.
 * - Five status tones only: neutral, info, attention, success, danger.
 *   Every domain status maps onto one of these plus a glyph.
 */

import { ALL_AGENT_ROLES, type AgentRole } from "@dude/domain";
import { oklch, toHex, type Oklch } from "./oklch.ts";

// ---------------------------------------------------------------------------
// Neutral ramp (0 = darkest, 12 = lightest). A smooth visual ramp for the
// gallery; `./themes.ts` builds each theme's actual colours from precise
// OKLCH lightness values of its own (see the comment there for why it does
// not simply index into this array) and only reaches into this array for
// the two points that must stay literally identical across the codebase:
// the dark theme's canvas/sunken/fieldBg (index 1) and the light theme's
// canvas/sunken (index 12).
// ---------------------------------------------------------------------------

const NEUTRAL_HUE = 250;
const NEUTRAL_CHROMA = 0.0035;
export { NEUTRAL_HUE, NEUTRAL_CHROMA };

const neutralL = [
  0.18, // 0
  0.235, // 1 app canvas / sunken / field bg (dark)
  0.27, // 2
  0.305, // 3
  0.34, // 4
  0.39, // 5
  0.46, // 6
  0.55, // 7
  0.64, // 8
  0.74, // 9
  0.83, // 10
  0.9, // 11
  0.965, // 12 canvas / sunken (light)
] as const;

export type NeutralRamp = readonly [
  string, string, string, string, string, string, string,
  string, string, string, string, string, string,
];

export const neutral: NeutralRamp = neutralL.map((l, i) =>
  toHex(oklch(l, i >= 11 ? NEUTRAL_CHROMA * 0.6 : NEUTRAL_CHROMA, NEUTRAL_HUE)),
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
//   mark     fill of a text-free status mark (dot, diamond, square)
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
  /** Fill of a text-free status mark (dot, diamond, square): >= 3:1 on canvas and surface, at full hue. */
  readonly mark: string;
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
  neutral: { fg: 0.74, bg: 0.29, border: 0.39, solid: 0.52 },
  info: { fg: 0.74, bg: 0.3, border: 0.45, solid: 0.55 },
  attention: { fg: 0.82, bg: 0.32, border: 0.51, solid: 0.8 },
  success: { fg: 0.8, bg: 0.3, border: 0.45, solid: 0.7 },
  danger: { fg: 0.7, bg: 0.3, border: 0.47, solid: 0.55 },
};

/**
 * Light-mode fg lightness was chosen by search: [0.40, 0.49, 0.515, 0.40] is
 * the assignment that clears all-pairs CVD ΔE >= 8 and normal ΔE >= 15 on
 * white while keeping every fg >= 4.5:1 on white and on its own tint (a
 * badge's label on its bg). Success at 0.55 missed the tint (3.9:1), and
 * darkening it at hue 158 pulls it towards attention for deutans, so its
 * light fg also turns to hue 182.5 (`LIGHT_FG_HUE`). Do not nudge these by eye.
 */
const LIGHT_L: Record<ToneName, { fg: number; bg: number; border: number; solid: number }> = {
  neutral: { fg: 0.45, bg: 0.94, border: 0.84, solid: 0.55 },
  info: { fg: 0.4, bg: 0.94, border: 0.8, solid: 0.55 },
  attention: { fg: 0.49, bg: 0.94, border: 0.78, solid: 0.78 },
  success: { fg: 0.515, bg: 0.94, border: 0.8, solid: 0.6 },
  danger: { fg: 0.4, bg: 0.95, border: 0.82, solid: 0.58 },
};
/** Light-mode fg hue where it differs from the tone's; see `LIGHT_L`. */
const LIGHT_FG_HUE: Partial<Record<ToneName, number>> = {
  success: 182.5,
};

/**
 * Light-mode status marks. The light fg is dark enough for body text, and
 * at 8px that reads as olive (attention) or near-black (danger, info), so
 * marks take a lighter step at the tone's own hue that still clears 3:1
 * (non-text contrast) on the canvas. Attention shifts to hue 70 because
 * hue 78 at this lightness falls out of sRGB gamut into olive. Dark marks
 * use the dark fg, which is already bright and saturated.
 */
const LIGHT_MARK: Record<ToneName, { l: number; hue?: number }> = {
  neutral: { l: 0.5 },
  info: { l: 0.55 },
  attention: { l: 0.64, hue: 70 },
  success: { l: 0.58 },
  danger: { l: 0.58 },
};

function tone(name: ToneName, mode: "light" | "dark"): ToneInstance {
  const { hue, chroma } = TONE_HUES[name];
  const L = mode === "dark" ? DARK_L[name] : LIGHT_L[name];
  const bgChroma = name === "neutral" ? chroma : mode === "dark" ? chroma * 0.35 : chroma * 0.25;
  const borderChroma = name === "neutral" ? chroma : mode === "dark" ? chroma * 0.6 : chroma * 0.5;
  const solid: Oklch = oklch(L.solid, name === "neutral" ? chroma : chroma, hue);
  // Text on the solid fill: black on bright fills, white on deep fills.
  const onSolid = L.solid >= 0.68 ? black : white;
  const fg = toHex(oklch(L.fg, chroma, mode === "light" ? (LIGHT_FG_HUE[name] ?? hue) : hue));
  const lm = LIGHT_MARK[name];
  return {
    fg,
    mark: mode === "dark" ? fg : toHex(oklch(lm.l, chroma, lm.hue ?? hue)),
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
// Agent role colors — categorical identity, never status. Six colour slots
// in a fixed order. Identity is carried primarily by the glyph (and, for the
// conversation agents, the round shape) in AgentAvatar; color is the
// secondary cue.
// ---------------------------------------------------------------------------

export type AgentRoleName = AgentRole;

export const AGENT_ROLE_NAMES: readonly AgentRoleName[] = ALL_AGENT_ROLES;

/**
 * Roles that wear another role's colour instead of a slot of their own. The
 * brainstorm is a conversation agent like the conductor: no seventh hue at
 * this chroma and lightness clears the pair bar against all six slots in
 * both modes, and the free hues near 80 read as the attention tone. Its
 * glyph and label tell it apart.
 */
export const ROLE_COLOUR_OF = { brainstorm: "conductor" } as const satisfies Partial<Record<AgentRoleName, AgentRoleName>>;

/** A role that owns a colour slot. */
export type RoleColourSlot = Exclude<AgentRoleName, keyof typeof ROLE_COLOUR_OF>;

/** The role whose colour `role` wears: itself, unless it borrows one. */
export function roleColourSlot(role: AgentRoleName): RoleColourSlot {
  return (ROLE_COLOUR_OF as Partial<Record<AgentRoleName, RoleColourSlot>>)[role] ?? (role as RoleColourSlot);
}

const ROLE_HUES: Record<RoleColourSlot, number> = {
  conductor: 300, // violet — conducts, sits above the others
  investigator: 232, // blue — reads and searches
  implementer: 170, // teal — builds
  reviewer: 40, // orange — scrutinises
  simplifier: 120, // green-yellow — prunes
  qa_browser: 350, // pink — pokes the UI
};

/**
 * Per-slot lightness, found by search so that all 15 pairs clear CVD ΔE >= 8
 * and normal-vision ΔE >= 15 in each mode (OKLab ×100, Machado protan and
 * deutan) while every fg stays >= 4.5:1 on its surface, with hues fixed and
 * lightness moved as little as possible. Lightness varies on purpose: it
 * is what keeps violet/blue and teal/green apart under deutan simulation.
 * `test/paletteDistance.test.ts` enforces the bar.
 */
const ROLE_L: Record<"light" | "dark", Record<RoleColourSlot, number>> = {
  dark: {
    conductor: 0.66,
    investigator: 0.82,
    implementer: 0.71,
    reviewer: 0.66,
    simplifier: 0.86,
    qa_browser: 0.82,
  },
  light: {
    conductor: 0.42,
    investigator: 0.56,
    implementer: 0.45,
    reviewer: 0.47,
    simplifier: 0.56,
    qa_browser: 0.57,
  },
};

export interface RoleColor {
  readonly fg: string;
  readonly bg: string;
  readonly solid: string;
  readonly onSolid: string;
}

function roleColor(role: RoleColourSlot, mode: "light" | "dark"): RoleColor {
  const h = ROLE_HUES[role];
  const l = ROLE_L[mode][role];
  if (mode === "dark") {
    return {
      fg: toHex(oklch(l, 0.12, h)),
      bg: toHex(oklch(0.3, 0.05, h)),
      solid: toHex(oklch(l, 0.12, h)),
      onSolid: l >= 0.65 ? black : white,
    };
  }
  return {
    fg: toHex(oklch(l, 0.14, h)),
    bg: toHex(oklch(0.94, 0.04, h)),
    solid: toHex(oklch(l, 0.14, h)),
    onSolid: white,
  };
}

function roleColorsFor(mode: "light" | "dark"): Record<AgentRoleName, RoleColor> {
  const slots = Object.fromEntries(
    (Object.keys(ROLE_HUES) as RoleColourSlot[]).map((r) => [r, roleColor(r, mode)]),
  ) as Record<RoleColourSlot, RoleColor>;
  return Object.fromEntries(AGENT_ROLE_NAMES.map((r) => [r, slots[roleColourSlot(r)]])) as Record<
    AgentRoleName,
    RoleColor
  >;
}

export const roleColors: Record<"light" | "dark", Record<AgentRoleName, RoleColor>> = {
  light: roleColorsFor("light"),
  dark: roleColorsFor("dark"),
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
    ? { fg: toHex(oklch(0.84, 0.07, hue)), bg: toHex(oklch(0.35, 0.035, hue)) }
    : { fg: toHex(oklch(0.42, 0.09, hue)), bg: toHex(oklch(0.93, 0.03, hue)) };
}

export const identityColors: Record<"light" | "dark", readonly IdentityColor[]> = {
  light: IDENTITY_HUES.map((h) => identityColor(h, "light")),
  dark: IDENTITY_HUES.map((h) => identityColor(h, "dark")),
};

// ---------------------------------------------------------------------------
// Accent (interactive). Same hue as info so the UI has one "blue", at
// chroma 0.13 so it sits calmly against the neutrals; focus ring and link
// contrast stay above the 3:1 / 4.5:1 floors.
// ---------------------------------------------------------------------------

export const accent = {
  light: {
    base: toHex(oklch(0.52, 0.13, 248)),
    hover: toHex(oklch(0.47, 0.13, 248)),
    active: toHex(oklch(0.42, 0.13, 248)),
    subtle: toHex(oklch(0.94, 0.03, 248)),
    ring: toHex(oklch(0.6, 0.14, 248)),
    text: toHex(oklch(0.46, 0.13, 248)),
  },
  dark: {
    // Dark enough for white button labels at 4.5:1; hover and active step
    // darker, as in light, so the label never loses contrast.
    base: toHex(oklch(0.56, 0.13, 248)),
    hover: toHex(oklch(0.52, 0.13, 248)),
    active: toHex(oklch(0.48, 0.13, 248)),
    subtle: toHex(oklch(0.31, 0.045, 248)),
    ring: toHex(oklch(0.7, 0.13, 248)),
    text: toHex(oklch(0.76, 0.11, 248)),
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
    addBg: toHex(oklch(0.28, 0.045, 150)),
    addBgStrong: toHex(oklch(0.38, 0.09, 150)),
    addFg: toHex(oklch(0.82, 0.12, 150)),
    delBg: toHex(oklch(0.28, 0.04, 20)),
    delBgStrong: toHex(oklch(0.38, 0.09, 20)),
    delFg: toHex(oklch(0.78, 0.13, 20)),
    hunkBg: toHex(oklch(0.28, 0.03, 248)),
    hunkFg: toHex(oklch(0.72, 0.08, 248)),
  },
} as const;

// ---------------------------------------------------------------------------
// Merged — GitHub's violet, because every reader of a pull request already
// knows it means "merged". Its own pair, not a tone: it is not a status of
// ours, and nothing else may borrow it.
// ---------------------------------------------------------------------------

export const merged = {
  light: { fg: toHex(oklch(0.48, 0.17, 300)), bg: toHex(oklch(0.95, 0.035, 300)) },
  dark: { fg: toHex(oklch(0.78, 0.12, 300)), bg: toHex(oklch(0.3, 0.06, 300)) },
} as const;

// ---------------------------------------------------------------------------
// ANSI terminal colours — for tool output that arrives with escape codes.
// The tool's own colours are the content (pytest's red FAILED, git's green
// `+`), so they are rendered, not mapped onto the status tones: a green
// line in a test log is not a "success" status of ours. Sixteen slots per
// mode, each tuned to stay readable on the field background: "black" on
// dark is a mid grey rather than invisible, "white" and "bright white" on
// light are greys rather than paper, and every chromatic slot sits in the
// lightness band the theme's text contrast needs. Bright is the same hue
// one step lighter (dark) or one step *darker* (light), so `dim grey`
// (bright black) never vanishes in either mode.
// ---------------------------------------------------------------------------

export const ANSI_COLOR_NAMES = [
  "black",
  "red",
  "green",
  "yellow",
  "blue",
  "magenta",
  "cyan",
  "white",
  "bright-black",
  "bright-red",
  "bright-green",
  "bright-yellow",
  "bright-blue",
  "bright-magenta",
  "bright-cyan",
  "bright-white",
] as const;

export type AnsiColorName = (typeof ANSI_COLOR_NAMES)[number];

/** Hue per chromatic slot (index 1–6 and 9–14). Yellow leans amber so it holds up on white. */
const ANSI_HUES = { red: 25, green: 150, yellow: 85, blue: 255, magenta: 325, cyan: 205 } as const;

function ansiPalette(mode: "light" | "dark"): Record<AnsiColorName, string> {
  const dark = mode === "dark";
  // Chromatic slots: lightness band chosen so each keeps >= 4.5:1 on the
  // field background (verified in tests); bright shifts one step away from
  // the background.
  const normalL = dark ? 0.76 : 0.5;
  const brightL = dark ? 0.86 : 0.44;
  const chroma = dark ? 0.14 : 0.15;
  const chromatic = (l: number, h: number) => toHex(oklch(l, h === ANSI_HUES.yellow ? chroma * 0.95 : chroma, h));
  return {
    // "black" is the colour tools use for "de-emphasised": readable, not gone.
    black: toHex(oklch(dark ? 0.66 : 0.24, NEUTRAL_CHROMA, NEUTRAL_HUE)),
    red: chromatic(normalL, ANSI_HUES.red),
    green: chromatic(normalL, ANSI_HUES.green),
    yellow: chromatic(normalL, ANSI_HUES.yellow),
    blue: chromatic(normalL, ANSI_HUES.blue),
    magenta: chromatic(normalL, ANSI_HUES.magenta),
    cyan: chromatic(normalL, ANSI_HUES.cyan),
    white: toHex(oklch(dark ? 0.86 : 0.5, NEUTRAL_CHROMA, NEUTRAL_HUE)),
    // "bright black" is the classic dim grey; on light it is a step darker
    // than "white" so the two stay distinct, and never paper.
    "bright-black": toHex(oklch(dark ? 0.72 : 0.44, NEUTRAL_CHROMA, NEUTRAL_HUE)),
    "bright-red": chromatic(brightL, ANSI_HUES.red),
    "bright-green": chromatic(brightL, ANSI_HUES.green),
    "bright-yellow": chromatic(brightL, ANSI_HUES.yellow),
    "bright-blue": chromatic(brightL, ANSI_HUES.blue),
    "bright-magenta": chromatic(brightL, ANSI_HUES.magenta),
    "bright-cyan": chromatic(brightL, ANSI_HUES.cyan),
    "bright-white": toHex(oklch(dark ? 0.97 : 0.4, NEUTRAL_CHROMA * 0.5, NEUTRAL_HUE)),
  };
}

export const ansiColors: Record<"light" | "dark", Record<AnsiColorName, string>> = {
  light: ansiPalette("light"),
  dark: ansiPalette("dark"),
};

/**
 * Lightness band for 256-colour and truecolor foregrounds, which arrive as
 * arbitrary RGB. The renderer keeps their hue and chroma and clamps OKLCH
 * lightness into this band, so a tool that prints near-black on the
 * assumption of a light terminal is still legible on the dark field.
 */
export const ansiForegroundLightness: Record<"light" | "dark", { readonly min: number; readonly max: number }> = {
  dark: { min: 0.7, max: 0.97 },
  light: { min: 0.2, max: 0.55 },
};

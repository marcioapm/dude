/**
 * Semantic color roles. Components consume ONLY these (via CSS custom
 * properties); the raw palette in ./palette.ts is never referenced directly.
 *
 * Both modes are designed together. Dark is primary: it is where the operator
 * will spend hours, so it gets the most careful contrast work. Light is a
 * peer, not an inversion — surfaces in light mode use shadow for elevation,
 * dark mode uses lighter surfaces plus hairlines.
 */

import {
  AGENT_ROLE_NAMES,
  ANSI_COLOR_NAMES,
  NEUTRAL_CHROMA,
  NEUTRAL_HUE,
  TONE_NAMES,
  accent,
  ansiColors,
  diff,
  identityColors,
  neutral,
  roleColors,
  tones,
  white,
  type AgentRoleName,
  type ToneName,
} from "./palette.ts";
import { oklch, toHex } from "./oklch.ts";

export type ThemeMode = "light" | "dark";

export interface ThemeColors {
  // Surfaces, from furthest back to nearest
  readonly canvas: string;
  readonly surface: string;
  readonly raised: string;
  readonly overlay: string;
  readonly sunken: string;
  /** Header, toolbar and footer bars inside a panel: one shade off the panel body. */
  readonly chrome: string;

  // Borders
  readonly borderSubtle: string;
  readonly border: string;
  readonly borderStrong: string;

  // Text
  /** Author names and headings: one step past primary, as Discord sets names. */
  readonly textStrong: string;
  readonly textPrimary: string;
  readonly textSecondary: string;
  readonly textMuted: string;
  readonly textDisabled: string;
  readonly textInverse: string;

  // Interaction
  readonly accent: string;
  readonly accentHover: string;
  readonly accentActive: string;
  readonly accentSubtle: string;
  readonly accentText: string;
  readonly onAccent: string;
  readonly focusRing: string;
  readonly selection: string;
  readonly hoverWash: string;
  readonly activeWash: string;
  /** Full-width wash under a hovered transcript row: ~3% ink, quieter than a control's hover. */
  readonly rowHover: string;

  // Component-level surfaces that differ in *kind* between modes (dark
  // fields are sunken, light fields are white) — tokens so CSS never
  // needs to know which mode it is in.
  readonly fieldBg: string;
  /** The composer: the one raised field, a step brighter (dark) or tinted (light) than the transcript. */
  readonly fieldRaised: string;
  readonly secondaryHover: string;
  readonly secondaryActive: string;
  readonly scrim: string;

  // Shadows (as rgba strings usable in box-shadow)
  readonly shadowColor: string;
  readonly shadowColorStrong: string;

  // Live indicator
  readonly live: string;
}

const alpha = (hex: string, a: number): string => {
  const n = Number.parseInt(hex.slice(1), 16);
  const r = (n >> 16) & 255;
  const g = (n >> 8) & 255;
  const b = n & 255;
  return `rgba(${r}, ${g}, ${b}, ${a})`;
};

/**
 * The surface/text/border ladders, in OKLCH lightness. Written explicitly
 * (not indexed into `neutral[]`, which is a coarser 13-step display ramp
 * for the gallery) so every step lands exactly where the contrast search
 * below puts it.
 *
 * Dark surfaces are charcoal, not black: canvas #1d1e20 and surface
 * #252728 sit where Obsidian (#1e1e1e / #262626) and Discord's deepest
 * greys (#1e1f22 / #2b2d31) do. Each step is ΔL 0.035, so regions read as
 * layers, not edges. The text ladder is soft: primary ~11:1 on the surface
 * (Discord runs #dbdee1 on #313338 at ~9.4:1), secondary ~7:1, muted ~5:1 —
 * close shades rather than a bright-white spike over dim grey. Muted still
 * clears 4.5:1 on `raised`, where card and header text sits.
 */
const oklchHex = (l: number, c = NEUTRAL_CHROMA) => toHex(oklch(l, c, NEUTRAL_HUE));
const darkL = {
  canvas: 0.235, // = neutral[1]; also sunken / fieldBg
  surface: 0.27,
  raised: 0.305,
  overlay: 0.34,
  borderSubtle: 0.31,
  border: 0.35,
  borderStrong: 0.42,
  textDisabled: 0.5,
  textMuted: 0.705,
  textSecondary: 0.78,
  textPrimary: 0.905,
  textStrong: 0.96,
};
const lightL = {
  canvas: 0.965, // = neutral[12]; also sunken
  chrome: 0.98,
  borderSubtle: 0.91,
  border: 0.85,
  borderStrong: 0.72,
  textDisabled: 0.66,
  // Muted clears 4.5:1 on the active wash over the canvas (a selected
  // sidebar row, the search field), not only on bare surfaces.
  textMuted: 0.5,
  textSecondary: 0.44,
  textPrimary: 0.32,
  textStrong: 0.18,
};

export const themeColors: Record<ThemeMode, ThemeColors> = {
  dark: {
    canvas: neutral[1],
    surface: oklchHex(darkL.surface),
    raised: oklchHex(darkL.raised),
    overlay: oklchHex(darkL.overlay),
    sunken: neutral[1],
    chrome: oklchHex(darkL.raised),

    borderSubtle: oklchHex(darkL.borderSubtle),
    border: oklchHex(darkL.border),
    borderStrong: oklchHex(darkL.borderStrong),

    textStrong: oklchHex(darkL.textStrong, NEUTRAL_CHROMA * 0.4),
    textPrimary: oklchHex(darkL.textPrimary, NEUTRAL_CHROMA * 0.6),
    textSecondary: oklchHex(darkL.textSecondary),
    textMuted: oklchHex(darkL.textMuted),
    textDisabled: oklchHex(darkL.textDisabled),
    textInverse: neutral[1],

    accent: accent.dark.base,
    accentHover: accent.dark.hover,
    accentActive: accent.dark.active,
    accentSubtle: accent.dark.subtle,
    accentText: accent.dark.text,
    onAccent: white,
    focusRing: accent.dark.ring,
    selection: alpha(accent.dark.base, 0.35),
    hoverWash: alpha(white, 0.05),
    activeWash: alpha(white, 0.09),
    rowHover: alpha(white, 0.03),

    fieldBg: neutral[1],
    fieldRaised: oklchHex(darkL.raised),
    secondaryHover: oklchHex(darkL.overlay),
    secondaryActive: oklchHex(darkL.surface),
    scrim: alpha("#000000", 0.55),

    shadowColor: alpha("#000000", 0.5),
    shadowColorStrong: alpha("#000000", 0.7),

    live: tones.dark.success.fg,
  },
  light: {
    canvas: neutral[12],
    surface: white,
    raised: white,
    overlay: white,
    sunken: neutral[12],
    chrome: oklchHex(lightL.chrome, NEUTRAL_CHROMA * 0.6),

    borderSubtle: oklchHex(lightL.borderSubtle, NEUTRAL_CHROMA * 0.6),
    border: oklchHex(lightL.border),
    borderStrong: oklchHex(lightL.borderStrong),

    textStrong: oklchHex(lightL.textStrong),
    textPrimary: oklchHex(lightL.textPrimary),
    textSecondary: oklchHex(lightL.textSecondary),
    textMuted: oklchHex(lightL.textMuted),
    textDisabled: oklchHex(lightL.textDisabled),
    textInverse: white,

    accent: accent.light.base,
    accentHover: accent.light.hover,
    accentActive: accent.light.active,
    accentSubtle: accent.light.subtle,
    accentText: accent.light.text,
    onAccent: white,
    focusRing: accent.light.ring,
    selection: alpha(accent.light.base, 0.22),
    hoverWash: alpha(oklchHex(lightL.textPrimary), 0.045),
    activeWash: alpha(oklchHex(lightL.textPrimary), 0.08),
    rowHover: alpha(oklchHex(lightL.textPrimary), 0.03),

    fieldBg: white,
    fieldRaised: oklchHex(lightL.chrome, NEUTRAL_CHROMA * 0.6),
    secondaryHover: neutral[12],
    secondaryActive: oklchHex(lightL.borderSubtle, NEUTRAL_CHROMA * 0.6),
    scrim: alpha(oklchHex(lightL.textPrimary), 0.4),

    shadowColor: alpha(oklchHex(lightL.textPrimary), 0.12),
    shadowColorStrong: alpha(oklchHex(lightL.textPrimary), 0.22),

    live: tones.light.success.fg,
  },
};

/**
 * Flatten one theme into `name -> value` pairs, suitable for emitting as
 * custom properties. Keys are kebab-case without the `--ds-` prefix.
 */
export function flattenTheme(mode: ThemeMode): Record<string, string> {
  const out: Record<string, string> = {};
  const c = themeColors[mode];
  for (const [k, v] of Object.entries(c)) out[`color-${kebab(k)}`] = v;

  for (const t of TONE_NAMES) {
    const tone = tones[mode][t];
    out[`tone-${t}-fg`] = tone.fg;
    out[`tone-${t}-bg`] = tone.bg;
    out[`tone-${t}-border`] = tone.border;
    out[`tone-${t}-solid`] = tone.solid;
    out[`tone-${t}-on-solid`] = tone.onSolid;
    out[`tone-${t}-mark`] = tone.mark;
  }
  for (const r of AGENT_ROLE_NAMES) {
    const rc = roleColors[mode][r];
    const key = r.replace("_", "-");
    out[`role-${key}-fg`] = rc.fg;
    out[`role-${key}-bg`] = rc.bg;
    out[`role-${key}-solid`] = rc.solid;
    out[`role-${key}-on-solid`] = rc.onSolid;
  }
  identityColors[mode].forEach((ic, i) => {
    out[`identity-${i}-fg`] = ic.fg;
    out[`identity-${i}-bg`] = ic.bg;
  });
  const d = diff[mode];
  out["diff-add-bg"] = d.addBg;
  out["diff-add-bg-strong"] = d.addBgStrong;
  out["diff-add-fg"] = d.addFg;
  out["diff-del-bg"] = d.delBg;
  out["diff-del-bg-strong"] = d.delBgStrong;
  out["diff-del-fg"] = d.delFg;
  out["diff-hunk-bg"] = d.hunkBg;
  out["diff-hunk-fg"] = d.hunkFg;
  for (const name of ANSI_COLOR_NAMES) out[`ansi-${name}`] = ansiColors[mode][name];

  // Elevation is theme-dependent: light uses shadow, dark uses a lighter
  // surface + hairline plus a much softer shadow for separation from canvas.
  if (mode === "dark") {
    out["shadow-1"] = `0 0 0 1px ${c.borderSubtle}, 0 1px 2px ${c.shadowColor}`;
    out["shadow-2"] = `0 0 0 1px ${c.border}, 0 4px 12px ${c.shadowColor}`;
    out["shadow-3"] = `0 0 0 1px ${c.border}, 0 12px 32px ${c.shadowColorStrong}`;
  } else {
    out["shadow-1"] = `0 0 0 1px ${c.borderSubtle}, 0 1px 2px ${c.shadowColor}`;
    out["shadow-2"] = `0 0 0 1px ${c.borderSubtle}, 0 4px 12px ${c.shadowColor}`;
    out["shadow-3"] = `0 0 0 1px ${c.border}, 0 12px 32px ${c.shadowColorStrong}`;
  }
  return out;
}

function kebab(s: string): string {
  return s.replace(/[A-Z]/g, (m) => `-${m.toLowerCase()}`);
}

export type { AgentRoleName, ToneName };

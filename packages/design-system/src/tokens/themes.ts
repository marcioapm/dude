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
  TONE_NAMES,
  accent,
  diff,
  neutral,
  roleColors,
  tones,
  white,
  type AgentRoleName,
  type ToneName,
} from "./palette.ts";

export type ThemeMode = "light" | "dark";

export interface ThemeColors {
  // Surfaces, from furthest back to nearest
  readonly canvas: string;
  readonly surface: string;
  readonly raised: string;
  readonly overlay: string;
  readonly sunken: string;

  // Borders
  readonly borderSubtle: string;
  readonly border: string;
  readonly borderStrong: string;

  // Text
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

  // Component-level surfaces that differ in *kind* between modes (dark
  // fields are sunken, light fields are white) — tokens so CSS never
  // needs to know which mode it is in.
  readonly fieldBg: string;
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

export const themeColors: Record<ThemeMode, ThemeColors> = {
  dark: {
    canvas: neutral[1],
    surface: neutral[2],
    raised: neutral[3],
    overlay: neutral[4],
    sunken: neutral[1],

    borderSubtle: neutral[4],
    border: neutral[5],
    borderStrong: neutral[6],

    textPrimary: neutral[11],
    textSecondary: neutral[8],
    textMuted: neutral[7],
    textDisabled: neutral[6],
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

    fieldBg: neutral[1],
    secondaryHover: neutral[4],
    secondaryActive: neutral[2],
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

    borderSubtle: neutral[11],
    border: neutral[10],
    borderStrong: neutral[9],

    textPrimary: neutral[1],
    textSecondary: neutral[6],
    textMuted: neutral[7],
    textDisabled: neutral[8],
    textInverse: white,

    accent: accent.light.base,
    accentHover: accent.light.hover,
    accentActive: accent.light.active,
    accentSubtle: accent.light.subtle,
    accentText: accent.light.text,
    onAccent: white,
    focusRing: accent.light.ring,
    selection: alpha(accent.light.base, 0.22),
    hoverWash: alpha(neutral[1], 0.045),
    activeWash: alpha(neutral[1], 0.08),

    fieldBg: white,
    secondaryHover: neutral[12],
    secondaryActive: neutral[11],
    scrim: alpha(neutral[3], 0.4),

    shadowColor: alpha(neutral[3], 0.12),
    shadowColorStrong: alpha(neutral[3], 0.22),

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
  }
  for (const r of AGENT_ROLE_NAMES) {
    const rc = roleColors[mode][r];
    const key = r.replace("_", "-");
    out[`role-${key}-fg`] = rc.fg;
    out[`role-${key}-bg`] = rc.bg;
    out[`role-${key}-solid`] = rc.solid;
    out[`role-${key}-on-solid`] = rc.onSolid;
  }
  const d = diff[mode];
  out["diff-add-bg"] = d.addBg;
  out["diff-add-bg-strong"] = d.addBgStrong;
  out["diff-add-fg"] = d.addFg;
  out["diff-del-bg"] = d.delBg;
  out["diff-del-bg-strong"] = d.delBgStrong;
  out["diff-del-fg"] = d.delFg;
  out["diff-hunk-bg"] = d.hunkBg;
  out["diff-hunk-fg"] = d.hunkFg;

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

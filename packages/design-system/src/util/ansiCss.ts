/**
 * ANSI colours to CSS. Pure: strings in, strings out, results cached.
 *
 * Named colours (the 16 slots) become the theme's `--ds-ansi-*` tokens, so
 * they follow the theme like every other colour. 256-colour and truecolor
 * values arrive as sRGB from a tool that assumed some terminal background
 * we do not have; as text on *our* field (`"fg"`) their OKLCH lightness is
 * clamped into the band the theme's text needs (`ansiForegroundLightness`),
 * hue and chroma kept, and the two results are handed to `light-dark()` so
 * the nearest `color-scheme` (set by the same selectors that set the
 * tokens) picks one. When the tool also set a fill, its ink was chosen
 * against that fill, not ours, and passes through unchanged (`"ink"`), as
 * fills do (`"bg"`).
 */

import { ansiForegroundLightness } from "../tokens/palette.ts";
import { fromRgb, rgbToHex, toHex } from "../tokens/oklch.ts";
import type { AnsiColor } from "./ansi.ts";

/** Where the colour goes: text on our field, text on the tool's fill, or the fill. */
export type AnsiColorUse = "fg" | "ink" | "bg";

function clampedHex(r: number, g: number, b: number, mode: "light" | "dark"): string {
  const c = fromRgb(r, g, b);
  const band = ansiForegroundLightness[mode];
  const l = Math.min(band.max, Math.max(band.min, c.l));
  return toHex({ l, c: c.c, h: c.h });
}

function compute(color: AnsiColor, use: AnsiColorUse): string {
  if (color.kind === "named") return `var(--ds-ansi-${color.name})`;
  if (use !== "fg") return rgbToHex(color.r, color.g, color.b);
  return `light-dark(${clampedHex(color.r, color.g, color.b, "light")}, ${clampedHex(color.r, color.g, color.b, "dark")})`;
}

const cache = new Map<string, string>();

/** The CSS colour for an ANSI colour in the given use. */
export function ansiColorCss(color: AnsiColor, use: AnsiColorUse): string {
  const key = color.kind === "named" ? `${use}:${color.name}` : `${use}:${color.r},${color.g},${color.b}`;
  let css = cache.get(key);
  if (css === undefined) {
    css = compute(color, use);
    cache.set(key, css);
  }
  return css;
}

/** Is an rgb fill light enough to want dark ink on it? */
export function isLightFill(color: { readonly r: number; readonly g: number; readonly b: number }): boolean {
  return fromRgb(color.r, color.g, color.b).l >= 0.6;
}

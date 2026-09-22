/**
 * Minimal OKLCH -> sRGB hex conversion.
 *
 * Tokens are authored in OKLCH because it is perceptually uniform: two colors
 * with the same L read as equally bright, which is what makes a status tone
 * ramp look "even" across hues and what lets us reason about contrast. We
 * resolve to hex at build time so the shipped CSS has no dependency on
 * `oklch()` support in whichever webview hosts the product.
 */

export interface Oklch {
  /** Lightness 0..1 */
  readonly l: number;
  /** Chroma 0..~0.4 */
  readonly c: number;
  /** Hue in degrees */
  readonly h: number;
}

export function oklch(l: number, c: number, h: number): Oklch {
  return { l, c, h };
}

function gammaEncode(x: number): number {
  const v = Math.max(0, Math.min(1, x));
  return v <= 0.0031308 ? 12.92 * v : 1.055 * Math.pow(v, 1 / 2.4) - 0.055;
}

function toHexByte(x: number): string {
  return Math.round(x * 255)
    .toString(16)
    .padStart(2, "0");
}

/** Returns true when the color is inside the sRGB gamut (no clipping). */
export function inGamut({ l, c, h }: Oklch): boolean {
  const [r, g, b] = toLinearRgb(l, c, h);
  const eps = 1e-4;
  return r >= -eps && r <= 1 + eps && g >= -eps && g <= 1 + eps && b >= -eps && b <= 1 + eps;
}

function toLinearRgb(l: number, c: number, h: number): [number, number, number] {
  const rad = (h * Math.PI) / 180;
  const a = c * Math.cos(rad);
  const b = c * Math.sin(rad);

  const l_ = l + 0.3963377774 * a + 0.2158037573 * b;
  const m_ = l - 0.1055613458 * a - 0.0638541728 * b;
  const s_ = l - 0.0894841775 * a - 1.291485548 * b;

  const l3 = l_ * l_ * l_;
  const m3 = m_ * m_ * m_;
  const s3 = s_ * s_ * s_;

  return [
    4.0767416621 * l3 - 3.3077115913 * m3 + 0.2309699292 * s3,
    -1.2684380046 * l3 + 2.6097574011 * m3 - 0.3413193965 * s3,
    -0.0041960863 * l3 - 0.7034186147 * m3 + 1.707614701 * s3,
  ];
}

/**
 * Convert to `#rrggbb`. If the color is out of gamut, chroma is reduced until
 * it fits (hue and lightness are preserved), which is the behaviour a designer
 * expects: "as vivid as this hue can be at this lightness".
 */
export function toHex(color: Oklch): string {
  let { c } = color;
  let rgb = toLinearRgb(color.l, c, color.h);
  let guard = 0;
  while (!inGamut({ l: color.l, c, h: color.h }) && guard++ < 40) {
    c *= 0.94;
    rgb = toLinearRgb(color.l, c, color.h);
  }
  const [r, g, b] = rgb;
  return `#${toHexByte(gammaEncode(r))}${toHexByte(gammaEncode(g))}${toHexByte(gammaEncode(b))}`;
}

/** `#rrggbbaa` with alpha 0..1. */
export function toHexAlpha(color: Oklch, alpha: number): string {
  return `${toHex(color)}${toHexByte(alpha)}`;
}

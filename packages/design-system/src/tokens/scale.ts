/**
 * Non-color scales. Values are px numbers here and emitted as `px` in CSS.
 *
 * Density: the base UI size is 13px, control height 28px, table row 28px.
 * That is the "trading terminal" density — dense enough to see 30+ rows on
 * a laptop, generous enough that nothing touches.
 */

export const space = {
  0: 0,
  2: 2,
  4: 4,
  6: 6,
  8: 8,
  12: 12,
  16: 16,
  20: 20,
  24: 24,
  32: 32,
  40: 40,
  48: 48,
  64: 64,
} as const;
export type SpaceStep = keyof typeof space;

export const radius = {
  none: 0,
  xs: 2,
  sm: 3,
  md: 4,
  lg: 6,
  xl: 8,
  full: 9999,
} as const;

export const fontFamily = {
  sans: `"Inter", "SF Pro Text", ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif`,
  mono: `"JetBrains Mono", "SF Mono", ui-monospace, Menlo, Consolas, "Liberation Mono", monospace`,
} as const;

/** Sizes in px. `md` is the body size for the whole product. */
export const fontSize = {
  "2xs": 10,
  xs: 11,
  sm: 12,
  md: 13,
  lg: 14,
  xl: 16,
  "2xl": 20,
  "3xl": 24,
  "4xl": 32,
} as const;
export type FontSizeStep = keyof typeof fontSize;

export const lineHeight = {
  none: 1,
  tight: 1.2,
  snug: 1.35,
  normal: 1.5,
} as const;

export const fontWeight = {
  regular: 400,
  medium: 500,
  semibold: 600,
} as const;

export const letterSpacing = {
  tight: "-0.01em",
  normal: "0",
  wide: "0.04em",
  caps: "0.06em",
} as const;

/** Control and row heights, px. */
export const size = {
  controlSm: 24,
  controlMd: 28,
  controlLg: 32,
  rowCompact: 24,
  rowDefault: 28,
  rowComfortable: 36,
  iconSm: 12,
  iconMd: 14,
  iconLg: 16,
} as const;

export const duration = {
  instant: 0,
  fast: 80,
  base: 140,
  slow: 240,
  deliberate: 400,
} as const;

export const easing = {
  standard: "cubic-bezier(0.2, 0, 0, 1)",
  enter: "cubic-bezier(0, 0, 0.2, 1)",
  exit: "cubic-bezier(0.4, 0, 1, 1)",
  linear: "linear",
} as const;

export const zIndex = {
  base: 0,
  raised: 1,
  sticky: 10,
  dropdown: 100,
  overlay: 200,
  dialog: 300,
  toast: 400,
  tooltip: 500,
} as const;

export const focusRing = {
  width: 2,
  offset: 1,
} as const;

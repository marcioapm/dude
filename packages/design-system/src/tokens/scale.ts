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

/**
 * Cadence of the looping "live" animations, in ms. Each is a distinct
 * rhythm so that the activity states read differently even in peripheral
 * vision: running spins, live breathes, thinking drifts, a tool sweeps,
 * a cursor blinks. Every loop divides by `--ds-motion-live` so reduced
 * motion freezes it in place.
 */
export const cadence = {
  /** The running spinner. */
  spin: 1100,
  /** The slow opacity breathe on live states and the needs-you ring. */
  breathe: 2400,
  /** A dashed ring rotating slowly: the model is thinking. */
  drift: 3200,
  /** The indeterminate sweep on a tool call that is still running. */
  sweep: 1600,
  /** The streaming text cursor. */
  blink: 1000,
} as const;

/** Reading measures for prose. Chat bubbles are narrower than documents. */
export const measure = {
  /** Agent messages inside a transcript. */
  message: "72ch",
  /** Published artifacts read in full. */
  document: "84ch",
} as const;

export const zIndex = {
  base: 0,
  raised: 1,
  sticky: 10,
  dropdown: 100,
  overlay: 200,
  dialog: 300,
  /**
   * A select's or menu's popup: above a dialog, because one opened from a
   * field in a dialog must be clickable. `dropdown` stays for page-level
   * layers that must sit under an open dialog.
   */
  popover: 350,
  toast: 400,
  tooltip: 500,
} as const;

export const focusRing = {
  width: 2,
  offset: 1,
} as const;

/**
 * Strength of the emphasis tints, as the share of the tone in
 * `color-mix(in srgb, <tone> var(--ds-tint-…), transparent)`. Emitted as
 * `--ds-tint-<kebab-key>` percentages; the palette contrast tests composite
 * the same numbers, so text on these tints is checked against what renders.
 */
export const tints = {
  /** The highlight pattern's wash: a steer turn (accent), a waiting question (attention). */
  highlight: 0.09,
  /** A hovered highlighted turn. */
  highlightHover: 0.12,
  /** The sidebar's pinned needs-you block (attention over the canvas). */
  needsYou: 0.08,
  /** A failed or bad-exit tool call: danger-bg over the surface. */
  failedFill: 0.7,
} as const;

export type TintName = keyof typeof tints;

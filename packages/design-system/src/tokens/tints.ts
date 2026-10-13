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
  /**
   * A message between agents: peer-solid over the surface or the raised
   * thread. Below the highlight's 12% hover because the danger "Not sent"
   * ink (dark) and the tag's secondary ink on the raised thread fall under
   * 4.5:1 there.
   */
  peer: 0.08,
  /** A hovered peer message. */
  peerHover: 0.1,
} as const;

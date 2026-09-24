/**
 * Non-color scales. Values here are the **comfortable** density (the
 * default) and are emitted as `px` in CSS by `scripts/build-tokens.ts`.
 * Compact is NOT a uniform shrink of this scale: `./density.ts` lists a
 * small, explicit set of tokens that get a `[data-density="compact"]`
 * override, and everything else — most of this file — is shared by both
 * densities. See `../../README.md` ("Density") for the reasoning.
 *
 * Type follows Obsidian 1.13's defaults (system UI font; 16px text, 15/13/12
 * UI sizes, 1.5 leading, 700px readable width); the transcript follows
 * Discord's chat metrics (16/22px, 40px avatar, content column at 72px).
 * Comfortable: 15px UI body, 32px default row, 6px default radius. Compact only tightens a handful of *big, structural*
 * measurements (row height, chat avatar, chat/board/main-pane spacing);
 * text stays within 1px of comfortable, icons, small control heights,
 * badge padding and hairline-scale radii do not move at all.
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

/**
 * Named layout spacing that does not sit on the step scale. Comfortable
 * values; `./density.ts` lists which ones compact overrides.
 */
export const spaceNamed = {
  /** Main pane padding (`apps/web` `.main`). */
  mainPad: 24,
  /** Board card padding and the gap between cards. */
  cardPad: 12,
  /** Chat turn horizontal padding (the gutter left of the avatar). */
  chatPadX: 16,
  /** Space above a turn from a new speaker; same-author turns sit 2px apart. */
  chatGap: 17,
  /** Chat avatar to text column: 16 + 40 + 16 puts the text at 72px, as in Discord. */
  chatAvatarGap: 16,
  /** Between major panel sections. */
  panelGap: 24,
  /** NavTree indent per level. */
  treeIndent: 16,
  /** Between consecutive sidebar rows. */
  navRowGap: 2,
} as const;
export type SpaceNamedKey = keyof typeof spaceNamed;

export const radius = {
  none: 0,
  xs: 2,
  sm: 4,
  /** Default control radius. Density-sensitive: 6 comfortable, 5 compact. */
  md: 6,
  /** Cards. Shared. */
  lg: 8,
  /** Dialogs. Shared. */
  xl: 12,
  full: 9999,
} as const;

// Obsidian's stack: the platform UI face (SF Pro on macOS, Noto Sans or the
// distro default on Linux, Segoe UI on Windows), Inter only as a fallback.
export const fontFamily = {
  sans: `ui-sans-serif, -apple-system, BlinkMacSystemFont, system-ui, "Segoe UI", Roboto, "Inter Variable", "Inter", sans-serif`,
  mono: `"JetBrains Mono", "SF Mono", ui-monospace, Menlo, Consolas, "Liberation Mono", monospace`,
} as const;

/**
 * Sizes in px, comfortable density, on Obsidian's UI scale: `sm` 13 and
 * `xs` 12 are its "UI small / smaller", `md` 15 its "UI medium", `xl` 20
 * its "UI large", `prose` 16 its text size. Only `md` and `prose` are
 * density-sensitive (1px less in compact); the small sizes are already at
 * the floor and hold.
 */
export const fontSize = {
  /** Small-caps labels only. Shared. */
  "2xs": 11,
  /** Obsidian "UI smaller". Shared. */
  xs: 12,
  /** Obsidian "UI small": nav rows, metadata. Shared. */
  sm: 13,
  /** UI body. Density-sensitive: 15 comfortable, 14 compact. */
  md: 15,
  lg: 16,
  /** Obsidian "UI large". */
  xl: 20,
  "2xl": 22,
  "3xl": 26,
  "4xl": 34,
  /** Chat and document text. Density-sensitive: 16 comfortable, 15 compact. */
  prose: 16,
  /** Tool output, logs, diffs. Shared. */
  mono: 13,
} as const;
export type FontSizeStep = keyof typeof fontSize;

export const lineHeight = {
  none: 1,
  /** Obsidian "line-height-tight": headings. */
  tight: 1.3,
  snug: 1.35,
  /** Obsidian "line-height-normal": long-form Markdown. */
  normal: 1.5,
  /** Transcript text: Discord's 22px at 16px. */
  chat: 1.375,
  /** Documents and multi-paragraph Markdown. */
  prose: 1.5,
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

/**
 * Control, row and avatar sizes, px, comfortable density. Density-sensitive
 * entries are noted; everything else is shared. `controlSm` and `rowCompact`
 * are already the smallest of their kind and hold across densities, per
 * the rule that small things do not get more compact.
 */
export const size = {
  /** Shared: already the smallest control height. */
  controlSm: 28,
  /** Density-sensitive: 32 comfortable, 30 compact. */
  controlMd: 32,
  /** Density-sensitive: 36 comfortable, 34 compact. */
  controlLg: 36,
  /** Shared: already the smallest row height (dense lists, logs). */
  rowCompact: 28,
  /** Density-sensitive: 32 comfortable, 28 compact — sidebar/nav/board rows. */
  rowDefault: 32,
  /** Density-sensitive: 40 comfortable, 36 compact — two-line rows. */
  rowComfortable: 40,
  /** Shared avatar sizes (nav, headers, stacks) — not the transcript avatar. */
  avatarXs: 16,
  avatarSm: 20,
  avatarMd: 24,
  avatarLg: 32,
  /** Density-sensitive: 40 comfortable, 32 compact — the chat transcript's own avatar. */
  avatarChat: 40,
  /** Badge, pill and chip heights. Shared: small things hold across densities. */
  badgeSm: 16,
  badgeMd: 18,
  chip: 22,
  /** Icon sizes. Shared: icons do not shrink with density, only their ink does. */
  iconSm: 14,
  iconMd: 16,
  iconLg: 18,
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

/**
 * Reading measures for prose, comfortable density. Density-sensitive: at
 * compact the body text is a touch smaller, so both measures widen back to
 * roughly the document width to keep a comparable number of characters per
 * line; see `./density.ts`.
 */
export const measure = {
  /** Agent messages inside a transcript. Density-sensitive: 70ch comfortable, 72ch compact. */
  message: "70ch",
  /** Published artifacts read in full: Obsidian's readable line width. Shared. */
  document: "700px",
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

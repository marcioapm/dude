/**
 * Non-color scales, in px unless noted, at the **comfortable** density.
 * Compact is not a uniform shrink: `./density.ts` lists the few tokens it
 * overrides; everything else here holds in both densities. Type follows
 * Obsidian's UI scale; the transcript follows Discord's chat metrics.
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

/** Named layout spacing that does not sit on the step scale. */
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
  /** Above a sidebar project heading. */
  navSectionGap: 12,
  /** Vertical padding of a two-line needs-you row. */
  attentionRowPadY: 6,
  /** Top and bottom padding of a thought or tool call between turns. */
  asideY: 4,
  /** Vertical padding inside a highlighted turn (the waiting question). */
  highlightY: 8,
  /** Code block padding, vertical and horizontal. */
  codeY: 12,
  codeX: 16,
  /** Vertical padding of a Markdown table cell. */
  cellY: 6,
  /** Composer: padding above and below, and the gap between its three rows. */
  composerY: 8,
  composerGap: 6,
  /** Vertical padding inside the composer's text field. */
  fieldY: 10,
} as const;

export const radius = {
  none: 0,
  xs: 2,
  sm: 4,
  /** Default control radius. */
  md: 6,
  /** Cards. */
  lg: 8,
  /** Dialogs. */
  xl: 12,
  full: 9999,
} as const;

// Obsidian's stack: the platform UI face (SF Pro on macOS, Noto Sans or the
// distro default on Linux, Segoe UI on Windows), Inter only as a fallback.
export const fontFamily = {
  sans: `ui-sans-serif, -apple-system, BlinkMacSystemFont, system-ui, "Segoe UI", Roboto, "Inter Variable", "Inter", sans-serif`,
  mono: `"JetBrains Mono", "SF Mono", ui-monospace, Menlo, Consolas, "Liberation Mono", monospace`,
} as const;

/** Obsidian's UI scale: `xs`/`sm` "UI smaller/small", `md` "UI medium", `xl` "UI large", `prose` its text size. */
export const fontSize = {
  /** Small-caps labels only. */
  "2xs": 11,
  xs: 12,
  /** Nav rows, metadata. */
  sm: 13,
  /** Sidebar rows and section labels: between Obsidian's 13px tree and Discord's 16px channels. */
  nav: 14,
  /** UI body. */
  md: 15,
  lg: 16,
  xl: 20,
  "2xl": 22,
  "3xl": 26,
  "4xl": 34,
  /** Chat and document text. */
  prose: 16,
  /** Tool output, logs, diffs. */
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

/** Control, row, avatar, badge and icon sizes. */
export const size = {
  controlSm: 28,
  controlMd: 32,
  controlLg: 36,
  /** Dense lists, logs. */
  rowCompact: 28,
  /** Sidebar, nav and board rows. */
  rowDefault: 32,
  /** Two-line rows. */
  rowComfortable: 40,
  /** Sidebar work items, epics, projects; tool call rows. */
  rowItem: 32,
  /** Sidebar session rows, thought rows. */
  rowItemSm: 28,
  /** Nav, headers, stacks — not the transcript avatar. */
  avatarXs: 16,
  avatarSm: 20,
  avatarMd: 24,
  avatarLg: 32,
  /** The chat transcript's own avatar. */
  avatarChat: 40,
  badgeSm: 16,
  badgeMd: 18,
  chip: 22,
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

/** Reading measures for prose. */
export const measure = {
  /** Agent messages inside a transcript. */
  message: "70ch",
  /** Published artifacts read in full: Obsidian's readable line width. */
  document: "700px",
  /** A page of the app outside the board and the transcript: a work item, settings. */
  page: "960px",
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

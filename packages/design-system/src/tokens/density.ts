/**
 * The density axis. `comfortable` is the default (Discord/Obsidian feel);
 * `compact` is an opt-in, denser mode — but per Márcio's brief, compact is
 * NOT a uniform shrink of the comfortable scale. Most of the token set
 * (body/prose text is within 1px, icons, small control heights, badge
 * padding, hairline radii, spacing under 8px) is identical in both
 * densities. Compact only tightens a short, explicit list of *big,
 * structural* measurements: row heights, the chat avatar, chat turn
 * padding and inter-speaker gap, board card padding, main pane padding and
 * panel gaps.
 *
 * This file is the single source of truth for that list. Everything not
 * named here is shared: `scripts/build-tokens.ts` emits the values from
 * `./scale.ts` once, in `:root`, and only the compact deltas below get a
 * second declaration under `[data-density="compact"]`.
 */

import { fontSize, radius, size, spaceNamed } from "./scale.ts";

export type Density = "comfortable" | "compact";
export const DENSITIES: readonly Density[] = ["comfortable", "compact"];
export const DEFAULT_DENSITY: Density = "comfortable";

/**
 * `--ds-<key>` -> compact value. Keys match the names `staticVars()` in
 * `scripts/build-tokens.ts` emits for `fontSize`, `radius`, `size`,
 * `spaceNamed` and `measure`. Anything not listed here is shared between
 * densities (no compact rule is emitted for it).
 */
export const compactOverrides: Readonly<Record<string, string>> = {
  // Text: within 1px of comfortable, never below 12px.
  "text-xs": `${fontSize.xs - 1}px`,
  "text-sm": `${fontSize.sm - 1}px`,
  "text-md": `${fontSize.md - 1}px`,
  "text-prose": `${fontSize.prose - 1}px`,

  // Radius: the default control radius only, 1px less.
  "radius-md": `${radius.md - 1}px`,

  // Controls: modest, a couple of px — these are not "big structural"
  // elements, but they are not already-tiny either. `control-sm` and
  // `row-compact` are intentionally absent: they are already the smallest
  // of their kind and hold across densities.
  "size-control-md": `${size.controlMd - 2}px`,
  "size-control-lg": `${size.controlLg - 2}px`,

  // Rows and the chat avatar: the big, structural shrink. A sidebar/board
  // row goes from 32 to 28; a two-line row from 40 to 36; the transcript
  // avatar from 36 to 28.
  "size-row-default": `${size.rowDefault - 4}px`,
  "size-row-comfortable": `${size.rowComfortable - 4}px`,
  "size-avatar-chat": `${size.avatarChat - 8}px`,

  // Big spacing: main pane padding, board card padding, chat turn padding
  // and the gap between speakers, panel/section gaps.
  "space-main-pad": `${spaceNamed.mainPad - 4}px`,
  "space-card-pad": `${spaceNamed.cardPad - 2}px`,
  "space-chat-pad-x": `${spaceNamed.chatPadX - 2}px`,
  "space-chat-pad-y": `${spaceNamed.chatPadY - 4}px`,
  "space-chat-gap": `${spaceNamed.chatGap - 6}px`,
  "space-chat-avatar-gap": `${spaceNamed.chatAvatarGap - 4}px`,
  "space-panel-gap": `${spaceNamed.panelGap - 4}px`,

  // Measure: compact's smaller body text means the comfortable ~70ch
  // measure would read as narrow, so compact widens back toward the
  // document width instead of shrinking further.
  "measure-message": "72ch",
  "measure-document": "72ch",
};

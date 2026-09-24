/**
 * The density axis. `comfortable` is the default and is the scale in
 * `./scale.ts`. `compact` is not a uniform shrink: it is the short list of
 * tokens below. Large layout spacing (main pane padding, chat turn padding
 * and the gap between speakers, board card padding, panel gaps), row
 * heights and the chat avatar shrink meaningfully (avatar 40 → 32, speaker
 * gap 17 → 10, sidebar row 32 → 28, main pad 24 → 16, card pad 12 → 8);
 * body and prose text, the default radius and medium controls lose 1–2px;
 * everything already small (icons, `control-sm`, `row-compact`, badge and
 * chip heights, the 2–6px steps, focus rings, text `2xs`/`xs`/`sm`, mono)
 * is not listed and so holds in both densities.
 *
 * `scripts/build-tokens.ts` emits the comfortable value of every listed
 * token under `[data-density="comfortable"]` and the compact value under
 * `[data-density="compact"]`, so a subtree can switch back either way.
 */

import { fontSize, measure, radius, size, spaceNamed } from "./scale.ts";

export type Density = "comfortable" | "compact";
export const DENSITIES: readonly Density[] = ["comfortable", "compact"];
export const DEFAULT_DENSITY: Density = "comfortable";

const px = (n: number) => `${n}px`;

/** `--ds-<key>` -> [comfortable, compact]. */
const densityPairs = {
  "text-md": [px(fontSize.md), px(fontSize.md - 1)],
  "text-prose": [px(fontSize.prose), px(fontSize.prose - 1)],

  "radius-md": [px(radius.md), px(radius.md - 1)],

  "size-control-md": [px(size.controlMd), px(size.controlMd - 2)],
  "size-control-lg": [px(size.controlLg), px(size.controlLg - 2)],

  "size-row-default": [px(size.rowDefault), px(size.rowDefault - 4)],
  "size-row-comfortable": [px(size.rowComfortable), px(size.rowComfortable - 4)],
  "size-avatar-chat": [px(size.avatarChat), px(size.avatarChat - 8)],

  "space-main-pad": [px(spaceNamed.mainPad), px(spaceNamed.mainPad - 8)],
  "space-card-pad": [px(spaceNamed.cardPad), px(spaceNamed.cardPad - 4)],
  "space-chat-pad-x": [px(spaceNamed.chatPadX), px(spaceNamed.chatPadX - 4)],
  "space-chat-gap": [px(spaceNamed.chatGap), px(spaceNamed.chatGap - 7)],
  "space-chat-avatar-gap": [px(spaceNamed.chatAvatarGap), px(spaceNamed.chatAvatarGap - 4)],
  "space-panel-gap": [px(spaceNamed.panelGap), px(spaceNamed.panelGap - 8)],

  // `ch` scales with the font, so this is characters per line, not width:
  // compact fits two more.
  "measure-message": [measure.message, "72ch"],
} as const satisfies Record<string, readonly [string, string]>;

export type DensityToken = keyof typeof densityPairs;

/** The density-sensitive tokens and their value in each density, keyed without the `--ds-` prefix. */
export const densityTokens: Readonly<Record<Density, Readonly<Record<DensityToken, string>>>> = {
  comfortable: Object.fromEntries(Object.entries(densityPairs).map(([k, [c]]) => [k, c])) as Record<DensityToken, string>,
  compact: Object.fromEntries(Object.entries(densityPairs).map(([k, [, c]]) => [k, c])) as Record<DensityToken, string>,
};

/** The compact values alone: what `[data-density="compact"]` overrides. */
export const compactOverrides: Readonly<Record<DensityToken, string>> = densityTokens.compact;

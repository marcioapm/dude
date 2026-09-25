/**
 * The density axis. `comfortable` is the default and is the scale in
 * `./scale.ts`. `compact` is not a uniform shrink: it is the short list of
 * tokens below. The big-ticket spacing shrinks hard, so compact fits
 * visibly more: the gap between speakers 17 → 8, sidebar rows 32/28 →
 * 26/24, main pad 24 → 12, card pad 12 → 6, code and highlight padding
 * about halved, the chat avatar 40 → 32, long-form leading 1.5 → 1.4.
 * Body and prose text, the default radius and medium controls lose 1–2px;
 * everything already small (icons, `control-sm`, `row-compact`, badge and
 * chip heights, the 2–6px steps, focus rings, text `2xs`/`xs`/`sm`, mono)
 * is not listed and so holds in both densities.
 *
 * `scripts/build-tokens.ts` emits the comfortable value of every listed
 * token under `[data-density="comfortable"]` and the compact value under
 * `[data-density="compact"]`, so a subtree can switch back either way.
 */

import { fontSize, lineHeight, measure, radius, size, spaceNamed } from "./scale.ts";

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
  "size-row-item": [px(size.rowItem), px(size.rowItem - 6)],
  "size-row-item-sm": [px(size.rowItemSm), px(size.rowItemSm - 4)],
  "size-avatar-chat": [px(size.avatarChat), px(size.avatarChat - 8)],

  "space-main-pad": [px(spaceNamed.mainPad), px(spaceNamed.mainPad - 12)],
  "space-card-pad": [px(spaceNamed.cardPad), px(spaceNamed.cardPad - 6)],
  "space-chat-pad-x": [px(spaceNamed.chatPadX), px(spaceNamed.chatPadX - 4)],
  "space-chat-gap": [px(spaceNamed.chatGap), px(spaceNamed.chatGap - 9)],
  "space-chat-avatar-gap": [px(spaceNamed.chatAvatarGap), px(spaceNamed.chatAvatarGap - 4)],
  "space-panel-gap": [px(spaceNamed.panelGap), px(spaceNamed.panelGap - 8)],
  "space-nav-row-gap": [px(spaceNamed.navRowGap), px(spaceNamed.navRowGap - 1)],
  "space-nav-section-gap": [px(spaceNamed.navSectionGap), px(spaceNamed.navSectionGap - 6)],
  "space-attention-row-pad-y": [px(spaceNamed.attentionRowPadY), px(spaceNamed.attentionRowPadY - 3)],
  "space-aside-y": [px(spaceNamed.asideY), px(spaceNamed.asideY - 2)],
  "space-highlight-y": [px(spaceNamed.highlightY), px(spaceNamed.highlightY - 4)],
  "space-code-y": [px(spaceNamed.codeY), px(spaceNamed.codeY - 4)],
  "space-code-x": [px(spaceNamed.codeX), px(spaceNamed.codeX - 4)],
  "space-cell-y": [px(spaceNamed.cellY), px(spaceNamed.cellY - 3)],
  "space-composer-y": [px(spaceNamed.composerY), px(spaceNamed.composerY - 4)],
  "space-composer-gap": [px(spaceNamed.composerGap), px(spaceNamed.composerGap - 2)],
  "space-field-y": [px(spaceNamed.fieldY), px(spaceNamed.fieldY - 4)],
  // Line pitch of a Markdown code block; mono text itself holds at 13px.
  "size-code-line": ["20px", "18px"],

  // Unitless leading: compact chat text is 15px on a 20px line (was 20.6),
  // long-form Markdown 1.4 (was 1.5). Text size itself changes by 1px at most.
  "leading-chat": [String(lineHeight.chat), String(4 / 3)],
  "leading-prose": [String(lineHeight.prose), "1.4"],
  // Between Markdown blocks inside a chat turn.
  "md-gap": ["0.75em", "0.5em"],

  // `ch` scales with the font, so this is characters per line, not width:
  // compact fits two more.
  "measure-message": [measure.message, "72ch"],
} as const satisfies Record<string, readonly [string, string]>;

export type DensityToken = keyof typeof densityPairs;

/** Large layout measures that compact takes in by a visible amount (at least 4px). */
export const LARGE_LAYOUT_TOKENS = [
  "space-main-pad",
  "space-chat-gap",
  "space-panel-gap",
  "space-card-pad",
  "size-avatar-chat",
  "size-row-default",
  "size-row-item",
  "size-row-item-sm",
  "space-code-y",
  "space-code-x",
  "space-highlight-y",
] as const satisfies readonly DensityToken[];

/** The big-ticket padding: compact keeps at most two thirds of it. */
export const BIG_TICKET_TOKENS = [
  "space-main-pad",
  "space-chat-gap",
  "space-card-pad",
  "space-attention-row-pad-y",
  "space-highlight-y",
  "space-code-y",
  "space-cell-y",
  "space-nav-section-gap",
] as const satisfies readonly DensityToken[];

/** The density-sensitive tokens and their value in each density, keyed without the `--ds-` prefix. */
export const densityTokens: Readonly<Record<Density, Readonly<Record<DensityToken, string>>>> = {
  comfortable: Object.fromEntries(Object.entries(densityPairs).map(([k, [c]]) => [k, c])) as Record<DensityToken, string>,
  compact: Object.fromEntries(Object.entries(densityPairs).map(([k, [, c]]) => [k, c])) as Record<DensityToken, string>,
};

/** The compact values alone: what `[data-density="compact"]` overrides. */
export const compactOverrides: Readonly<Record<DensityToken, string>> = densityTokens.compact;

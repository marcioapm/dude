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

export function isDensity(v: unknown): v is Density {
  return (DENSITIES as readonly unknown[]).includes(v);
}

/** `[comfortable, compact]` for a px value that compact takes `by` px off. */
function shrink(n: number, by: number): readonly [string, string] {
  return [`${n}px`, `${n - by}px`];
}

/** `--ds-<key>` -> [comfortable, compact]. */
const densityPairs = {
  "text-md": shrink(fontSize.md, 1),
  "text-prose": shrink(fontSize.prose, 1),

  "radius-md": shrink(radius.md, 1),

  "size-control-md": shrink(size.controlMd, 2),
  "size-control-lg": shrink(size.controlLg, 2),

  "size-row-default": shrink(size.rowDefault, 4),
  "size-row-comfortable": shrink(size.rowComfortable, 4),
  "size-row-item": shrink(size.rowItem, 6),
  "size-row-item-sm": shrink(size.rowItemSm, 4),
  "size-avatar-chat": shrink(size.avatarChat, 8),

  "space-main-pad": shrink(spaceNamed.mainPad, 12),
  "space-card-pad": shrink(spaceNamed.cardPad, 6),
  "space-chat-pad-x": shrink(spaceNamed.chatPadX, 4),
  "space-chat-gap": shrink(spaceNamed.chatGap, 9),
  "space-chat-avatar-gap": shrink(spaceNamed.chatAvatarGap, 4),
  "space-panel-gap": shrink(spaceNamed.panelGap, 8),
  "space-nav-row-gap": shrink(spaceNamed.navRowGap, 1),
  "space-nav-section-gap": shrink(spaceNamed.navSectionGap, 6),
  "space-attention-row-pad-y": shrink(spaceNamed.attentionRowPadY, 3),
  "space-aside-y": shrink(spaceNamed.asideY, 2),
  "space-highlight-y": shrink(spaceNamed.highlightY, 4),
  "space-code-y": shrink(spaceNamed.codeY, 4),
  "space-code-x": shrink(spaceNamed.codeX, 4),
  "space-cell-y": shrink(spaceNamed.cellY, 3),
  "space-composer-y": shrink(spaceNamed.composerY, 4),
  "space-composer-gap": shrink(spaceNamed.composerGap, 2),
  "space-field-y": shrink(spaceNamed.fieldY, 4),
  // Line pitch of a Markdown code block; mono text itself holds at 13px.
  "size-code-line": ["20px", "18px"],

  // Unitless leading: compact chat text is 15px on a whole 20px line;
  // long-form Markdown tightens to 1.4. Text size changes by 1px at most.
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

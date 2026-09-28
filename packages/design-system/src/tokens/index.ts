/**
 * Typed token export. Import this when you need a value in TS (charts,
 * canvas, Tauri-native menus). For styling components, use the CSS custom
 * properties from ./tokens.css — they are the same values.
 */

export * from "./scale.ts";
export * from "./status.ts";
export * from "./activity.ts";
export * from "./triage.ts";
export * from "./density.ts";
export * from "./tints.ts";
export { themeColors, flattenTheme } from "./themes.ts";
export type { ThemeMode, ThemeColors } from "./themes.ts";
export {
  tones,
  roleColors,
  neutral,
  accent,
  diff,
  merged,
  identityColors,
  IDENTITY_SLOTS,
  TONE_NAMES,
  AGENT_ROLE_NAMES,
  ANSI_COLOR_NAMES,
  ansiColors,
  ansiForegroundLightness,
} from "./palette.ts";
export type { ToneName, ToneInstance, AgentRoleName, RoleColor, IdentityColor, AnsiColorName } from "./palette.ts";
export { oklch, toHex } from "./oklch.ts";
export type { Oklch } from "./oklch.ts";

/**
 * Build a `var(--ds-…)` reference. Purely a typing convenience so that
 * component code cannot misspell a token.
 */
export function cssVar(name: TokenName, fallback?: string): string {
  return fallback === undefined ? `var(--ds-${name})` : `var(--ds-${name}, ${fallback})`;
}

/** `chatGap` -> `chat-gap`, the way `scripts/build-tokens.ts` names scale keys. */
type Kebab<S extends string> = S extends `${infer H}${infer T}`
  ? `${H extends Lowercase<H> ? H : `-${Lowercase<H>}`}${Kebab<T>}`
  : S;

/**
 * Names of tokens emitted to CSS. Scale-derived families are exact; the
 * colour families (`color-`, `tone-`, `role-`, `diff-`, `ansi-`) are open.
 */
export type TokenName =
  | import("./density.ts").DensityToken
  | `space-${keyof typeof import("./scale.ts").space}`
  | `space-${Kebab<keyof typeof import("./scale.ts").spaceNamed>}`
  | `radius-${keyof typeof import("./scale.ts").radius}`
  | `radius-face-${keyof typeof import("./scale.ts").faceRadius}`
  | `text-${keyof typeof import("./scale.ts").fontSize}`
  | `leading-${keyof typeof import("./scale.ts").lineHeight}`
  | `weight-${keyof typeof import("./scale.ts").fontWeight}`
  | `tracking-${keyof typeof import("./scale.ts").letterSpacing}`
  | `size-${Kebab<keyof typeof import("./scale.ts").size>}`
  | `focus-ring-${"width" | "offset"}`
  | `tint-${Kebab<keyof typeof import("./tints.ts").tints>}`
  | "motion-live"
  | `font-${"sans" | "mono"}`
  | `color-${string}`
  | `tone-${string}`
  | `role-${string}`
  | `identity-${number}-${"fg" | "bg"}`
  | `diff-${string}`
  | `merged-${"fg" | "bg"}`
  | `ansi-${string}`
  | `shadow-${1 | 2 | 3}`
  | `duration-${keyof typeof import("./scale.ts").duration}`
  | `ease-${keyof typeof import("./scale.ts").easing}`
  | `cadence-${keyof typeof import("./scale.ts").cadence}`
  | `measure-${keyof typeof import("./scale.ts").measure}`
  | `z-${keyof typeof import("./scale.ts").zIndex}`;

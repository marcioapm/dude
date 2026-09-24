/**
 * Typed token export. Import this when you need a value in TS (charts,
 * canvas, Tauri-native menus). For styling components, use the CSS custom
 * properties from ./tokens.css — they are the same values.
 */

export * from "./scale.ts";
export * from "./status.ts";
export * from "./activity.ts";
export * from "./triage.ts";
export { themeColors, flattenTheme } from "./themes.ts";
export type { ThemeMode, ThemeColors } from "./themes.ts";
export {
  tones,
  roleColors,
  neutral,
  accent,
  diff,
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

/** Names of tokens emitted to CSS (non-exhaustive union for autocompletion). */
export type TokenName =
  | `space-${keyof typeof import("./scale.ts").space}`
  | `radius-${keyof typeof import("./scale.ts").radius}`
  | `text-${keyof typeof import("./scale.ts").fontSize}`
  | `font-${"sans" | "mono"}`
  | `color-${string}`
  | `tone-${string}`
  | `role-${string}`
  | `identity-${number}-${"fg" | "bg"}`
  | `diff-${string}`
  | `ansi-${string}`
  | `shadow-${1 | 2 | 3}`
  | `duration-${keyof typeof import("./scale.ts").duration}`
  | `ease-${keyof typeof import("./scale.ts").easing}`
  | `cadence-${keyof typeof import("./scale.ts").cadence}`
  | `measure-${keyof typeof import("./scale.ts").measure}`
  | `z-${keyof typeof import("./scale.ts").zIndex}`;

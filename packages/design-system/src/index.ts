/**
 * @dude/design-system
 *
 * Import order for consumers:
 *   import "@dude/design-system/tokens.css";   // custom properties, themes
 *   import "@dude/design-system/base.css";     // reset + keyframes + utilities
 *   import { Button, StatusBadge } from "@dude/design-system";
 */

export * from "./tokens/index.ts";
export * from "./primitives/index.ts";
export * from "./components/index.ts";
export { Icon, ICON_NAMES } from "./icons/index.tsx";
export type { IconName, IconProps } from "./icons/index.tsx";
export { cx } from "./util/cx.ts";
export {
  formatUsd,
  formatTokens,
  formatDuration,
  formatTimestamp,
  formatPercent,
  shortId,
} from "./util/format.ts";
export type { DurationOptions, TimestampStyle, UsdOptions } from "./util/format.ts";
export { parseMarkdown, safeUrl } from "./util/markdown.ts";
export type { Block as MarkdownBlock, Inline as MarkdownInline, ParseOptions as MarkdownParseOptions } from "./util/markdown.ts";
export { useNow } from "./util/useNow.ts";
export {
  flattenNav,
  ancestorKeys,
  attentionItems,
  globalCounts,
  projectCounts,
  epicCounts,
  workItemTriage,
  workingRoles,
  currentRun,
  navKey,
} from "./util/navModel.ts";
export type {
  NavProject,
  NavEpic,
  NavWorkItem,
  NavRun,
  NavSession,
  NavRef,
  NavKind,
  NavRow,
  NavFilter,
  NavOverrides,
  AttentionItem,
} from "./util/navModel.ts";
export { ThemeProvider, useTheme } from "./theme.tsx";
export type { ThemePreference, ThemeContextValue } from "./theme.tsx";

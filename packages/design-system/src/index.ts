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
  firstName,
  plural,
  formatTokens,
  formatBytes,
  formatDuration,
  formatTimestamp,
  formatPercent,
  shortId,
} from "./util/format.ts";
export type { DurationOptions, TimestampStyle, UsdOptions } from "./util/format.ts";
export { parseMarkdown, safeUrl } from "./util/markdown.ts";
export { parseAnsi } from "./util/ansi.ts";
export type { AnsiColor, AnsiStyle, AnsiSegment, ParseAnsiOptions } from "./util/ansi.ts";
export type { Block as MarkdownBlock, Inline as MarkdownInline, ParseOptions as MarkdownParseOptions } from "./util/markdown.ts";
export { toMs, useNow } from "./util/useNow.ts";
export {
  flattenNav,
  ancestorKeys,
  attentionItems,
  isYours,
  globalCounts,
  projectCounts,
  epicCounts,
  taskTriage,
  workingRoles,
  currentRun,
  navKey,
  waitingWords,
  liveSessions,
  waitingSplit,
  projectPeople,
  taskOwner,
  ownerAgents,
} from "./util/navModel.ts";
export type {
  NavProject,
  NavEpic,
  NavTask,
  NavRun,
  NavSession,
  NavRef,
  NavKind,
  NavRow,
  NavFilter,
  NavOverrides,
  AttentionItem,
} from "./util/navModel.ts";
export {
  BOARD_COLUMN_KINDS,
  BOARD_COLUMN_SPECS,
  BOARD_COLUMN_FOR_STATUS,
  boardColumnOf,
  boardCards,
  boardColumns,
  boardCardCount,
  boardCost,
  boardScope,
  boardSwimlanes,
  liveActivity,
  NO_EPIC_LANE,
} from "./util/boardModel.ts";
export type { BoardColumnKind, BoardColumnSpec, BoardCard, BoardColumn, BoardScope, BoardSwimlane, LiveActivity } from "./util/boardModel.ts";
export { ThemeProvider, useTheme } from "./theme.tsx";
export type { ThemePreference, ThemeContextValue } from "./theme.tsx";
export { describeServer, summarizeServers, canStartAny, canStopAny, bareUrl, serverLogLines } from "./util/servers.ts";
export type { ServerWords, ServerContext } from "./util/servers.ts";

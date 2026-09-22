import { useMemo, useState, type HTMLAttributes, type KeyboardEvent, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { formatDuration } from "../util/format.ts";
import { toMs, useNow } from "../util/useNow.ts";
import { TOOL_SLOW_AFTER_MS, type ToolCallStatus } from "../tokens/activity.ts";
import { DiffView, parseUnifiedDiff, type FileDiff } from "./DiffView.tsx";
import styles from "./ToolCallCard.module.css";

export interface ToolCallCardProps extends Omit<HTMLAttributes<HTMLDivElement>, "children" | "title"> {
  readonly name: string;
  readonly status: ToolCallStatus;
  /** Raw arguments. Objects are summarised to one line when collapsed and pretty-printed when open. */
  readonly args?: unknown;
  /** One-line human summary; overrides the derived args summary. */
  readonly summary?: ReactNode;
  readonly startedAt?: string | number | Date | undefined;
  readonly endedAt?: string | number | Date | null | undefined;
  /** Static duration when `startedAt` is unknown. */
  readonly durationMs?: number | undefined;
  /** Tool output: a string renders in a mono block; a node renders as-is. */
  readonly result?: ReactNode;
  /** A unified diff (string) or parsed files; rendered with DiffView. */
  readonly diff?: string | ReadonlyArray<FileDiff> | undefined;
  /** Error text. Shown in the collapsed row too — errors are never hidden. */
  readonly error?: string | undefined;
  readonly exitCode?: number | undefined;
  readonly icon?: IconName | undefined;
  readonly defaultExpanded?: boolean | undefined;
  readonly expanded?: boolean | undefined;
  readonly onExpandedChange?: ((open: boolean) => void) | undefined;
  /** Promote a running call to "slow" after this long. */
  readonly slowAfterMs?: number | undefined;
  /** Truncate string results above this many lines (the rest is scrollable). */
  readonly maxResultLines?: number | undefined;
}

const TOOL_ICON: ReadonlyArray<readonly [RegExp, IconName]> = [
  [/^(bash|shell|sh|exec|run|terminal)/i, "terminal"],
  [/^(read|write|edit|multiedit|create|view|cat)/i, "file"],
  [/^(grep|glob|search|find|ls|list)/i, "search"],
  [/^(web|fetch|http|browser|navigate|playwright)/i, "globe"],
  [/^(todo|plan)/i, "list-check"],
  [/^(task|agent|spawn|subagent|delegate)/i, "agent"],
  [/^(git|commit|push|pr)/i, "git-branch"],
  [/^(publish|artifact)/i, "layers"],
];

function iconFor(name: string): IconName {
  for (const [re, icon] of TOOL_ICON) if (re.test(name)) return icon;
  return "terminal";
}

const PRIMARY_ARG_KEYS = ["command", "cmd", "file_path", "filePath", "path", "pattern", "query", "url", "prompt", "description", "title", "instruction"];

/** One line describing a call from its arguments, for the collapsed row. */
export function summarizeToolArgs(args: unknown): string {
  if (args === undefined || args === null) return "";
  if (typeof args === "string") return firstLine(args);
  if (typeof args !== "object") return String(args);
  const o = args as Record<string, unknown>;
  // A plan rewrite: say how far along it is, not the JSON.
  if (Array.isArray(o["todos"])) {
    const todos = o["todos"] as ReadonlyArray<{ status?: unknown; content?: unknown }>;
    const done = todos.filter((t) => t.status === "completed").length;
    const cur = todos.find((t) => t.status === "in_progress");
    const head = `${todos.length} ${todos.length === 1 ? "item" : "items"} · ${done} done`;
    return cur && typeof cur.content === "string" ? `${head} · now: ${firstLine(cur.content)}` : head;
  }
  for (const k of PRIMARY_ARG_KEYS) {
    const v = o[k];
    if (typeof v === "string" && v.length > 0) return firstLine(v);
  }
  const parts: string[] = [];
  for (const [k, v] of Object.entries(o)) {
    if (v === undefined) continue;
    const s = typeof v === "string" ? v : JSON.stringify(v);
    parts.push(`${k}: ${firstLine(s ?? "")}`);
    if (parts.length >= 3) break;
  }
  return parts.join("  ");
}

function firstLine(s: string): string {
  const i = s.indexOf("\n");
  return i === -1 ? s : `${s.slice(0, i)} …`;
}

function prettyArgs(args: unknown): string {
  if (typeof args === "string") return args;
  try {
    return JSON.stringify(args, null, 2) ?? "";
  } catch {
    return String(args);
  }
}

const STATUS_ICON: Record<ToolCallStatus, IconName | null> = {
  running: null,
  completed: "check",
  failed: "cross",
  aborted: "stop",
};

/**
 * A tool invocation inline in a transcript. Collapsed it is one 28px row:
 * glyph · name · what it was called with · duration · outcome. Running
 * calls carry a sweep along the bottom edge and a ticking duration that
 * turns attention-toned once the call is slow. Failed calls open by default
 * with the error's first line in the row itself, so an error is never
 * behind a click.
 */
export function ToolCallCard({
  name,
  status,
  args,
  summary,
  startedAt,
  endedAt,
  durationMs,
  result,
  diff,
  error,
  exitCode,
  icon,
  defaultExpanded,
  expanded,
  onExpandedChange,
  slowAfterMs = TOOL_SLOW_AFTER_MS,
  maxResultLines = 40,
  className,
  ...rest
}: ToolCallCardProps) {
  const failed = status === "failed";
  const running = status === "running";
  const [internal, setInternal] = useState(defaultExpanded ?? failed);
  const open = expanded ?? internal;
  const now = useNow(running);
  const start = toMs(startedAt);
  const end = toMs(endedAt);
  const elapsed = start !== null ? Math.max(0, (running || end === null ? now : end) - start) : durationMs ?? null;
  const slow = running && elapsed !== null && elapsed >= slowAfterMs;

  const files = useMemo<ReadonlyArray<FileDiff>>(() => (typeof diff === "string" ? parseUnifiedDiff(diff) : diff ?? []), [diff]);
  const hasBody = args !== undefined || result !== undefined || files.length > 0 || error !== undefined;
  const line = summary ?? summarizeToolArgs(args);

  const toggle = () => {
    if (!hasBody) return;
    const next = !open;
    if (expanded === undefined) setInternal(next);
    onExpandedChange?.(next);
  };
  const onKey = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      toggle();
    }
  };

  const statusIcon = STATUS_ICON[status];
  const resultText = typeof result === "string" ? result : null;
  const resultLines = resultText === null ? 0 : resultText.split("\n").length;

  return (
    <div
      className={cx(styles["root"], styles[status], slow && styles["slow"], open && styles["open"], className)}
      data-tool={name}
      data-status={status}
      data-slow={slow ? "true" : undefined}
      {...rest}
    >
      <div
        className={styles["row"]}
        role={hasBody ? "button" : undefined}
        tabIndex={hasBody ? 0 : undefined}
        aria-expanded={hasBody ? open : undefined}
        onClick={toggle}
        onKeyDown={hasBody ? onKey : undefined}
      >
        <span className={styles["icon"]} aria-hidden>
          <Icon name={icon ?? iconFor(name)} size={12} />
        </span>
        <code className={styles["name"]}>{name}</code>
        <span className={styles["summary"]} title={typeof line === "string" ? line : undefined}>
          {line}
        </span>
        {failed && error ? <span className={styles["errorInline"]}>{firstLine(error)}</span> : null}
        <span className={styles["meta"]}>
          {exitCode !== undefined && exitCode !== 0 ? <span className={styles["exit"]}>exit {exitCode}</span> : null}
          {elapsed !== null ? (
            <span className={cx(styles["duration"], slow && styles["durationSlow"])} title={`${Math.round(elapsed)} ms`}>
              {formatDuration(elapsed)}
            </span>
          ) : null}
          <span className={cx(styles["state"], styles[`state-${status}`])} aria-label={status}>
            {statusIcon ? <Icon name={statusIcon} size={11} strokeWidth={2} /> : <Icon name="spinner" size={11} />}
          </span>
          {hasBody ? <Icon name="chevron-right" size={12} className={styles["chevron"]} /> : <span className={styles["chevronSpacer"]} />}
        </span>
        {running ? (
          <span className={styles["track"]} aria-hidden>
            <span className={styles["sweep"]} />
          </span>
        ) : null}
      </div>

      {hasBody && open ? (
        <div className={styles["body"]}>
          {args !== undefined ? (
            <div className={styles["section"]}>
              <div className={styles["sectionLabel"]}>Arguments</div>
              <pre className={styles["pre"]}>{prettyArgs(args)}</pre>
            </div>
          ) : null}
          {error !== undefined ? (
            <div className={cx(styles["section"], styles["errorSection"])}>
              <div className={styles["sectionLabel"]}>Error</div>
              <pre className={cx(styles["pre"], styles["preError"])}>{error}</pre>
            </div>
          ) : null}
          {files.length > 0 ? (
            <div className={styles["section"]}>
              <div className={styles["sectionLabel"]}>Changes</div>
              <DiffView files={files} summary={files.length > 1} />
            </div>
          ) : null}
          {result !== undefined ? (
            <div className={styles["section"]}>
              <div className={styles["sectionLabel"]}>
                Result
                {resultLines > maxResultLines ? <span className={styles["sectionHint"]}>{resultLines.toLocaleString("en-US")} lines</span> : null}
              </div>
              {resultText !== null ? (
                <pre className={styles["pre"]} style={{ maxHeight: `${maxResultLines * 18 + 16}px` }}>
                  {resultText}
                </pre>
              ) : (
                result
              )}
            </div>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

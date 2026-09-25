import { useEffect, useMemo, useRef, type HTMLAttributes, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { formatBytes, formatDuration } from "../util/format.ts";
import { useDisclosure } from "../util/useDisclosure.ts";
import { useElapsed } from "../util/useNow.ts";
import { TOOL_SLOW_AFTER_MS, type ToolCallStatus } from "../tokens/activity.ts";
import { AnsiString } from "./AnsiString.tsx";
import { DiffView, parseUnifiedDiff, type FileDiff } from "./DiffView.tsx";
import styles from "./ToolCallCard.module.css";

/**
 * A tool's output as the backend delivers it: capped per stream, keeping
 * the head and the tail when the middle was dropped. A plain string is
 * output that fit.
 */
export interface ToolOutput {
  readonly head: string;
  /** The end of the output, when the middle was dropped. */
  readonly tail?: string | undefined;
  /** How much was dropped between `head` and `tail`. Shown as an elision line. */
  readonly omittedBytes?: number | undefined;
}

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
  /**
   * The call's output: stdout, or stdout and stderr merged when the harness
   * does not separate them (OpenCode). Rendered in a mono block with the
   * exit code; an elided middle is marked.
   */
  readonly output?: string | ToolOutput | undefined;
  /** stderr, when the harness reports it separately. A second block, marked. */
  readonly stderr?: string | ToolOutput | undefined;
  /** A non-output result (an artifact id, a rendered node). Strings render in a mono block. */
  readonly result?: ReactNode;
  /** A unified diff (string) or parsed files; rendered with DiffView. */
  readonly diff?: string | ReadonlyArray<FileDiff> | undefined;
  /** Error text. Shown in the collapsed row too — errors are never hidden. */
  readonly error?: string | undefined;
  /** Process exit code. Shown in the collapsed row when non-zero, except `1` on a failed call, which the ✕ already says. */
  readonly exitCode?: number | undefined;
  readonly icon?: IconName | undefined;
  readonly defaultExpanded?: boolean | undefined;
  readonly expanded?: boolean | undefined;
  readonly onExpandedChange?: ((open: boolean) => void) | undefined;
  /** Promote a running call to "slow" after this long. */
  readonly slowAfterMs?: number | undefined;
  /** Scroll output blocks taller than this many lines. */
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

function toOutput(v: string | ToolOutput | undefined): ToolOutput | null {
  if (v === undefined) return null;
  return typeof v === "string" ? { head: v } : v;
}

function lineCount(o: ToolOutput): number {
  return o.head.split("\n").length + (o.tail !== undefined ? o.tail.split("\n").length : 0);
}

function isEmpty(o: ToolOutput): boolean {
  return o.head.length === 0 && (o.tail === undefined || o.tail.length === 0) && (o.omittedBytes ?? 0) === 0;
}

/**
 * What the backend dropped, said once and the same way in the section
 * header and on the separator. Four cases: a tail with a byte count, a
 * tail with none (the middle went, size unknown), a byte count with no
 * tail (the end went — the separator then closes the block), or nothing.
 */
function elision(o: ToolOutput): { readonly hint: string; readonly separator: string } | null {
  const omitted = o.omittedBytes ?? 0;
  const hasTail = o.tail !== undefined;
  if (omitted <= 0 && !hasTail) return null;
  const amount = omitted > 0 ? `${formatBytes(omitted)} omitted` : "middle omitted";
  const kept = hasTail ? "head and tail" : "head only";
  return { hint: `${kept} · ${amount}`, separator: amount };
}

const STATUS_ICON: Record<ToolCallStatus, IconName | null> = {
  running: null,
  completed: "check",
  failed: "cross",
  aborted: "stop",
};

/**
 * A tool invocation inline in a transcript. Collapsed it is one 32px row (26 compact):
 * glyph · name · what it was called with · duration · outcome. Running
 * calls carry a sweep along the bottom edge and a ticking duration that
 * turns attention-toned once the call is slow. Failed calls open by default
 * with the error's first line in the row itself, so an error is never
 * behind a click. A non-zero exit code sits in the row unless it is the
 * plain `1` of a failed call; open, the output block always shows it.
 *
 * Open, the output is a mono block headed by its exit code. The backend
 * caps each stream and keeps the head and tail of anything longer; the
 * dropped middle is drawn as a labelled elision line, never silently
 * joined. stderr, when the harness reports it apart, is a second block
 * marked by label and rail.
 *
 * The container forces colour, so output carries the tool's own escape
 * codes: SGR is rendered (`AnsiString`), everything else is stripped, and a
 * sequence cut by the cap is dropped rather than shown as a fragment.
 * Lines are counted on the raw text — a code never contains a newline.
 */
export function ToolCallCard({
  name,
  status,
  args,
  summary,
  startedAt,
  endedAt,
  durationMs,
  output,
  stderr,
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
  const badExit = exitCode !== undefined && exitCode !== 0;
  const bad = failed || badExit;
  const { open, toggle, onKeyDown, reveal } = useDisclosure({ expanded, defaultExpanded: defaultExpanded ?? bad, onExpandedChange });
  // A card that mounted running and then failed opens itself, keeping the
  // "errors are never behind a click" promise past mount — unless it is
  // controlled or the user already chose (reveal handles both).
  const wasBad = useRef(bad);
  useEffect(() => {
    if (bad && !wasBad.current) reveal(true);
    wasBad.current = bad;
  }, [bad, reveal]);
  const elapsed = useElapsed({ startedAt, endedAt, durationMs, live: running });
  const slow = running && elapsed !== null && elapsed >= slowAfterMs;

  const files = useMemo<ReadonlyArray<FileDiff>>(() => (typeof diff === "string" ? parseUnifiedDiff(diff) : diff ?? []), [diff]);
  const out = toOutput(output);
  const err = toOutput(stderr);
  const hasBody = args !== undefined || out !== null || err !== null || result !== undefined || files.length > 0 || error !== undefined || exitCode !== undefined;
  const line = summary ?? summarizeToolArgs(args);

  const statusIcon = STATUS_ICON[status];
  const resultText = typeof result === "string" ? result : null;
  const resultLines = resultText === null ? 0 : resultText.split("\n").length;
  const maxHeight = `${maxResultLines * 18 + 16}px`;

  return (
    <div
      className={cx(styles["root"], styles[status], slow && styles["slow"], badExit && styles["badExit"], open && styles["open"], className)}
      data-tool={name}
      data-status={status}
      data-slow={slow ? "true" : undefined}
      data-exit-code={exitCode}
      {...rest}
    >
      <div
        className={styles["row"]}
        role={hasBody ? "button" : undefined}
        tabIndex={hasBody ? 0 : undefined}
        aria-expanded={hasBody ? open : undefined}
        onClick={hasBody ? toggle : undefined}
        onKeyDown={hasBody ? onKeyDown : undefined}
      >
        <span className={styles["icon"]} aria-hidden>
          <Icon name={icon ?? iconFor(name)} size={14} />
        </span>
        <code className={styles["name"]}>{name}</code>
        <span className={styles["summary"]} title={typeof line === "string" ? line : undefined}>
          {line}
        </span>
        {failed && error ? <span className={styles["errorInline"]}>{firstLine(error)}</span> : null}
        <span className={styles["meta"]}>
          {badExit && !(failed && exitCode === 1) ? <span className={styles["exit"]}><span className="ds-cap">exit {exitCode}</span></span> : null}
          {elapsed !== null ? (
            <span className={cx(styles["duration"], slow && styles["durationSlow"])} title={`${Math.round(elapsed)} ms`}>
              {formatDuration(elapsed)}
            </span>
          ) : null}
          <span className={cx(styles["state"], styles[`state-${status}`])} aria-label={running ? (slow ? "still running" : "running") : status}>
            {statusIcon ? <Icon name={statusIcon} size={11} strokeWidth={2} /> : <Icon name="spinner" size={11} />}
          </span>
          {hasBody ? <Icon name="chevron-right" size={14} className={styles["chevron"]} /> : <span className={styles["chevronSpacer"]} />}
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
          {out !== null ? (
            <OutputSection label={err !== null ? "stdout" : "Output"} output={out} exitCode={exitCode} running={running} maxHeight={maxHeight} maxLines={maxResultLines} />
          ) : null}
          {err !== null ? (
            <OutputSection label="stderr" output={err} exitCode={out === null ? exitCode : undefined} running={running} maxHeight={maxHeight} maxLines={maxResultLines} stderr />
          ) : null}
          {out === null && err === null && exitCode !== undefined ? (
            <div className={styles["section"]}>
              <div className={styles["sectionLabel"]}>
                Output
                <ExitChip code={exitCode} />
                <span className={styles["sectionHint"]}>no output</span>
              </div>
            </div>
          ) : null}
          {result !== undefined ? (
            <div className={styles["section"]}>
              <div className={styles["sectionLabel"]}>
                Result
                {resultLines > maxResultLines ? <span className={styles["sectionHint"]}>{resultLines.toLocaleString("en-US")} lines</span> : null}
              </div>
              {resultText !== null ? (
                <pre className={styles["pre"]} style={{ maxHeight }}>
                  <AnsiString text={resultText} />
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

function ExitChip({ code }: { readonly code: number }) {
  return (
    <span className={cx(styles["exitChip"], code !== 0 && styles["exitChipBad"])} title={code === 0 ? "Exited normally" : `Exited with code ${code}`}>
      exit {code}
    </span>
  );
}

function OutputSection({
  label,
  output,
  exitCode,
  running,
  maxHeight,
  maxLines,
  stderr,
}: {
  readonly label: string;
  readonly output: ToolOutput;
  readonly exitCode: number | undefined;
  readonly running: boolean;
  readonly maxHeight: string;
  readonly maxLines: number;
  readonly stderr?: boolean | undefined;
}) {
  const lines = lineCount(output);
  const elided = elision(output);
  const empty = isEmpty(output);
  const caret = running ? <span className={styles["outCaret"]} aria-hidden /> : null;
  return (
    <div className={cx(styles["section"], stderr && styles["stderrSection"])}>
      <div className={styles["sectionLabel"]}>
        {label}
        {exitCode !== undefined ? <ExitChip code={exitCode} /> : null}
        {running ? <span className={styles["sectionHint"]}>so far</span> : null}
        {empty ? <span className={styles["sectionHint"]}>empty</span> : null}
        {elided ? <span className={styles["sectionHint"]}>{elided.hint}</span> : lines > maxLines ? <span className={styles["sectionHint"]}>{lines.toLocaleString("en-US")} lines</span> : null}
      </div>
      {!empty ? (
        <div className={cx(styles["pre"], styles["out"], stderr && styles["preStderr"])} style={{ maxHeight }} role="region" aria-label={label}>
          <span className={styles["outText"]}>
            <AnsiString text={output.head} />
            {output.tail === undefined ? caret : null}
          </span>
          {elided ? (
            <span className={styles["elision"]} role="separator" aria-label={elided.separator}>
              <span className={styles["elisionLine"]} aria-hidden />
              <span className={styles["elisionText"]}>{elided.separator}</span>
              <span className={styles["elisionLine"]} aria-hidden />
            </span>
          ) : null}
          {output.tail !== undefined ? (
            <span className={styles["outText"]}>
              <AnsiString text={output.tail} cutStart />
              {caret}
            </span>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

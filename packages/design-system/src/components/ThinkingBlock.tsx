import { useState, type HTMLAttributes, type KeyboardEvent } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { formatDuration } from "../util/format.ts";
import { toMs, useNow } from "../util/useNow.ts";
import { Markdown } from "./Markdown.tsx";
import styles from "./ThinkingBlock.module.css";

export interface ThinkingBlockProps extends Omit<HTMLAttributes<HTMLDivElement>, "children" | "title"> {
  /** The reasoning text. Usually prose; rendered as Markdown unless `plain`. */
  readonly text: string;
  /** Still growing. The row says "Thinking", the preview follows the tail and the duration ticks. */
  readonly streaming?: boolean | undefined;
  readonly startedAt?: string | number | Date | undefined;
  readonly endedAt?: string | number | Date | null | undefined;
  /** Static duration when `startedAt` is unknown. */
  readonly durationMs?: number | undefined;
  readonly defaultExpanded?: boolean | undefined;
  readonly expanded?: boolean | undefined;
  readonly onExpandedChange?: ((open: boolean) => void) | undefined;
  /** Render the text as plain pre-wrapped text instead of Markdown. */
  readonly plain?: boolean | undefined;
  /** Replace the row label ("Thought" / "Thinking"). */
  readonly label?: string | undefined;
}

/** First non-empty line, for the collapsed preview of a finished thought. */
function firstLine(s: string): string {
  for (const line of s.split("\n")) {
    const t = line.trim();
    if (t.length > 0) return t;
  }
  return "";
}

/** Last non-empty line, for the collapsed preview of a thought still growing. */
function lastLine(s: string): string {
  const lines = s.split("\n");
  for (let i = lines.length - 1; i >= 0; i--) {
    const t = lines[i]?.trim() ?? "";
    if (t.length > 0) return t;
  }
  return "";
}

/** Strip the Markdown a preview line would otherwise show literally. */
function unmark(s: string): string {
  return s
    .replace(/^#{1,6}\s+/, "")
    .replace(/^([-*+]|\d+[.)])\s+/, "")
    .replace(/^>\s?/, "")
    .replace(/`([^`]*)`/g, "$1")
    .replace(/\*\*([^*]*)\*\*/g, "$1")
    .replace(/\*([^*]*)\*/g, "$1");
}

/**
 * The model's reasoning, which arrives between its messages and its tool
 * calls. It is secondary to what the agent actually says, so it is quieter
 * than a message (no avatar, no frame, muted ink, 24px) and distinct from a
 * tool call (no surface, no border, a brain rather than a tool glyph).
 * Collapsed it is one line: brain · label · a preview of the thought · how
 * long it took. Dozens in a row read as a faint ledger, not a wall.
 *
 * While streaming the brain sits inside the drifting dashed ring — the
 * `thinking` rhythm from the activity vocabulary, shared with nothing else
 * — the preview follows the latest line and the duration ticks.
 */
export function ThinkingBlock({
  text,
  streaming,
  startedAt,
  endedAt,
  durationMs,
  defaultExpanded,
  expanded,
  onExpandedChange,
  plain,
  label,
  className,
  ...rest
}: ThinkingBlockProps) {
  const live = streaming === true;
  const [internal, setInternal] = useState(defaultExpanded ?? false);
  const open = expanded ?? internal;
  const now = useNow(live);
  const start = toMs(startedAt);
  const end = toMs(endedAt);
  const elapsed = start !== null ? Math.max(0, (live || end === null ? now : end) - start) : durationMs ?? null;

  const toggle = () => {
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

  const preview = unmark(live ? lastLine(text) : firstLine(text));
  const text_ = label ?? (live ? "Thinking" : "Thought");

  return (
    <div className={cx(styles["root"], live && styles["live"], open && styles["open"], className)} data-streaming={live ? "true" : undefined} aria-busy={live || undefined} {...rest}>
      <div className={styles["row"]} role="button" tabIndex={0} aria-expanded={open} onClick={toggle} onKeyDown={onKey}>
        <span className={styles["glyph"]} aria-hidden>
          {live ? <Icon name="circle-dotted" size={16} strokeWidth={1.5} className={styles["ring"]} /> : null}
          <Icon name="brain" size={live ? 9 : 12} className={styles["brain"]} />
        </span>
        <span className={styles["label"]}>{text_}</span>
        {!open ? (
          <span className={styles["preview"]} title={preview.length > 0 ? preview : undefined}>
            {preview.length > 0 ? preview : live ? "…" : "(empty)"}
          </span>
        ) : (
          <span className={styles["spacer"]} />
        )}
        {elapsed !== null ? (
          <span className={styles["duration"]} title={`${Math.round(elapsed)} ms`}>
            {formatDuration(elapsed)}
          </span>
        ) : null}
        <Icon name="chevron-right" size={12} className={styles["chevron"]} />
      </div>
      {open ? (
        <div className={styles["body"]}>
          {plain ? (
            <div className={styles["plain"]}>
              {text}
              {live ? <span className={styles["caret"]} aria-hidden /> : null}
            </div>
          ) : (
            <Markdown source={text} streaming={live} />
          )}
        </div>
      ) : null}
    </div>
  );
}

import { type HTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { formatDuration } from "../util/format.ts";
import { parseInline, plain } from "../util/markdown.ts";
import { useDisclosure } from "../util/useDisclosure.ts";
import { useElapsed } from "../util/useNow.ts";
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

const FENCE_LINE = /^(`{3,}|~{3,})/;

/**
 * The first (or, `fromEnd`, the last) line worth previewing: non-blank
 * and not a code fence. Scans from one end with indexOf rather than
 * splitting a thought that may run to kilobytes on every render.
 */
function previewLine(text: string, fromEnd: boolean): string {
  let lo = 0;
  let hi = text.length;
  while (lo < hi) {
    let line: string;
    if (fromEnd) {
      const i = text.lastIndexOf("\n", hi - 1);
      line = text.slice(i + 1, hi);
      hi = i < 0 ? 0 : i;
    } else {
      const i = text.indexOf("\n", lo);
      const end = i < 0 ? text.length : i;
      line = text.slice(lo, end);
      lo = end + 1;
    }
    const t = line.trim();
    if (t.length > 0 && !FENCE_LINE.test(t)) return t;
  }
  return "";
}

/** Strip the Markdown a preview line would otherwise show literally. */
function unmark(line: string): string {
  const body = line.replace(/^(#{1,6}\s+|([-*+]|\d+[.)])\s+|>\s?)/, "");
  return plain(parseInline(body, false));
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
  plain: plainText,
  label,
  className,
  ...rest
}: ThinkingBlockProps) {
  const live = streaming === true;
  const { open, toggle, onKeyDown } = useDisclosure({ expanded, defaultExpanded, onExpandedChange });
  const elapsed = useElapsed({ startedAt, endedAt, durationMs, live });

  const preview = unmark(previewLine(text, live));
  const rowLabel = label ?? (live ? "Thinking" : "Thought");

  return (
    <div className={cx(styles["root"], live && styles["live"], open && styles["open"], className)} data-streaming={live ? "true" : undefined} aria-busy={live || undefined} {...rest}>
      <div className={styles["row"]} role="button" tabIndex={0} aria-expanded={open} onClick={toggle} onKeyDown={onKeyDown}>
        <span className={styles["glyph"]} aria-hidden>
          {live ? <Icon name="circle-dotted" size={16} strokeWidth={1.5} className={styles["ring"]} /> : null}
          <Icon name="brain" size={live ? 9 : 14} className={styles["brain"]} />
        </span>
        <span className={styles["label"]}>{rowLabel}</span>
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
        <Icon name="chevron-right" size={14} className={styles["chevron"]} />
      </div>
      {open ? (
        <div className={styles["body"]}>
          {plainText ? (
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

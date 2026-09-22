import { useCallback, useEffect, useLayoutEffect, useRef, useState, type HTMLAttributes, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Button } from "../primitives/Button.tsx";
import { formatTimestamp } from "../util/format.ts";
import styles from "./LogStream.module.css";

export type LogLevel = "debug" | "info" | "warn" | "error" | "system";
export type LogChannel = "stdout" | "stderr";

export interface LogLine {
  /** Monotonic, unique; used as the React key. */
  readonly seq: number;
  readonly text: string;
  readonly ts?: string | number | Date | undefined;
  readonly level?: LogLevel | undefined;
  readonly channel?: LogChannel | undefined;
}

export interface LogStreamProps extends Omit<HTMLAttributes<HTMLDivElement>, "children" | "title"> {
  readonly lines: ReadonlyArray<LogLine>;
  readonly title?: ReactNode;
  readonly toolbar?: ReactNode;
  /** Show timestamps column. */
  readonly timestamps?: boolean | undefined;
  readonly lineNumbers?: boolean | undefined;
  /** Disable soft wrap (horizontal scroll instead). */
  readonly nowrap?: boolean | undefined;
  /** Still receiving lines; shows a cursor at the end. */
  readonly live?: boolean | undefined;
  /** Fill parent height instead of `maxHeight`. */
  readonly fill?: boolean | undefined;
  readonly maxHeight?: number | string | undefined;
  /**
   * Keep only the last N lines in the DOM. The `lines` array is the
   * consumer's; this is a render window, not a data cap. For > ~5k lines
   * pass a windowed array or use a virtualiser.
   */
  readonly renderLimit?: number | undefined;
  readonly emptyMessage?: ReactNode;
}

/**
 * High-volume monospace log. Follows the tail while you are at the bottom;
 * the moment you scroll up it stops following and offers a "Jump to
 * latest" button, so reading history is never yanked away from you.
 */
export function LogStream({
  lines,
  title,
  toolbar,
  timestamps = false,
  lineNumbers = true,
  nowrap,
  live,
  fill,
  maxHeight = 360,
  renderLimit = 2000,
  emptyMessage = "No output yet.",
  className,
  ...rest
}: LogStreamProps) {
  const viewportRef = useRef<HTMLDivElement>(null);
  const [following, setFollowing] = useState(true);
  const lastSeqRef = useRef<number>(-1);
  const [newFrom, setNewFrom] = useState<number>(Number.POSITIVE_INFINITY);

  const window = lines.length > renderLimit ? lines.slice(lines.length - renderLimit) : lines;
  const firstShownSeq = window[0]?.seq;
  const hidden = lines.length - window.length;

  const scrollToBottom = useCallback(() => {
    const el = viewportRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, []);

  // Track "new" lines for a brief flash, and follow the tail.
  useLayoutEffect(() => {
    const last = lines[lines.length - 1];
    if (last && last.seq > lastSeqRef.current) {
      setNewFrom(lastSeqRef.current + 1);
      lastSeqRef.current = last.seq;
      if (following) scrollToBottom();
    }
  }, [lines, following, scrollToBottom]);

  useEffect(() => {
    if (!Number.isFinite(newFrom)) return;
    const id = setTimeout(() => setNewFrom(Number.POSITIVE_INFINITY), 600);
    return () => clearTimeout(id);
  }, [newFrom]);

  const onScroll = () => {
    const el = viewportRef.current;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 8;
    if (atBottom !== following) setFollowing(atBottom);
  };

  return (
    <div
      className={cx(styles["root"], fill && styles["fill"], nowrap && styles["nowrap"], className)}
      style={fill ? undefined : { maxHeight }}
      {...rest}
    >
      {title !== undefined || toolbar !== undefined ? (
        <div className={styles["toolbar"]}>
          {title !== undefined ? <span className={styles["toolbarTitle"]}>{title}</span> : null}
          <span className={styles["toolbarSpacer"]} />
          {toolbar}
          <span className={styles["count"]}>
            {lines.length.toLocaleString("en-US")} {lines.length === 1 ? "line" : "lines"}
          </span>
        </div>
      ) : null}
      <div
        ref={viewportRef}
        className={styles["viewport"]}
        onScroll={onScroll}
        role="log"
        aria-live={live ? "polite" : "off"}
        aria-relevant="additions"
        tabIndex={0}
      >
        {lines.length === 0 ? <div className={styles["empty"]}>{emptyMessage}</div> : null}
        {hidden > 0 ? <div className={styles["empty"]}>… {hidden.toLocaleString("en-US")} earlier lines not rendered</div> : null}
        {window.map((l, i) => (
          <div
            key={l.seq}
            className={cx(
              styles["line"],
              l.level && styles[l.level],
              l.channel === "stderr" && styles["stderr"],
              l.seq >= newFrom && styles["lineNew"],
            )}
            data-seq={l.seq}
          >
            <span className={styles["lineNo"]} aria-hidden>
              {lineNumbers ? (firstShownSeq !== undefined ? hidden + i + 1 : i + 1) : ""}
            </span>
            <span className={styles["ts"]}>{timestamps && l.ts !== undefined ? formatTimestamp(l.ts, "time-ms") : ""}</span>
            <span className={styles["text"]}>
              {l.text}
              {live && i === window.length - 1 ? <span className={styles["cursor"]} aria-hidden /> : null}
            </span>
          </div>
        ))}
      </div>
      {!following && lines.length > 0 ? (
        <Button
          size="sm"
          variant="secondary"
          leadingIcon="arrow-down-to-line"
          className={styles["followBtn"]}
          onClick={() => {
            setFollowing(true);
            scrollToBottom();
          }}
        >
          Jump to latest
        </Button>
      ) : null}
    </div>
  );
}

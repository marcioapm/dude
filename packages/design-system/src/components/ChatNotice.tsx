import type { HTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { formatTimestamp } from "../util/format.ts";
import styles from "./ChatNotice.module.css";

/**
 * What the factory did to the session, as the transcript tells it — or,
 * in a task's Chat, a decision its delivery waits on, or something that
 * happened that wakes nobody.
 */
export type ChatNoticeKind = "parked" | "unparked" | "nudged" | "decision" | "notice";

const GLYPH: Record<ChatNoticeKind, IconName> = { parked: "pause", unparked: "retry", nudged: "clock", decision: "hand", notice: "info" };

export interface ChatNoticeProps extends Omit<HTMLAttributes<HTMLDivElement>, "children"> {
  readonly kind: ChatNoticeKind;
  /** One sentence: what happened and what ends it. */
  readonly text: string;
  readonly at: string | number | Date;
  /** Who says it, when that is not plain from where it is: dude's name for the task, in a task's Chat. */
  readonly by?: string | undefined;
}

/**
 * Something the factory did to the session, not something anyone said: it
 * was parked while it waits for a person (its container stopped, nothing
 * held), taken back up, or nudged after going quiet. A centred muted line
 * between turns — no avatar, no frame — so it reads as the transcript's
 * margin note, never as a message. `by` signs it, its name in primary ink.
 */
export function ChatNotice({ kind, text, at, by, className, ...rest }: ChatNoticeProps) {
  const when = new Date(at);
  return (
    <div role="note" className={cx(styles["root"], className)} data-kind={kind} {...rest}>
      <span className={styles["rule"]} aria-hidden />
      <span className={styles["body"]}>
        <Icon name={GLYPH[kind]} size={12} className={styles["glyph"]} />
        <span className={styles["text"]}>{by ? <><b className={styles["by"]}>{by}</b>{": "}</> : null}{text}</span>
        <time className={styles["time"]} dateTime={Number.isNaN(when.getTime()) ? undefined : when.toISOString()} title={when.toLocaleString()}>
          {formatTimestamp(at, "time")}
        </time>
      </span>
      <span className={styles["rule"]} aria-hidden />
    </div>
  );
}

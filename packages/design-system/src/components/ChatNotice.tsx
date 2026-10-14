import type { HTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { formatTimestamp } from "../util/format.ts";
import styles from "./ChatNotice.module.css";

/**
 * What the factory did to the session, as the transcript tells it — or,
 * in a task's Chat, a decision its delivery waits on, something that
 * happened that wakes nobody, a commit the conductor published, or a Run
 * restarted in its place; in a brainstorm session, who named it.
 */
export type ChatNoticeKind = "parked" | "unparked" | "nudged" | "decision" | "notice" | "commit" | "restart" | "stopped" | "renamed";

const GLYPH: Record<ChatNoticeKind, IconName> = { parked: "pause", unparked: "retry", nudged: "clock", decision: "hand", notice: "info",
  commit: "git-branch", restart: "retry", stopped: "warning", renamed: "edit" };

export interface ChatNoticeProps extends Omit<HTMLAttributes<HTMLDivElement>, "children"> {
  readonly kind: ChatNoticeKind;
  /** One sentence: what happened and what ends it. */
  readonly text: string;
  readonly at: string | number | Date;
  /** Who says it, when that is not plain from where it is: dude's name for the task, in a task's Chat. */
  readonly by?: string | undefined;
  /**
   * The words dude put in front of the agent's next input for what the
   * line reports, verbatim: quoted under the line, "Told it:" before them.
   */
  readonly told?: string | undefined;
}

/**
 * Something the factory did to the session, not something anyone said: it
 * was parked while it waits for a person (its container stopped, nothing
 * held), taken back up, or nudged after going quiet. A centred muted line
 * between turns — no avatar, no frame — so it reads as the transcript's
 * margin note, never as a message. `by` signs it, its name in primary ink.
 * `told` quotes, under the line, what the agent was told of it.
 */
export function ChatNotice({ kind, text, at, by, told, className, ...rest }: ChatNoticeProps) {
  const when = new Date(at);
  const line = (
    <>
      <span className={styles["rule"]} aria-hidden />
      <span className={styles["body"]}>
        <Icon name={GLYPH[kind]} size={12} className={styles["glyph"]} />
        <span className={styles["text"]}>{by ? <><b className={styles["by"]}>{by}</b>{": "}</> : null}{text}</span>
        <time className={styles["time"]} dateTime={Number.isNaN(when.getTime()) ? undefined : when.toISOString()} title={when.toLocaleString()}>
          {formatTimestamp(at, "time")}
        </time>
      </span>
      <span className={styles["rule"]} aria-hidden />
    </>
  );
  if (!told) {
    return <div role="note" className={cx(styles["root"], className)} data-kind={kind} {...rest}>{line}</div>;
  }
  return (
    <div role="note" className={cx(styles["told"], className)} data-kind={kind} {...rest}>
      <div className={styles["root"]}>{line}</div>
      <blockquote className={styles["quote"]}>
        <span className={cx(styles["quoteLabel"], "ds-cap")}>Told it</span>
        <span className={styles["quoteText"]}>{told}</span>
      </blockquote>
    </div>
  );
}

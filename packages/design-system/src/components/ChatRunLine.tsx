import type { HTMLAttributes, ReactNode } from "react";
import type { AgentRole, RunStatus } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { AgentAvatar, ROLE_LABEL } from "./AgentAvatar.tsx";
import { StatusMark } from "./StatusMark.tsx";
import styles from "./ChatRunLine.module.css";

export interface ChatRunLineProps extends Omit<HTMLAttributes<HTMLDivElement>, "children" | "title"> {
  readonly role: AgentRole;
  readonly status: RunStatus;
  readonly statusLabel?: string | undefined;
  readonly replacement?: ReactNode;
  /** What it is, after its role: "correctness", "fix of 2 findings". */
  readonly what?: ReactNode;
  /** Short facts at the end: its duration, its cost. */
  readonly facts?: readonly ReactNode[] | undefined;
  /** Open the Run's own session. The whole line is the link. */
  readonly onOpen: () => void;
  /**
   * The conductor's steers of this Run, where they landed: each a steer
   * turn (ChatMessage, intent `steer`) with its delivery state, under the
   * line on the same rail.
   */
  readonly steers?: ReactNode;
}

/**
 * A Run the conductor started, in its Chat: one collapsed line on a rail
 * in the Run's role colour — its face, its role and what it is, its
 * status, and what it came to — that opens the Run's own session. The
 * conductor's conversation stays the subject; each Run is a line in it,
 * where it started, never its transcript. The conductor's steers of it
 * sit under it.
 */
export function ChatRunLine({ role, status, statusLabel, replacement, what, facts = [], onOpen, steers, className, ...rest }: ChatRunLineProps) {
  return (
    <div className={cx(styles["root"], styles[role], className)} data-role={role} data-status={status} {...rest}>
      <button type="button" className={styles["line"]} onClick={onOpen} aria-label={`Open the ${ROLE_LABEL[role]}'s session`}>
        <Icon name="chevron-right" size={12} className={styles["chevron"]} />
        <AgentAvatar role={role} size="xs" live={status === "running"} />
        <span className={styles["role"]}>{ROLE_LABEL[role]}</span>
        {what ? <span className={styles["what"]}>· {what}</span> : null}
        <span className={styles["end"]}>
          <StatusMark status={status} label={statusLabel} size="sm" />
          {facts.map((fact, i) => <span key={i}>{fact}</span>)}
        </span>
      </button>
      {replacement}
      {steers ? <div className={styles["steers"]} data-testid="run-line-steers">{steers}</div> : null}
    </div>
  );
}

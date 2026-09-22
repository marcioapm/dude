import { useState, type CSSProperties, type HTMLAttributes, type KeyboardEvent, type ReactNode } from "react";
import type { AgentRole, SessionStatus } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { ACTIVITY_FOR_SESSION_STATUS, type ActivityKind } from "../tokens/activity.ts";
import { statusSpec } from "../tokens/status.ts";
import { AgentAvatar, ROLE_LABEL } from "./AgentAvatar.tsx";
import { ActivityIndicator, type ActivityIndicatorProps } from "./ActivityIndicator.tsx";
import { StatusBadge } from "./StatusBadge.tsx";
import { CostDisplay, Duration } from "./Numbers.tsx";
import styles from "./ChatThread.module.css";

export interface ChatThreadProps extends Omit<HTMLAttributes<HTMLElement>, "children" | "title"> {
  readonly sessionId: string;
  readonly role: AgentRole;
  readonly status: SessionStatus;
  readonly model?: string | undefined;
  /** The task the parent delegated; shown as the thread title. */
  readonly task: string;
  /** Finer than `status`; defaults from the status. */
  readonly activity?: ActivityKind | undefined;
  readonly activityProps?: Omit<ActivityIndicatorProps, "kind"> | undefined;
  /** One line about what the child is doing right now, for the collapsed header. */
  readonly summary?: ReactNode;
  readonly startedAt?: string | number | Date | undefined;
  readonly endedAt?: string | number | Date | null | undefined;
  readonly costUsd?: number | undefined;
  /** Nesting depth; the transcript sets it. Depth ≥ 2 flattens to a rail. */
  readonly depth?: number | undefined;
  readonly defaultCollapsed?: boolean | undefined;
  readonly collapsed?: boolean | undefined;
  readonly onCollapsedChange?: ((collapsed: boolean) => void) | undefined;
  /** Open this session in its own transcript. Rendered as an action in the header. */
  readonly onOpen?: ((sessionId: string) => void) | undefined;
  /** Number of turns inside, for the collapsed header. */
  readonly turnCount?: number | undefined;
  readonly children?: ReactNode;
}

/**
 * A subagent's conversation, nested inside its parent's transcript.
 *
 * Delineation: a 2px rail in the child's *role* colour down the left, a
 * header that reads "Investigator · task…" with its status, and the
 * child's turns indented under it. The rail is what keeps depth legible:
 * a reviewer inside an implementer inside the orchestrator is three
 * differently-coloured rails, side by side, not three shades of grey.
 *
 * Depth policy: depth 0 (the watched session) has no rail. Depth 1 nests
 * fully. Depth 2 nests but starts collapsed. Depth 3+ collapses to the
 * header only with an "Open" action — beyond that, indentation eats the
 * measure and the operator should follow the child into its own view.
 *
 * Finished children fold up: status dot, cost, duration, one line. Live
 * children stay open and their activity shows in the header even when
 * collapsed, so a collapsed thread still tells you it is waiting on a
 * 40-second bash.
 */
export function ChatThread({
  sessionId,
  role,
  status,
  model,
  task,
  activity,
  activityProps,
  summary,
  startedAt,
  endedAt,
  costUsd,
  depth = 1,
  defaultCollapsed,
  collapsed,
  onCollapsedChange,
  onOpen,
  turnCount,
  children,
  className,
  ...rest
}: ChatThreadProps) {
  const spec = statusSpec(status);
  const finished = spec.terminal;
  const pinnedClosed = depth >= 3;
  const [internal, setInternal] = useState(defaultCollapsed ?? (finished || depth >= 2));
  const isCollapsed = pinnedClosed || (collapsed ?? internal);
  const act: ActivityKind | null = activity ?? ACTIVITY_FOR_SESSION_STATUS[status];
  const live = spec.live;

  const toggle = () => {
    if (pinnedClosed) {
      onOpen?.(sessionId);
      return;
    }
    const next = !isCollapsed;
    if (collapsed === undefined) setInternal(next);
    onCollapsedChange?.(next);
  };
  const onKey = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      toggle();
    }
  };

  return (
    <section
      className={cx(styles["root"], styles[role], live && styles["live"], finished && styles["finished"], isCollapsed && styles["collapsed"], pinnedClosed && styles["headerOnly"], className)}
      style={{ "--depth": depth } as CSSProperties}
      data-session-id={sessionId}
      data-status={status}
      data-depth={depth}
      aria-label={`${ROLE_LABEL[role]} subagent: ${task}`}
      {...rest}
    >
      <div className={styles["header"]} role="button" tabIndex={0} aria-expanded={pinnedClosed ? undefined : !isCollapsed} onClick={toggle} onKeyDown={onKey}>
        <Icon name="chevron-right" size={12} className={styles["chevron"]} />
        <AgentAvatar role={role} size="xs" live={status === "running"} />
        <span className={styles["roleName"]}>{ROLE_LABEL[role]}</span>
        <span className={styles["task"]} title={task}>
          {task}
        </span>
        {model ? <code className={styles["model"]}>{model}</code> : null}
        <span className={styles["meta"]}>
          {isCollapsed && live && act !== null && act !== "completed" ? (
            <ActivityIndicator kind={act} variant="badge" size="sm" {...activityProps} />
          ) : (
            <StatusBadge status={status} size="sm" variant={finished ? "dot" : "badge"} />
          )}
          {isCollapsed && turnCount !== undefined ? <span className={styles["turns"]}>{turnCount} turns</span> : null}
          {costUsd !== undefined ? <CostDisplay usd={costUsd} tone="muted" /> : null}
          {startedAt !== undefined ? <Duration since={startedAt} until={endedAt ?? (live ? null : undefined)} tone="muted" /> : null}
          {onOpen ? (
            <button
              type="button"
              className={styles["open"]}
              aria-label="Open session"
              title="Open session"
              onClick={(e) => {
                e.stopPropagation();
                onOpen(sessionId);
              }}
            >
              <Icon name="external" size={12} />
            </button>
          ) : null}
        </span>
      </div>
      {isCollapsed && summary !== undefined ? <div className={styles["summary"]}>{summary}</div> : null}
      {!isCollapsed ? (
        <div className={styles["body"]} role="group">
          {children}
        </div>
      ) : null}
    </section>
  );
}

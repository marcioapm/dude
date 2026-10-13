import { type CSSProperties, type HTMLAttributes, type ReactNode } from "react";
import type { SessionStatus } from "@dude/domain";
import type { AgentRoleName } from "../tokens/palette.ts";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { ACTIVITY_FOR_SESSION_STATUS, type ActivityKind } from "../tokens/activity.ts";
import { statusSpec } from "../tokens/status.ts";
import { activateOnKey, useDisclosure } from "../util/useDisclosure.ts";
import { AgentAvatar, ROLE_LABEL } from "./AgentAvatar.tsx";
import { ActivityIndicator, type ActivityIndicatorProps } from "./ActivityIndicator.tsx";
import { StatusBadge } from "./StatusBadge.tsx";
import { CostDisplay, Duration } from "./Numbers.tsx";
import styles from "./ChatThread.module.css";

export interface ChatThreadProps extends Omit<HTMLAttributes<HTMLElement>, "children" | "title"> {
  readonly sessionId: string;
  /** A child session of a tool call is `subagent`. */
  readonly role: AgentRoleName;
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
  /** `null` = not reported; shows "—". */
  readonly costUsd?: number | null | undefined;
  /** Nesting depth; the transcript sets it. Depth ≥ 2 flattens to a rail. */
  readonly depth?: number | undefined;
  readonly defaultCollapsed?: boolean | undefined;
  readonly openOnly?: boolean | undefined;
  readonly collapsed?: boolean | undefined;
  readonly onCollapsedChange?: ((collapsed: boolean) => void) | undefined;
  /** Open this session in its own transcript. Rendered as an action in the header. */
  readonly onOpen?: ((sessionId: string) => void) | undefined;
  /** Number of turns inside, for the collapsed header. */
  readonly turnCount?: number | undefined;
  /**
   * The thread is live and blocked on something nothing in it can answer
   * (a permission its harness asked for): the rail turns attention and the
   * header says "waiting <dur> on <on>". Not an activity kind: the thread
   * is still live; what is stuck is said once, here.
   */
  readonly waiting?: ChatThreadWaiting | undefined;
  readonly children?: ReactNode;
}

export interface ChatThreadWaiting {
  /** What it waits on, in words: "a permission", "external_directory". */
  readonly on: string;
  readonly since: string | number | Date;
  /** More, under the header (what was asked, for which paths). */
  readonly detail?: ReactNode;
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
  openOnly = false,
  onCollapsedChange,
  onOpen,
  turnCount,
  waiting,
  children,
  className,
  ...rest
}: ChatThreadProps) {
  const spec = statusSpec(status);
  const finished = spec.terminal;
  const pinnedClosed = openOnly || depth >= 3;
  const disclosure = useDisclosure({
    expanded: collapsed === undefined ? undefined : !collapsed,
    defaultExpanded: !(defaultCollapsed ?? (finished || depth >= 2)),
    onExpandedChange: onCollapsedChange === undefined ? undefined : (open) => onCollapsedChange(!open),
  });
  const isCollapsed = pinnedClosed || !disclosure.open;
  const act: ActivityKind | null = activity ?? ACTIVITY_FOR_SESSION_STATUS[status];
  const live = spec.live;
  const isWaiting = waiting !== undefined && live;
  let headerStatus: ReactNode;
  if (isWaiting) {
    headerStatus = (
      <span className={styles["waitingOn"]} data-testid="chat-thread-waiting">
        <Icon name="pause" size={12} />
        waiting <Duration since={waiting.since} live /> on {waiting.on}
      </span>
    );
  } else if (isCollapsed && live && act !== null && act !== "completed") {
    headerStatus = <ActivityIndicator kind={act} variant="badge" size="sm" {...activityProps} />;
  } else {
    headerStatus = <StatusBadge status={status} size="sm" variant={finished ? "dot" : "badge"} />;
  }

  // Header-only depth cannot open in place; its header opens the session instead.
  const toggle = pinnedClosed ? () => onOpen?.(sessionId) : disclosure.toggle;
  const onKey = pinnedClosed ? activateOnKey(toggle) : disclosure.onKeyDown;

  return (
    <section
      className={cx(styles["root"], styles[role], live && styles["live"], finished && styles["finished"], isCollapsed && styles["collapsed"], pinnedClosed && styles["headerOnly"], isWaiting && styles["waiting"], className)}
      style={{ "--depth": depth } as CSSProperties}
      data-session-id={sessionId}
      data-status={status}
      data-depth={depth}
      data-waiting={isWaiting ? "true" : undefined}
      data-open-only={openOnly ? "true" : undefined}
      aria-label={`${ROLE_LABEL[role]} subagent: ${task}`}
      onClick={openOnly ? () => onOpen?.(sessionId) : undefined}
      {...rest}
    >
      <div className={styles["header"]} role="button" tabIndex={0} aria-expanded={pinnedClosed ? undefined : !isCollapsed} onClick={(event) => { event.stopPropagation(); toggle(); }} onKeyDown={onKey}>
        <Icon name="chevron-right" size={12} className={styles["chevron"]} />
        <AgentAvatar role={role} size="xs" live={status === "running"} />
        <span className={styles["roleName"]}>{ROLE_LABEL[role]}</span>
        <span className={styles["task"]} title={task}>
          {task}
        </span>
        {model ? <code className={styles["model"]}>{model}</code> : null}
        <span className={styles["meta"]}>
          {headerStatus}
          {isCollapsed && turnCount !== undefined ? <span className={styles["turns"]}>{turnCount} turns</span> : null}
          {costUsd !== undefined ? <CostDisplay usd={costUsd} tone="muted" /> : null}
          {startedAt !== undefined ? <Duration since={startedAt} until={endedAt} live={live} tone="muted" /> : null}
          {onOpen && !openOnly ? (
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
      {!openOnly && isWaiting && waiting.detail !== undefined ? <div className={styles["waitingDetail"]}>{waiting.detail}</div> : null}
      {isCollapsed && summary !== undefined ? <div className={styles["summary"]}>{summary}</div> : null}
      {!isCollapsed ? (
        <div className={styles["body"]} role="group">
          {children}
        </div>
      ) : null}
    </section>
  );
}

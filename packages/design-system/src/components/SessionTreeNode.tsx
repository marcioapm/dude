import { useState, type CSSProperties, type HTMLAttributes, type KeyboardEvent, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import type { AgentRoleName } from "../tokens/palette.ts";
import type { SessionStatus } from "../tokens/status.ts";
import { AgentAvatar, ROLE_LABEL } from "./AgentAvatar.tsx";
import { StatusBadge } from "./StatusBadge.tsx";
import { CostDisplay, Duration, TokenCount } from "./Numbers.tsx";
import styles from "./SessionTreeNode.module.css";

export interface SessionNodeData {
  readonly id: string;
  readonly role: AgentRoleName;
  readonly status: SessionStatus;
  readonly model?: string | undefined;
  /** Short title; defaults to the role label. */
  readonly title?: string | undefined;
  /** What the agent is doing right now (live sessions only). */
  readonly activity?: string | undefined;
  readonly costUsd?: number | undefined;
  readonly tokens?: number | undefined;
  readonly startedAt?: string | number | Date | undefined;
  readonly endedAt?: string | number | Date | null | undefined;
  readonly children?: ReadonlyArray<SessionNodeData> | undefined;
}

export interface SessionTreeNodeProps extends Omit<HTMLAttributes<HTMLDivElement>, "children" | "onSelect"> {
  readonly node: SessionNodeData;
  readonly depth?: number | undefined;
  readonly selectedId?: string | null | undefined;
  readonly onSelect?: ((id: string) => void) | undefined;
  readonly defaultExpanded?: boolean | undefined;
  /** Render extra trailing content (a menu, a link). */
  readonly trailing?: ((node: SessionNodeData) => ReactNode) | undefined;
}

const LIVE: ReadonlySet<SessionStatus> = new Set(["running", "waiting_on_human"]);
const FINISHED: ReadonlySet<SessionStatus> = new Set(["completed", "failed", "aborted"]);

/**
 * One session in the run's session tree, recursive. The orchestrator sits
 * at depth 0 and its subagents nest under it. Guide lines mark depth;
 * cost/tokens/duration sit in a fixed right column so a tree of twenty
 * sessions can be summed by eye.
 */
export function SessionTreeNode({
  node,
  depth = 0,
  selectedId,
  onSelect,
  defaultExpanded = true,
  trailing,
  className,
  ...rest
}: SessionTreeNodeProps) {
  const [open, setOpen] = useState(defaultExpanded);
  const hasChildren = node.children !== undefined && node.children.length > 0;
  const live = LIVE.has(node.status);
  const finished = FINISHED.has(node.status);
  const selected = selectedId === node.id;

  const onKey = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      onSelect?.(node.id);
    } else if (e.key === "ArrowRight" && hasChildren && !open) {
      e.preventDefault();
      setOpen(true);
    } else if (e.key === "ArrowLeft" && hasChildren && open) {
      e.preventDefault();
      setOpen(false);
    }
  };

  return (
    <>
      <div
        className={cx(styles["node"], selected && styles["selected"], finished && styles["finished"], className)}
        style={{ "--depth": depth } as CSSProperties}
        role="treeitem"
        aria-level={depth + 1}
        aria-expanded={hasChildren ? open : undefined}
        aria-selected={selected}
        tabIndex={0}
        onClick={() => onSelect?.(node.id)}
        onKeyDown={onKey}
        data-session-id={node.id}
        data-status={node.status}
        {...rest}
      >
        {Array.from({ length: depth }, (_, i) => (
          <span key={i} className={styles["guide"]} style={{ left: `calc(var(--ds-space-8) + ${i} * var(--indent) + 7px)` }} aria-hidden />
        ))}
        {hasChildren ? (
          <button
            type="button"
            className={cx(styles["toggle"], open && styles["toggleOpen"])}
            aria-label={open ? "Collapse" : "Expand"}
            tabIndex={-1}
            onClick={(e) => {
              e.stopPropagation();
              setOpen((v) => !v);
            }}
          >
            <Icon name="chevron-right" size={12} className={styles["toggleIcon"]} />
          </button>
        ) : (
          <span className={styles["toggleSpacer"]} />
        )}
        <AgentAvatar role={node.role} size="sm" live={node.status === "running"} />
        <span className={styles["main"]}>
          <span className={styles["title"]}>{node.title ?? ROLE_LABEL[node.role]}</span>
          {node.model ? <span className={cx(styles["sub"], styles["model"])}>{node.model}</span> : null}
          {hasChildren && !open ? <span className={styles["childCount"]}>{node.children?.length}</span> : null}
          {live && node.activity ? <span className={styles["activity"]}>{node.activity}</span> : null}
        </span>
        <StatusBadge status={node.status} size="sm" variant={finished ? "dot" : "badge"} />
        <span className={styles["meta"]}>
          {node.tokens !== undefined ? (
            <span className={styles["metaItem"]}>
              <TokenCount tokens={node.tokens} tone="muted" />
            </span>
          ) : null}
          {node.costUsd !== undefined ? (
            <span className={styles["metaItem"]}>
              <CostDisplay usd={node.costUsd} tone={finished ? "muted" : "secondary"} />
            </span>
          ) : null}
          {node.startedAt !== undefined ? (
            <span className={styles["metaItem"]}>
              <Duration since={node.startedAt} until={node.endedAt} tone="muted" />
            </span>
          ) : null}
        </span>
        <span>{trailing?.(node)}</span>
      </div>
      {hasChildren && open ? (
        <div className={styles["children"]} role="group">
          {node.children?.map((child) => (
            <SessionTreeNode
              key={child.id}
              node={child}
              depth={depth + 1}
              selectedId={selectedId}
              onSelect={onSelect}
              defaultExpanded={defaultExpanded}
              trailing={trailing}
            />
          ))}
        </div>
      ) : null}
    </>
  );
}

export interface SessionTreeProps extends HTMLAttributes<HTMLDivElement> {
  readonly "aria-label"?: string | undefined;
}

/** Container providing the `tree` role. */
export function SessionTree({ className, children, ...rest }: SessionTreeProps) {
  return (
    <div className={cx(styles["tree"], className)} role="tree" {...rest}>
      {children}
    </div>
  );
}

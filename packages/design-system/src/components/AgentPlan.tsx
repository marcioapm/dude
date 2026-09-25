import { useEffect, useMemo, useRef, useState, type HTMLAttributes, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { TODO_SPECS, type TodoStatus } from "../tokens/activity.ts";
import styles from "./AgentPlan.module.css";

export interface PlanItem {
  readonly content: string;
  readonly status: TodoStatus;
  /** Advisory. Only shown when the list mixes priorities. */
  readonly priority?: "high" | "medium" | "low" | string | undefined;
}

export interface AgentPlanProps extends Omit<HTMLAttributes<HTMLDivElement>, "children" | "title"> {
  readonly items: ReadonlyArray<PlanItem>;
  readonly title?: ReactNode;
  /** Shown after the title, e.g. the role or "updated 12s ago". */
  readonly meta?: ReactNode;
  readonly collapsible?: boolean | undefined;
  readonly defaultCollapsed?: boolean | undefined;
  readonly collapsed?: boolean | undefined;
  readonly onCollapsedChange?: ((collapsed: boolean) => void) | undefined;
  /** Stick to the top of the nearest scroll container. */
  readonly sticky?: boolean | undefined;
  /** Flat: no border/background; for use inside a nested thread. */
  readonly flat?: boolean | undefined;
}

/** Progress from a todo list: done / total, plus the current item. */
export function planProgress(items: ReadonlyArray<PlanItem>): { done: number; total: number; current: PlanItem | null } {
  let done = 0;
  let current: PlanItem | null = null;
  for (const it of items) {
    if (TODO_SPECS[it.status].done) done++;
    if (current === null && it.status === "in_progress") current = it;
  }
  return { done, total: items.length, current };
}

/**
 * The agent's running plan, as written by `todowrite`. The agent rewrites
 * the whole list each call, so this renders *the current list* in place
 * rather than one card per call. The header answers "how far along" (3 of
 * 7 and a segmented bar); the rows answer "what is it on". A status change
 * flashes its row once and pops the glyph — the one-shot motion the system
 * already allows for rows that just changed — and under reduced motion the
 * glyph and tone alone carry the change.
 *
 * Only the session being watched pins its plan. A subagent's plan lives
 * inside its own thread (see ChatThread), never as a second pinned panel.
 */
export function AgentPlan({
  items,
  title = "Plan",
  meta,
  collapsible = true,
  defaultCollapsed = false,
  collapsed,
  onCollapsedChange,
  sticky,
  flat,
  className,
  ...rest
}: AgentPlanProps) {
  const [internal, setInternal] = useState(defaultCollapsed);
  const isCollapsed = collapsed ?? internal;
  const { done, total, current } = useMemo(() => planProgress(items), [items]);

  // Remember each item's last status so a change can be animated once.
  const prevRef = useRef<Map<string, TodoStatus>>(new Map());
  const [changed, setChanged] = useState<ReadonlyMap<string, number>>(new Map());
  useEffect(() => {
    const prev = prevRef.current;
    const next = new Map<string, TodoStatus>();
    const bumps = new Map<string, number>();
    const stamp = Date.now();
    for (const it of items) {
      next.set(it.content, it.status);
      const before = prev.get(it.content);
      if (before !== undefined && before !== it.status) bumps.set(it.content, stamp);
    }
    prevRef.current = next;
    if (bumps.size > 0) {
      setChanged((c) => {
        const m = new Map(c);
        for (const [k, v] of bumps) m.set(k, v);
        return m;
      });
      const id = setTimeout(() => setChanged(new Map()), 700);
      return () => clearTimeout(id);
    }
    return undefined;
  }, [items]);

  const mixedPriority = useMemo(() => new Set(items.map((i) => i.priority ?? "")).size > 1, [items]);

  const toggle = () => {
    if (!collapsible) return;
    const next = !isCollapsed;
    if (collapsed === undefined) setInternal(next);
    onCollapsedChange?.(next);
  };

  const allDone = total > 0 && done === total;

  return (
    <div
      className={cx(styles["root"], sticky && styles["sticky"], flat && styles["flat"], isCollapsed && styles["collapsed"], allDone && styles["allDone"], className)}
      data-progress={`${done}/${total}`}
      {...rest}
    >
      <div
        className={styles["header"]}
        role={collapsible ? "button" : undefined}
        tabIndex={collapsible ? 0 : undefined}
        aria-expanded={collapsible ? !isCollapsed : undefined}
        onClick={toggle}
        onKeyDown={(e) => {
          if (collapsible && (e.key === "Enter" || e.key === " ")) {
            e.preventDefault();
            toggle();
          }
        }}
      >
        <span className={styles["headerIcon"]} aria-hidden>
          <Icon name="list-check" size={14} />
        </span>
        <span className={styles["title"]}>{title}</span>
        {meta !== undefined ? <span className={styles["meta"]}>{meta}</span> : null}
        <span className={styles["progress"]}>
          <span className={styles["count"]}>
            {done} of {total}
          </span>
          <span className={styles["bar"]} aria-hidden>
            {items.map((it, i) => (
              <i key={i} data-s={it.status} />
            ))}
          </span>
        </span>
        {isCollapsed && current ? (
          <span className={styles["currentInline"]}>
            <span className={cx(styles["glyph"], styles["glyph-in_progress"])} aria-hidden>
              <Icon name="circle-dotted" size={11} strokeWidth={2} />
            </span>
            <span className={styles["currentText"]}>{current.content}</span>
          </span>
        ) : null}
        {collapsible ? <Icon name="chevron-right" size={14} className={styles["chevron"]} /> : null}
      </div>
      {!isCollapsed ? (
        <ol className={styles["list"]}>
          {items.map((it, i) => {
            const spec = TODO_SPECS[it.status];
            const bump = changed.get(it.content);
            return (
              <li
                key={`${it.content}#${bump ?? 0}`}
                className={cx(styles["item"], styles[`item-${it.status}`], bump !== undefined && styles["itemChanged"])}
                data-status={it.status}
              >
                <span className={cx(styles["glyph"], styles[`glyph-${it.status}`], bump !== undefined && styles["glyphPop"])} aria-hidden>
                  <Icon name={spec.glyph} size={11} strokeWidth={2} />
                </span>
                <span className={styles["ordinal"]} aria-hidden>
                  {i + 1}
                </span>
                <span className={styles["content"]}>{it.content}</span>
                {mixedPriority && it.priority === "high" ? <span className={styles["priority"]}>high</span> : null}
                <span className="ds-sr-only">{spec.label}</span>
              </li>
            );
          })}
        </ol>
      ) : null}
    </div>
  );
}

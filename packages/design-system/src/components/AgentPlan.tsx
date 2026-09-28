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
  /** At the end of the line: "updated 12s ago", or whose plan it is. */
  readonly meta?: ReactNode;
  readonly collapsible?: boolean | undefined;
  readonly defaultCollapsed?: boolean | undefined;
  readonly collapsed?: boolean | undefined;
  readonly onCollapsedChange?: ((collapsed: boolean) => void) | undefined;
  /** Pinned above the conversation: stays in view while the turns scroll. */
  readonly sticky?: boolean | undefined;
  /** Flat: no fill; for a plan inside a nested thread. */
  readonly flat?: boolean | undefined;
}

/** Progress from a todo list: done (cancelled counts as settled) of total, and the item it is on. */
export function planProgress(items: ReadonlyArray<PlanItem>): { done: number; total: number; current: PlanItem | null } {
  let done = 0;
  let current: PlanItem | null = null;
  for (const it of items) {
    if (TODO_SPECS[it.status].done) done++;
    if (current === null && it.status === "in_progress") current = it;
  }
  return { done, total: items.length, current };
}

export interface PlanMeterProps extends Omit<HTMLAttributes<HTMLSpanElement>, "children"> {
  readonly done: number;
  readonly total: number;
  /** Pixel width of the whole meter. */
  readonly width?: number | undefined;
  /** The step after the last done one is being worked on: it breathes. */
  readonly live?: boolean | undefined;
}

/**
 * How far along a plan is, as one cell per step: done, the one being
 * worked on, the rest. Small enough for a card, a pipeline row or a tree
 * row; the words ("3 of 6", the step) sit beside it, not in it.
 */
export function PlanMeter({ done, total, width = 56, live = true, className, style, ...rest }: PlanMeterProps) {
  if (total <= 0) return null;
  return (
    <span
      className={cx(styles["meter"], className)}
      style={{ width, ...style }}
      role="img"
      aria-label={`${done} of ${total} steps done`}
      {...rest}
    >
      {Array.from({ length: total }, (_, i) => (
        <i key={i} data-s={i < done ? "done" : i === done && live ? "current" : "todo"} />
      ))}
    </span>
  );
}

/**
 * The agent's plan, as its latest `todowrite` wrote it, pinned above its
 * conversation. Folded it is one line — progress, a step meter, and the
 * step it is on — so it costs a row and still answers "what is it doing".
 * Open, it is the whole list, the current step highlighted, settled ones
 * struck through. A step that changes flashes once and pops its glyph.
 *
 * Only the session being watched pins its plan; a subagent's lives in its
 * own thread, flat.
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
  const isCollapsed = collapsible && (collapsed ?? internal);
  const { done, total, current } = useMemo(() => planProgress(items), [items]);

  // Each item's last status, so a change is animated once.
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
        <Icon name="list-check" size={14} className={styles["headerIcon"]} />
        <span className={styles["title"]}>{title}</span>
        <span className={styles["count"]}>
          {done} of {total}
        </span>
        <PlanMeter done={done} total={total} width={Math.min(120, Math.max(40, total * 14))} live={!allDone} />
        {current ? (
          <span className={cx(styles["current"], !isCollapsed && styles["currentHidden"])} aria-hidden={!isCollapsed}>
            <Icon name="circle-dotted" size={12} strokeWidth={2} className={styles["currentGlyph"]} />
            <span className={styles["currentText"]}>{current.content}</span>
          </span>
        ) : (
          <span className={styles["spacer"]} />
        )}
        {meta !== undefined ? <span className={styles["meta"]}>{meta}</span> : null}
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
                aria-current={it.status === "in_progress" ? "step" : undefined}
              >
                <span className={cx(styles["glyph"], styles[`glyph-${it.status}`], bump !== undefined && styles["glyphPop"])} aria-hidden>
                  <Icon name={spec.glyph} size={12} strokeWidth={2} />
                </span>
                <span className={styles["content"]}>{it.content}</span>
                {mixedPriority && it.priority === "high" ? <span className={styles["priority"]}>high</span> : <span />}
                <span className="ds-sr-only">
                  {i + 1}. {spec.label}
                </span>
              </li>
            );
          })}
        </ol>
      ) : null}
    </div>
  );
}

import { useCallback, useEffect, useId, useMemo, useRef, useState, type HTMLAttributes, type KeyboardEvent, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { focusedElement, returnFocus } from "../util/focusReturn.ts";
import { Icon } from "../icons/index.tsx";
import { IconButton, type IconButtonProps } from "../primitives/Button.tsx";
import { EmptyState, Skeleton } from "../primitives/Feedback.tsx";
import { ScrollArea } from "../primitives/ScrollArea.tsx";
import { COUNTED_TRIAGE_KINDS, TRIAGE_SPECS, type TriageKind } from "../tokens/triage.ts";
import {
  attentionItems,
  flattenNav,
  ownerOf,
  splitAttention,
  globalCounts,
  navKey,
  type AttentionItem,
  type NavFilter,
  type NavOverrides,
  type NavProject,
  type NavRef,
  type NavRow,
  waitingWords,
} from "../util/navModel.ts";
import { HumanAvatar, HumanAvatarStack, type Person } from "./HumanAvatar.tsx";
import { NavTree, type NavRowMenuControls } from "./NavTree.tsx";
import type { RowMenuItem } from "../primitives/RowMenu.tsx";
import { StatusBadge } from "./StatusBadge.tsx";
import { AgentAvatar } from "./AgentAvatar.tsx";
import styles from "./Sidebar.module.css";

export interface SidebarProps extends Omit<HTMLAttributes<HTMLElement>, "onSelect" | "title"> {
  readonly projects: ReadonlyArray<NavProject>;
  /**
   * The viewer's person id. Only what they can answer is counted and
   * pinned as Needs you; what waits on others is one quiet row
   * (`onShowOthers`). Undefined: every ask is the viewer's.
   */
  readonly you?: string | undefined;
  /** Where "Waiting on others" goes (the full inbox). */
  readonly onShowOthers?: (() => void) | undefined;
  /** Between the header and the search: who is online (`OnlineRow`). */
  readonly presence?: ReactNode;
  readonly selected?: NavRef | null | undefined;
  readonly onSelect?: ((ref: NavRef, node: NavRow["node"]) => void) | undefined;
  /** Header line: the organisation, or the product name. */
  readonly title?: ReactNode;
  /** Right side of the header: a new-task button, settings. */
  readonly headerActions?: ReactNode;
  /** Below the tree: the signed-in person, connection state. */
  readonly footer?: ReactNode;
  /** Data has not arrived yet. Skeleton rows instead of "no projects". */
  readonly loading?: boolean | undefined;
  /** Controlled search text. Uncontrolled when omitted. */
  readonly query?: string | undefined;
  readonly onQueryChange?: ((q: string) => void) | undefined;
  /** Controlled bucket filter. Uncontrolled when omitted. */
  readonly triage?: TriageKind | null | undefined;
  readonly onTriageChange?: ((t: TriageKind | null) => void) | undefined;
  /** Open/closed overrides for the tree; see `NavTree`. */
  readonly expanded?: NavOverrides | undefined;
  readonly onExpandedChange?: ((next: NavOverrides) => void) | undefined;
  /** Hide the pinned "Needs you" section (e.g. a dedicated inbox exists). */
  readonly hideAttention?: boolean | undefined;
  /** Where "and N more" in the "Needs you" section goes (a full inbox). */
  readonly onShowAllAttention?: (() => void) | undefined;
  /** Row "…" menus for the tree; see `NavTree`. */
  readonly menuItems?: ((row: NavRow) => ReadonlyArray<RowMenuItem> | null | undefined) | undefined;
  readonly menu?: ((row: NavRow, controls: NavRowMenuControls) => ReactNode) | undefined;
  readonly width?: number | string | undefined;
  /**
   * Below `SIDEBAR_DRAWER_QUERY` (a viewport under 1000px) the sidebar
   * leaves the layout and becomes an off-canvas drawer over a scrim,
   * shown while `open`. Render a `SidebarToggle` in the main pane to open
   * it. Above the breakpoint `open` is ignored and the sidebar is inline.
   */
  readonly collapsible?: boolean | undefined;
  /** The drawer is showing (narrow screens, with `collapsible`). */
  readonly open?: boolean | undefined;
  /** Escape, a scrim click and choosing a row ask for `false`. */
  readonly onOpenChange?: ((open: boolean) => void) | undefined;
}

/** Where a `collapsible` sidebar becomes a drawer. Kept in step with Sidebar.module.css. */
export const SIDEBAR_DRAWER_QUERY = "(max-width: 999.98px)";

function isDrawerViewport(): boolean {
  return typeof window !== "undefined" && typeof window.matchMedia === "function" && window.matchMedia(SIDEBAR_DRAWER_QUERY).matches;
}

/**
 * The persistent navigation next to the transcript. Top to bottom:
 *
 *   header      who / where, plus actions
 *   search      `/` from anywhere in the tree; ↓ moves into the tree
 *   chips       needs you · active · ready · failed — global counts, each
 *               a filter; the needs-you chip is the only loud one
 *   needs you   pinned: every blocked task across all projects, with
 *               who is asking and who it waits on. Findable without
 *               expanding anything. Absent when nothing is blocked.
 *   tree        Project → Epic → Task → Session
 *   footer      the signed-in person, connection state
 *
 * Calm at fifty tasks: colour appears only on the status marks, and
 * the one amber area is the needs-you block.
 */
export function Sidebar({
  projects,
  you,
  onShowOthers,
  presence,
  selected,
  onSelect,
  title,
  headerActions,
  footer,
  loading,
  query,
  onQueryChange,
  triage,
  onTriageChange,
  expanded,
  onExpandedChange,
  hideAttention,
  onShowAllAttention,
  menuItems,
  menu,
  width = 304,
  collapsible,
  open,
  onOpenChange,
  className,
  style,
  ...rest
}: SidebarProps) {
  const [localQuery, setLocalQuery] = useState("");
  const q = query ?? localQuery;
  const setQuery = useCallback(
    (v: string) => {
      onQueryChange?.(v);
      if (query === undefined) setLocalQuery(v);
    },
    [onQueryChange, query],
  );
  const [localTriage, setLocalTriage] = useState<TriageKind | null>(null);
  const t = triage === undefined ? localTriage : triage;
  const setTriage = useCallback(
    (v: TriageKind | null) => {
      onTriageChange?.(v);
      if (triage === undefined) setLocalTriage(v);
    },
    [onTriageChange, triage],
  );

  const filter = useMemo<NavFilter>(() => ({ query: q, triage: t, you }), [q, t, you]);
  const counts = useMemo(() => globalCounts(projects, you), [projects, you]);
  const { yours: attention, others } = useMemo(() => splitAttention(attentionItems(projects, you)), [projects, you]);
  const filtering = q.trim().length > 0 || t !== null;
  const visible = useMemo(() => (filtering ? flattenNav(projects, expanded ?? new Map(), filter).length : -1), [filtering, projects, expanded, filter]);

  const searchRef = useRef<HTMLInputElement>(null);
  const treeRef = useRef<HTMLDivElement>(null);
  const searchId = useId();
  const focusSearch = useCallback(() => searchRef.current?.focus(), []);
  const onSearchKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      treeRef.current?.querySelector<HTMLElement>('[role="treeitem"][tabindex="0"]')?.focus();
    } else if (e.key === "Escape" && q) {
      e.preventDefault();
      setQuery("");
    }
  };

  const clear = () => {
    setQuery("");
    setTriage(null);
  };

  // The drawer: focus moves into it on open and back to what opened it
  // (the toggle) on close; Escape closes it. Nothing is trapped.
  const drawerOpen = !!collapsible && !!open;
  // The handler is read through a ref so an inline one does not re-run the
  // effect (which would bounce focus to the toggle and back).
  const onOpenChangeRef = useRef(onOpenChange);
  onOpenChangeRef.current = onOpenChange;
  useEffect(() => {
    if (!drawerOpen || !isDrawerViewport()) return;
    const opener = focusedElement();
    searchRef.current?.focus({ preventScroll: true });
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === "Escape" && !e.defaultPrevented) onOpenChangeRef.current?.(false);
    };
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("keydown", onKey);
      returnFocus(opener);
    };
  }, [drawerOpen]);
  const select = useCallback(
    (ref: NavRef, node: NavRow["node"]) => {
      onSelect?.(ref, node);
      if (drawerOpen) onOpenChange?.(false);
    },
    [onSelect, drawerOpen, onOpenChange],
  );

  const aside = (
    <aside
      className={cx(styles["root"], collapsible && styles["collapsible"], drawerOpen && styles["open"], className)}
      style={{ width, ...style }}
      aria-label="Navigation"
      data-open={collapsible ? String(!!open) : undefined}
      {...rest}
    >
      {title !== undefined || headerActions !== undefined ? (
        <header className={styles["header"]}>
          <span className={styles["title"]}>{title}</span>
          {headerActions ? <span className={styles["headerActions"]}>{headerActions}</span> : null}
        </header>
      ) : null}

      {presence}

      <div className={styles["search"]}>
        <Icon name="search" size={14} className={styles["searchIcon"]} />
        <input
          ref={searchRef}
          id={searchId}
          className={styles["searchInput"]}
          type="search"
          value={q}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={onSearchKey}
          placeholder="Find work…"
          aria-label="Find tasks, people, epics"
          autoComplete="off"
          spellCheck={false}
        />
        {q ? (
          <button type="button" className={styles["searchClear"]} aria-label="Clear search" onClick={() => setQuery("")}>
            <Icon name="close" size={10} />
          </button>
        ) : (
          <kbd className={styles["searchKbd"]} aria-hidden>
            /
          </kbd>
        )}
      </div>

      <div className={styles["chips"]} role="group" aria-label="Filter by state">
        {COUNTED_TRIAGE_KINDS.map((k) => {
          const spec = TRIAGE_SPECS[k];
          const n = counts[k];
          const on = t === k;
          return (
            <button
              key={k}
              type="button"
              className={cx(styles["chip"], styles[`chip-${k}`], on && styles["chipOn"], n === 0 && styles["chipZero"])}
              aria-pressed={on}
              title={spec.description}
              onClick={() => setTriage(on ? null : k)}
              disabled={n === 0 && !on}
            >
              <StatusBadge status={spec.status} variant="dot" iconOnly className={styles["chipMark"]} />
              <span className={cx(styles["chipLabel"], "ds-cap")}>{spec.label}</span>
              <span className={cx(styles["chipCount"], "ds-cap")}>{n}</span>
            </button>
          );
        })}
      </div>

      {!hideAttention && !loading && attention.length > 0 && !filtering ? <AttentionList items={attention} selected={selected} onSelect={select} onShowAll={onShowAllAttention} /> : null}
      {!hideAttention && !loading && others.length > 0 && !filtering ? (
        <button type="button" className={styles["othersRow"]} onClick={onShowOthers} disabled={!onShowOthers} data-testid="waiting-on-others">
          <span className={styles["othersTitle"]}>Waiting on others</span>
          <HumanAvatarStack people={others.map((it) => ownerOf(it.task)).filter((p): p is Person => p !== null)} size="xs" max={3} aria-hidden />
          <span className={styles["othersCount"]}>{others.length}</span>
        </button>
      ) : null}

      <ScrollArea fill className={styles["scroll"]}>
        {loading ? (
          <TreeSkeleton />
        ) : projects.length === 0 ? (
          <EmptyState compact icon="folder" title="No projects yet" description="Add a project to start giving agents work." className={styles["empty"]} />
        ) : visible === 0 ? (
          <EmptyState
            compact
            icon="search"
            title="No matches"
            description={t ? `Nothing is ${TRIAGE_SPECS[t].label.toLowerCase()}${q ? ` matching “${q}”` : ""}.` : `Nothing matches “${q}”.`}
            action={
              <button type="button" className={styles["link"]} onClick={clear}>
                Clear
              </button>
            }
            className={styles["empty"]}
          />
        ) : (
          <NavTree ref={treeRef} projects={projects} selected={selected} onSelect={select} expanded={expanded} onExpandedChange={onExpandedChange} filter={filter} onSearchRequest={focusSearch} menuItems={menuItems} menu={menu} />
        )}
      </ScrollArea>

      {footer ? <footer className={styles["footer"]}>{footer}</footer> : null}
    </aside>
  );
  if (!collapsible) return aside;
  return (
    <>
      {aside}
      <div className={cx(styles["scrim"], drawerOpen && styles["scrimOn"])} aria-hidden onClick={() => onOpenChange?.(false)} />
    </>
  );
}

export interface SidebarToggleProps extends Omit<IconButtonProps, "icon" | "label" | "onClick"> {
  readonly open: boolean;
  readonly onOpenChange: (open: boolean) => void;
  /** The sidebar's `id`, for `aria-controls`. */
  readonly controls?: string | undefined;
  readonly label?: string | undefined;
}

/**
 * The menu button that opens a `collapsible` sidebar's drawer. Put it at
 * the start of the main pane's top bar; it hides itself when the viewport
 * is wide enough for the sidebar to be inline.
 */
export function SidebarToggle({ open, onOpenChange, controls, label = "Navigation", className, ...rest }: SidebarToggleProps) {
  return (
    <IconButton
      icon="menu"
      label={label}
      aria-expanded={open}
      aria-controls={controls}
      className={cx(styles["toggle"], className)}
      onClick={() => onOpenChange(!open)}
      {...rest}
    />
  );
}

// ---------------------------------------------------------------------------

export interface AttentionListProps {
  readonly items: ReadonlyArray<AttentionItem>;
  readonly selected?: NavRef | null | undefined;
  readonly onSelect?: ((ref: NavRef, node: NavRow["node"]) => void) | undefined;
  /** Rows shown before "and N more". Infinity: all of them. */
  readonly max?: number | undefined;
  /** Makes "and N more" a button to where all of them are listed. */
  readonly onShowAll?: (() => void) | undefined;
  /** A heading of its own (a full inbox), not the sidebar's collapsible one. */
  readonly title?: string | undefined;
  /**
   * Asks that wait on someone else: calm (no attention wash — only yours
   * are loud), the owner's face leads the row, and each offers Take over.
   */
  readonly others?: boolean | undefined;
  /** "Take over": make the task yours, so its asks are yours to answer. */
  readonly onTakeOver?: ((item: AttentionItem) => void) | undefined;
}

/**
 * The pinned "Needs you" section: one row per blocked task, across
 * every project, in the order given (the caller sorts — oldest wait first
 * is the sensible default). Each row says what, who is asking, what they
 * ask and who it waits on; where it lives (project · epic) is the row's
 * tooltip, so the ask gets the width. It is a list, not a tree. With
 * `others`, the same list for what waits on someone else, quietly.
 */
export function AttentionList({ items, selected, onSelect, max = 5, onShowAll, title, others, onTakeOver }: AttentionListProps) {
  const [open, setOpen] = useState(true);
  const shown = open ? items.slice(0, max) : [];
  const more = items.length - shown.length;
  const selectedKey = selected ? navKey(selected) : null;
  return (
    <section className={cx(styles["attention"], others && styles["attentionOthers"])} aria-label={title ?? "Needs you"}>
      {title === undefined ? (
        <button type="button" className={styles["attentionHead"]} aria-expanded={open} onClick={() => setOpen((v) => !v)}>
          <Icon name="chevron-right" size={14} className={cx(styles["attentionChevron"], open && styles["attentionChevronOpen"])} />
          <StatusBadge status="awaiting_input" variant="dot" iconOnly className={styles["attentionMark"]} />
          <span className={styles["attentionTitle"]}>Needs you</span>
          <span className={styles["attentionCount"]}>{items.length}</span>
        </button>
      ) : (
        <h2 className={cx(styles["attentionHead"], styles["attentionHeadStatic"])}>
          {others ? null : <StatusBadge status="awaiting_input" variant="dot" iconOnly className={styles["attentionMark"]} />}
          <span className={styles["attentionTitle"]}>{title}</span>
          <span className={styles["attentionCount"]}>{items.length}</span>
        </h2>
      )}
      {open ? (
        <ul className={styles["attentionList"]}>
          {shown.map((it) => {
            const ref: NavRef = it.session ? { kind: "session", id: it.session.id } : { kind: "task", id: it.task.id };
            const isSel = navKey(ref) === selectedKey || navKey({ kind: "task", id: it.task.id }) === selectedKey;
            const where = it.epic ? `${it.project.name} · ${it.epic.title}` : it.project.name;
            const people = it.task.people ?? [];
            const names = people.map((p) => p.name).join(", ");
            const owner = ownerOf(it.task);
            return (
              <li key={it.task.id} className={styles["attentionItem"]}>
                <button
                  type="button"
                  className={cx(styles["attentionRow"], isSel && styles["attentionRowSelected"])}
                  onClick={() => onSelect?.(ref, it.session ?? it.task)}
                  aria-current={isSel ? "true" : undefined}
                  title={where}
                >
                  <span className={styles["attentionMain"]}>
                    <span className={styles["attentionWi"]}>
                      {it.task.key ? <span className={styles["attentionKey"]}>{it.task.key}</span> : null}
                      <span className={styles["attentionWiTitle"]} title={it.task.title}>
                        {it.task.title}
                      </span>
                    </span>
                    <span className={styles["attentionSub"]}>
                      {/* The asker's slot is kept when there is no session, so every ask starts on one x. */}
                      <span className={styles["attentionAsker"]}>{it.session ? <AgentAvatar role={it.session.role} size="xs" /> : null}</span>
                      {it.session ? (
                        <span className={styles["attentionAsk"]} title={it.session.activity}>
                          {it.session.activity ?? (others && owner ? `is waiting for ${owner.name}` : "is waiting for you")}
                        </span>
                      ) : (
                        <span className={styles["attentionAsk"]} title={waitingWords(it.task)}>
                          {others && owner ? `waiting for ${owner.name}` : waitingWords(it.task)}
                        </span>
                      )}
                    </span>
                  </span>
                  {people.length > 0 ? (
                    <span className={styles["attentionPeople"]} role="group" aria-label={names} title={names}>
                      <HumanAvatar person={people[0]!} size={others ? "md" : "xs"} aria-hidden />
                      {people.length > 1 ? <span className={styles["attentionPeopleMore"]}>+{people.length - 1}</span> : null}
                    </span>
                  ) : null}
                </button>
                {onTakeOver ? (
                  <button type="button" className={styles["takeOver"]} onClick={() => onTakeOver(it)} data-testid="take-over">
                    Take over
                  </button>
                ) : null}
              </li>
            );
          })}
          {more > 0 ? (
            <li className={styles["attentionMore"]}>
              {onShowAll ? (
                <button type="button" className={styles["attentionMoreButton"]} onClick={onShowAll} data-testid="attention-show-all">
                  and {more} more — see all
                </button>
              ) : (
                <>and {more} more — filter by Needs you to see all</>
              )}
            </li>
          ) : null}
        </ul>
      ) : null}
    </section>
  );
}

function TreeSkeleton() {
  const widths = [60, 120, 150, 110, 140, 90, 130, 100, 145, 80];
  return (
    <div className={styles["skeleton"]} aria-busy="true" aria-label="Loading projects">
      {widths.map((w, i) => (
        <div key={i} className={styles["skeletonRow"]} style={{ paddingLeft: i === 0 || i === 5 ? 8 : i % 3 === 0 ? 20 : 32 }}>
          <Skeleton variant="circle" width={8} height={8} />
          <Skeleton variant="text" width={w} />
        </div>
      ))}
    </div>
  );
}

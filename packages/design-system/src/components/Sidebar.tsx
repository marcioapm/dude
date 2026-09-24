import { useCallback, useId, useMemo, useRef, useState, type HTMLAttributes, type KeyboardEvent, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { EmptyState, Skeleton } from "../primitives/Feedback.tsx";
import { ScrollArea } from "../primitives/ScrollArea.tsx";
import { COUNTED_TRIAGE_KINDS, TRIAGE_SPECS, type TriageKind } from "../tokens/triage.ts";
import {
  attentionItems,
  flattenNav,
  globalCounts,
  navKey,
  type AttentionItem,
  type NavFilter,
  type NavOverrides,
  type NavProject,
  type NavRef,
  type NavRow,
} from "../util/navModel.ts";
import { HumanAvatarStack } from "./HumanAvatar.tsx";
import { NavTree, type NavRowMenuControls } from "./NavTree.tsx";
import type { RowMenuItem } from "../primitives/RowMenu.tsx";
import { StatusBadge } from "./StatusBadge.tsx";
import { AgentAvatar } from "./AgentAvatar.tsx";
import styles from "./Sidebar.module.css";

export interface SidebarProps extends Omit<HTMLAttributes<HTMLElement>, "onSelect" | "title"> {
  readonly projects: ReadonlyArray<NavProject>;
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
  /** Row "…" menus for the tree; see `NavTree`. */
  readonly menuItems?: ((row: NavRow) => ReadonlyArray<RowMenuItem> | null | undefined) | undefined;
  readonly menu?: ((row: NavRow, controls: NavRowMenuControls) => ReactNode) | undefined;
  readonly width?: number | string | undefined;
}

/**
 * The persistent navigation next to the transcript. Top to bottom:
 *
 *   header      who / where, plus actions
 *   search      `/` from anywhere in the tree; ↓ moves into the tree
 *   chips       needs you · active · ready · failed — global counts, each
 *               a filter; the needs-you chip is the only loud one
 *   needs you   pinned: every blocked work item across all projects, with
 *               who is asking and who it waits on. Findable without
 *               expanding anything. Absent when nothing is blocked.
 *   tree        Project → Epic → Work item → Session
 *   footer      the signed-in person, connection state
 *
 * Calm at fifty work items: rows are 24/28px, colour appears only on the
 * status marks, and the only wash on the surface is the attention tint on
 * rows that need a person.
 */
export function Sidebar({
  projects,
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
  menuItems,
  menu,
  width = 304,
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

  const filter = useMemo<NavFilter>(() => ({ query: q, triage: t }), [q, t]);
  const counts = useMemo(() => globalCounts(projects), [projects]);
  const attention = useMemo(() => attentionItems(projects), [projects]);
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

  return (
    <aside className={cx(styles["root"], className)} style={{ width, ...style }} aria-label="Navigation" {...rest}>
      {title !== undefined || headerActions !== undefined ? (
        <header className={styles["header"]}>
          <span className={styles["title"]}>{title}</span>
          {headerActions ? <span className={styles["headerActions"]}>{headerActions}</span> : null}
        </header>
      ) : null}

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
          aria-label="Find work items, people, epics"
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

      {!hideAttention && !loading && attention.length > 0 && !filtering ? <AttentionList items={attention} selected={selected} onSelect={onSelect} /> : null}

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
          <NavTree ref={treeRef} projects={projects} selected={selected} onSelect={onSelect} expanded={expanded} onExpandedChange={onExpandedChange} filter={filter} onSearchRequest={focusSearch} menuItems={menuItems} menu={menu} />
        )}
      </ScrollArea>

      {footer ? <footer className={styles["footer"]}>{footer}</footer> : null}
    </aside>
  );
}

// ---------------------------------------------------------------------------

export interface AttentionListProps {
  readonly items: ReadonlyArray<AttentionItem>;
  readonly selected?: NavRef | null | undefined;
  readonly onSelect?: ((ref: NavRef, node: NavRow["node"]) => void) | undefined;
  /** Rows shown before "and N more". */
  readonly max?: number | undefined;
}

/**
 * The pinned "Needs you" section: one row per blocked work item, across
 * every project, in the order given (the caller sorts — oldest wait first
 * is the sensible default). Each row says what, where, who is asking and
 * who it waits on. It is a list, not a tree: nothing to expand.
 */
export function AttentionList({ items, selected, onSelect, max = 5 }: AttentionListProps) {
  const [open, setOpen] = useState(true);
  const shown = open ? items.slice(0, max) : [];
  const more = items.length - shown.length;
  const selectedKey = selected ? navKey(selected) : null;
  return (
    <section className={styles["attention"]} aria-label="Needs you">
      <button type="button" className={styles["attentionHead"]} aria-expanded={open} onClick={() => setOpen((v) => !v)}>
        <Icon name="chevron-right" size={14} className={cx(styles["attentionChevron"], open && styles["attentionChevronOpen"])} />
        <StatusBadge status="awaiting_input" variant="dot" iconOnly className={styles["attentionMark"]} />
        <span className={styles["attentionTitle"]}>Needs you</span>
        <span className={styles["attentionCount"]}>{items.length}</span>
      </button>
      {open ? (
        <ul className={styles["attentionList"]}>
          {shown.map((it) => {
            const ref: NavRef = it.session ? { kind: "session", id: it.session.id } : { kind: "workItem", id: it.workItem.id };
            const isSel = navKey(ref) === selectedKey || navKey({ kind: "workItem", id: it.workItem.id }) === selectedKey;
            const where = it.epic ? `${it.project.name} · ${it.epic.title}` : it.project.name;
            return (
              <li key={it.workItem.id}>
                <button type="button" className={cx(styles["attentionRow"], isSel && styles["attentionRowSelected"])} onClick={() => onSelect?.(ref, it.session ?? it.workItem)} aria-current={isSel ? "true" : undefined}>
                  <span className={styles["attentionMain"]}>
                    <span className={styles["attentionWi"]}>
                      {it.workItem.key ? <span className={styles["attentionKey"]}>{it.workItem.key}</span> : null}
                      <span className={styles["attentionWiTitle"]} title={it.workItem.title}>
                        {it.workItem.title}
                      </span>
                    </span>
                    <span className={styles["attentionSub"]}>
                      {it.session ? (
                        <>
                          <AgentAvatar role={it.session.role} size="xs" className={styles["attentionAsker"]} />
                          <span className={styles["attentionAsk"]} title={it.session.activity}>
                            {it.session.activity ?? "is waiting for you"}
                          </span>
                        </>
                      ) : (
                        <span className={styles["attentionAsk"]}>{it.workItem.status === "awaiting_confirmation" ? "plan needs your confirmation" : "waiting for you"}</span>
                      )}
                      <span className={styles["attentionWhere"]} title={where}>
                        {where}
                      </span>
                    </span>
                  </span>
                  {it.workItem.people && it.workItem.people.length > 0 ? <HumanAvatarStack people={it.workItem.people} size="xs" max={2} /> : null}
                </button>
              </li>
            );
          })}
          {more > 0 ? <li className={styles["attentionMore"]}>and {more} more — filter by Needs you to see all</li> : null}
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

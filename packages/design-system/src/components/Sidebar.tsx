import { useCallback, useEffect, useId, useMemo, useRef, useState, type ButtonHTMLAttributes, type HTMLAttributes, type KeyboardEvent, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { focusedElement, returnFocus } from "../util/focusReturn.ts";
import { isBareKey } from "../util/keys.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { IconButton, type IconButtonProps } from "../primitives/Button.tsx";
import { EmptyState, Skeleton } from "../primitives/Feedback.tsx";
import { ScrollArea } from "../primitives/ScrollArea.tsx";
import { Tooltip } from "../primitives/Tooltip.tsx";
import { ProjectAvatar } from "./ProjectAvatar.tsx";
import {
  attentionItems,
  flattenNav,
  navKey,
  projectCounts,
  waitingSplit,
  type NavFilter,
  type NavOverrides,
  type NavProject,
  type NavRef,
  type NavRow,
  waitingWords,
} from "../util/navModel.ts";
import { PersonAvatar, PersonAvatarStack, type Person } from "./PersonAvatar.tsx";
import { NeedsYouCount } from "./StatusMark.tsx";
import { Segmented } from "./ScreenHeader.tsx";
import { NavTree, type NavRowMenuControls } from "./NavTree.tsx";
import type { RowMenuItem } from "../primitives/RowMenu.tsx";
import type { TriageCounts } from "../tokens/triage.ts";
import styles from "./Sidebar.module.css";

export interface SidebarProps extends Omit<HTMLAttributes<HTMLElement>, "onSelect" | "title"> {
  readonly projects: ReadonlyArray<NavProject>;
  readonly selected?: NavRef | null | undefined;
  readonly onSelect?: ((ref: NavRef, node: NavRow["node"]) => void) | undefined;
  /** The brand at the top: dude, and the organisation. */
  readonly title?: ReactNode;
  /** Right side of the brand line. */
  readonly headerActions?: ReactNode;
  /** The profile band at the bottom, on its own shade: organisation settings, you. */
  readonly footer?: ReactNode;
  /** Data has not arrived yet. Skeleton rows instead of "no projects". */
  readonly loading?: boolean | undefined;
  /** Controlled search text. Uncontrolled when omitted. */
  readonly query?: string | undefined;
  readonly onQueryChange?: ((q: string) => void) | undefined;
  /** Open/closed overrides for the tree; see `NavTree`. */
  readonly expanded?: NavOverrides | undefined;
  readonly onExpandedChange?: ((next: NavOverrides) => void) | undefined;
  /**
   * The signed-in person's id: what waits on them is theirs, the rest is
   * others', and "Mine" filters the tree to their tasks. Without it, all
   * of it is yours and there is no Mine.
   */
  readonly you?: string | null | undefined;
  /** Who is online, when the app knows: faces under the brand. */
  readonly online?: ReadonlyArray<Person> | undefined;
  /** Open "Waiting on you" (or on others): a full inbox. Without it the rows are not drawn. */
  readonly onWaitingSelect?: ((whose: "you" | "others") => void) | undefined;
  /** The inbox is what is open: its row is current. */
  readonly waitingSelected?: boolean | undefined;
  /** Waiting on you beyond the tree's tasks: session invitations and questions put to you. */
  readonly waitingExtra?: number | undefined;
  /** Controlled "Mine" (the tree shows only your tasks). Uncontrolled when omitted. */
  readonly mine?: boolean | undefined;
  readonly onMineChange?: ((mine: boolean) => void) | undefined;
  /** Beside the Projects label: a new-project button. */
  readonly treeActions?: ReactNode;
  /** Above Projects: your brainstorm sessions (`SidebarSessions`). */
  readonly sessions?: ReactNode;
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
  /**
   * Folded to a 56px rail of faces and glyphs (`SIDEBAR_DRAWER_QUERY` not
   * matching: 1000px and up; narrower, the drawer works as without it).
   */
  readonly collapsed?: boolean | undefined;
  /** Set, the header has a collapse chevron and the rail an expand one. */
  readonly onCollapsedChange?: ((collapsed: boolean) => void) | undefined;
  /** The rail's first item: the brand's face (the `title`'s, without its words). */
  readonly railMark?: ReactNode;
  /** Pressing the rail's face: home. */
  readonly onHome?: (() => void) | undefined;
  /** Home is what is open: the rail's face is current. */
  readonly homeSelected?: boolean | undefined;
  /** The rail's New session and Sessions, from what the `sessions` slot does. */
  readonly railSessions?: SidebarRailSessions | undefined;
  /** The band's rail form: `SidebarRailItem`s (settings, your face). */
  readonly railFooter?: ReactNode;
}

/** What the rail needs of the `sessions` slot: two places, and the names for the Sessions tooltip. */
export interface SidebarRailSessions {
  readonly onNew: () => void;
  readonly onOpenList: () => void;
  /** The list (or one of them) is what is open. */
  readonly current?: boolean | undefined;
  /** Your latest few, named in the Sessions tooltip. */
  readonly recent?: ReadonlyArray<string> | undefined;
}

/** Where a `collapsible` sidebar becomes a drawer. Kept in step with Sidebar.module.css. */
export const SIDEBAR_DRAWER_QUERY = "(max-width: 999.98px)";

function isDrawerViewport(): boolean {
  return typeof window !== "undefined" && typeof window.matchMedia === "function" && window.matchMedia(SIDEBAR_DRAWER_QUERY).matches;
}

/** Whether the viewport is under the drawer breakpoint, followed as it changes. */
function useDrawerViewport(): boolean {
  const [narrow, setNarrow] = useState(isDrawerViewport);
  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") return;
    const query = window.matchMedia(SIDEBAR_DRAWER_QUERY);
    const follow = () => setNarrow(query.matches);
    follow();
    query.addEventListener("change", follow);
    return () => query.removeEventListener("change", follow);
  }, []);
  return narrow;
}

/** A project's counts in words, the loud one first and nothing that is zero: "1 needs you · 4 running". */
export function projectCountWords(c: TriageCounts): string {
  return [
    c.needs_you ? `${c.needs_you} ${c.needs_you === 1 ? "needs" : "need"} you` : null,
    c.active ? `${c.active} running` : null,
    c.failed ? `${c.failed} failed` : null,
    c.ready ? `${c.ready} ready` : null,
  ].filter(Boolean).join(" · ");
}

/**
 * The persistent navigation beside everything else. Top to bottom:
 *
 *   brand       dude, and the organisation
 *   online      who is here now, as faces (when the app knows)
 *   search      `/` from anywhere in the tree; ↓ moves into the tree
 *   waiting     "Waiting on you" with the one loud count, and "Waiting on
 *               others" — what needs a person, answerable without
 *               expanding anything
 *   projects    Everyone / Mine, then the tree: projects with their faces,
 *               epics, tasks with their people, and under a task only the
 *               agents working now
 *   band        on its own shade: organisation settings, and you
 *
 * Calm at fifty tasks: colour is on faces and marks, and the one amber
 * thing is the count of what waits on you.
 *
 * `collapsed` (1000px and up) folds it to a 56px rail of the same rows'
 * faces and glyphs, each named in a tooltip: home, expand, search, waiting,
 * New session and Sessions, each project's face (the needs-you diamond on
 * one that waits on you), then the band. The tree is not in it.
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
  expanded,
  onExpandedChange,
  you,
  online,
  onWaitingSelect,
  waitingSelected,
  waitingExtra = 0,
  mine,
  onMineChange,
  treeActions,
  sessions,
  menuItems,
  menu,
  width = 300,
  collapsible,
  open,
  onOpenChange,
  collapsed,
  onCollapsedChange,
  railMark,
  onHome,
  homeSelected,
  railSessions,
  railFooter,
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
  const [localMine, setLocalMine] = useState(false);
  const isMine = Boolean(you) && (mine ?? localMine);
  const setMine = (v: boolean) => {
    onMineChange?.(v);
    if (mine === undefined) setLocalMine(v);
  };

  const filter = useMemo<NavFilter>(() => ({ query: q, triage: null, person: isMine ? (you ?? null) : null, you }), [q, isMine, you]);
  const waiting = useMemo(() => waitingSplit(attentionItems(projects), you), [projects, you]);
  const filtering = q.trim().length > 0 || isMine;
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
    if (isMine) setMine(false);
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
  const openWaiting = (whose: "you" | "others") => {
    onWaitingSelect?.(whose);
    if (drawerOpen) onOpenChange?.(false);
  };

  // The rail is a wide screen's: under the breakpoint `collapsed` is ignored.
  const narrow = useDrawerViewport();
  const rail = Boolean(collapsed && onCollapsedChange && !narrow);
  // The rail's search expands the sidebar, then the field takes the focus.
  const searchOnExpand = useRef(false);
  useEffect(() => {
    if (rail || !searchOnExpand.current) return;
    searchOnExpand.current = false;
    searchRef.current?.focus();
  }, [rail]);
  // `/` outside a field does what the rail's search item does.
  useEffect(() => {
    if (!rail || !onCollapsedChange) return;
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (!isBareKey(e, "/")) return;
      e.preventDefault();
      searchOnExpand.current = true;
      onCollapsedChange(false);
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [rail, onCollapsedChange]);
  const yoursWaiting = waiting.yours.length + waitingExtra;
  // Each project's counts walk its whole subtree: once per tree, not per render.
  const railCounts = useMemo(() => (rail ? new Map(projects.map((p) => [p.id, projectCounts(p, you)])) : null), [rail, projects, you]);

  if (rail) {
    return (
      <nav className={cx(styles["rail"], className)} style={style} aria-label="Navigation" data-testid="sidebar-rail" {...rest}>
        <div className={styles["railGroup"]}>
          {railMark ? (
            <SidebarRailItem label="Home" current={homeSelected} onClick={onHome} data-testid="rail-home">{railMark}</SidebarRailItem>
          ) : null}
          <SidebarRailItem label="Expand sidebar" shortcut="[" onClick={() => onCollapsedChange?.(false)} data-testid="rail-expand">
            <Icon name="chevron-right" size={16} />
          </SidebarRailItem>
        </div>
        <div className={styles["railGroup"]}>
          <SidebarRailItem label="Find work" shortcut="/" data-testid="rail-search" onClick={() => {
            searchOnExpand.current = true;
            onCollapsedChange?.(false);
          }}>
            <Icon name="search" size={16} />
          </SidebarRailItem>
          {onWaitingSelect ? (
            <SidebarRailItem label={yoursWaiting ? `Waiting on you: ${yoursWaiting}` : "Waiting on you"}
              tip={yoursWaiting ? `Waiting on you · ${yoursWaiting}` : "Nothing waiting on you"}
              current={waitingSelected} onClick={() => onWaitingSelect("you")} data-testid="rail-waiting">
              {/* With something waiting the count is the item: its diamond is the needs-you shape. */}
              {yoursWaiting ? <NeedsYouCount count={yoursWaiting} /> : <Icon name="inbox" size={16} />}
            </SidebarRailItem>
          ) : null}
          {railSessions ? (
            <>
              <SidebarRailItem label="New session" onClick={railSessions.onNew} data-testid="rail-new-session">
                <Icon name="plus" size={16} />
              </SidebarRailItem>
              <SidebarRailItem label="Sessions" current={railSessions.current} onClick={railSessions.onOpenList} data-testid="rail-sessions"
                tip={railSessions.recent?.length ? (
                  <span className={styles["railTip"]}><b>Sessions</b>{railSessions.recent.slice(0, 4).map((t, i) => <span key={i} className={styles["railTipMuted"]}>{t}</span>)}</span>
                ) : undefined}>
                <Icon name="brainstorm" size={16} />
              </SidebarRailItem>
            </>
          ) : null}
        </div>
        <div className={styles["railProjects"]} role="group" aria-label="Projects">
          {projects.map((p) => {
            const counts = railCounts!.get(p.id)!;
            const words = projectCountWords(counts);
            const waits = counts.needs_you > 0;
            return (
              <SidebarRailItem key={p.id} label={words ? `${p.name}: ${words}` : p.name}
                tip={<span className={styles["railTip"]}><b>{p.name}</b>{words ? <span className={styles["railTipMuted"]}>{words}</span> : null}</span>}
                current={selected?.kind === "project" && selected.id === p.id} onClick={() => onSelect?.({ kind: "project", id: p.id }, p)}
                data-testid="rail-project" data-project={p.id} data-needs-you={waits || undefined}>
                <ProjectAvatar project={{ id: p.id, name: p.name, imageUrl: p.imageUrl, colorSlot: p.colorSlot }} size={24} />
                {waits ? <span className={styles["railMark"]} aria-hidden /> : null}
              </SidebarRailItem>
            );
          })}
        </div>
        {railFooter ? <div className={styles["railBand"]}>{railFooter}</div> : null}
      </nav>
    );
  }

  const aside = (
    <aside
      className={cx(styles["root"], collapsible && styles["collapsible"], drawerOpen && styles["open"], className)}
      style={{ width, ...style }}
      aria-label="Navigation"
      data-open={collapsible ? String(!!open) : undefined}
      {...rest}
    >
      {title !== undefined || headerActions !== undefined || onCollapsedChange ? (
        <header className={styles["header"]}>
          <span className={styles["title"]}>{title}</span>
          {headerActions ? <span className={styles["headerActions"]}>{headerActions}</span> : null}
          {onCollapsedChange ? (
            <Tooltip content="Collapse sidebar" shortcut="[" side="right">
              <IconButton icon="chevron-left" label="Collapse sidebar" size="sm" className={styles["collapse"]}
                onClick={() => onCollapsedChange(true)} data-testid="sidebar-collapse" />
            </Tooltip>
          ) : null}
        </header>
      ) : null}

      {online && online.length > 0 ? (
        <div className={styles["online"]} role="group" aria-label={`Online: ${online.map((p) => p.name).join(", ")}`} data-testid="online">
          <span className={cx(styles["label"], "ds-label")} aria-hidden>Online</span>
          <PersonAvatarStack people={online} size={24} max={6} />
          <span className={styles["onlineCount"]}>{online.length}</span>
        </div>
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

      {onWaitingSelect ? (
        <nav className={styles["waiting"]} aria-label="Waiting">
          <SidebarLink
            current={waitingSelected}
            onClick={() => openWaiting("you")}
            leading={waiting.yours.length + waitingExtra > 0 ? <NeedsYouCount count={waiting.yours.length + waitingExtra} /> : undefined}
            data-testid="waiting-on-you"
          >
            Waiting on you
          </SidebarLink>
          {waiting.others.length > 0 ? (
            <SidebarLink onClick={() => openWaiting("others")} count={waiting.others.length} data-testid="waiting-on-others">
              Waiting on others
            </SidebarLink>
          ) : null}
        </nav>
      ) : null}

      {sessions}

      <div className={styles["treeHead"]}>
        <span className="ds-label">Projects</span>
        <span className={styles["spacer"]} />
        {you ? (
          <Segmented label="Whose tasks" size="sm" value={isMine ? "mine" : "everyone"} onChange={(v) => setMine(v === "mine")}
            options={[{ value: "everyone", label: "Everyone" }, { value: "mine", label: "Mine" }]} />
        ) : null}
        {treeActions}
      </div>

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
            description={isMine && !q ? "Nothing of yours here." : q ? `Nothing matches “${q}”.` : "Nothing here."}
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

      {footer ? <footer className={styles["band"]}>{footer}</footer> : null}
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

export interface SidebarSession {
  readonly id: string;
  readonly title: string;
  /** In it with others; with the owner's face when it is not yours. */
  readonly shared?: boolean | undefined;
  readonly owner?: Person | null | undefined;
}

export interface SidebarSessionsProps {
  readonly sessions: ReadonlyArray<SidebarSession>;
  readonly selected?: string | null | undefined;
  readonly onSelect: (id: string) => void;
  /** The label opens the list of them all. */
  readonly onOpenList?: (() => void) | undefined;
  readonly onNew: () => void;
}

/**
 * Your brainstorm sessions, above the projects: only those you are in.
 * Each row has the bulb glyph; one shared carries the shared glyph, and the
 * owner's face when the owner is someone else. Then New session.
 */
export function SidebarSessions({ sessions, selected, onSelect, onOpenList, onNew }: SidebarSessionsProps) {
  return (
    <nav className={styles["sessions"]} aria-label="Sessions" data-testid="sidebar-sessions">
      <div className={styles["treeHead"]}>
        {onOpenList ? (
          <button type="button" className={cx(styles["link"], "ds-label")} onClick={onOpenList}>Sessions</button>
        ) : <span className="ds-label">Sessions</span>}
      </div>
      {sessions.map((s) => (
        <SidebarLink key={s.id} icon="brainstorm" current={selected === s.id} onClick={() => onSelect(s.id)} data-session={s.id}
          trailing={s.shared ? (
            <span className={styles["sessionShared"]} aria-label={s.owner ? `shared, ${s.owner.name}'s` : "shared"}>
              <Icon name="shared" size={12} />
              {s.owner ? <PersonAvatar person={s.owner} size={16} /> : null}
            </span>
          ) : undefined}>
          {s.title}
        </SidebarLink>
      ))}
      <SidebarLink icon="plus" onClick={onNew} data-testid="new-session">New session</SidebarLink>
    </nav>
  );
}

export interface SidebarRailItemProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, "type"> {
  /** Its accessible name, and its tooltip unless `tip` says more. */
  readonly label: string;
  readonly tip?: ReactNode;
  readonly shortcut?: string | undefined;
  readonly current?: boolean | undefined;
}

/**
 * One square of the collapsed sidebar: a sidebar row with its words folded
 * into a tooltip to the right. The app passes these as `railFooter`.
 */
export function SidebarRailItem({ label, tip, shortcut, current, className, children, ...rest }: SidebarRailItemProps) {
  return (
    <Tooltip content={tip ?? label} side="right" shortcut={shortcut}>
      <button type="button" className={cx(styles["railItem"], current && styles["railItemCurrent"], className)} aria-label={label}
        aria-current={current ? "page" : undefined} {...rest}>
        {children}
      </button>
    </Tooltip>
  );
}

export interface SidebarLinkProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, "type"> {
  /** An icon before the words. */
  readonly icon?: IconName | undefined;
  /** Anything before the words instead: a count pill, a face. */
  readonly leading?: ReactNode;
  /** A muted number at the end. */
  readonly count?: number | undefined;
  /** At the end, after the count: a tag ("Admin"). */
  readonly trailing?: ReactNode;
  /** It is what is open. */
  readonly current?: boolean | undefined;
}

/** A place in the sidebar that is not in the tree: an inbox, settings. A row, not a button-looking button. */
export function SidebarLink({ icon, leading, count, trailing, current, className, children, ...rest }: SidebarLinkProps) {
  return (
    <button type="button" className={cx(styles["navRow"], current && styles["navRowCurrent"], className)} aria-current={current ? "page" : undefined} {...rest}>
      {icon ? <Icon name={icon} size={14} className={styles["navIcon"]} /> : null}
      {leading}
      <span className={styles["navText"]}>{children}</span>
      {count !== undefined ? <span className={styles["navCount"]}>{count}</span> : null}
      {trailing}
    </button>
  );
}

export interface SidebarProfileProps extends Omit<HTMLAttributes<HTMLDivElement>, "children"> {
  readonly person: Person;
  /** Under the name: an email, a role. */
  readonly detail?: ReactNode;
  /** Open your settings. */
  readonly onOpen: () => void;
  /** At the end: a sign-out button. */
  readonly actions?: ReactNode;
  readonly openProps?: Record<string, string> | undefined;
}

/** You, at the bottom of the sidebar: your face, your name, and the way to your settings. */
export function SidebarProfile({ person, detail, onOpen, actions, openProps, className, ...rest }: SidebarProfileProps) {
  return (
    <div className={cx(styles["profile"], className)} {...rest}>
      <button type="button" className={styles["profileOpen"]} onClick={onOpen} title="Your settings" {...openProps}>
        <PersonAvatar person={person} size={40} aria-hidden title="" />
        <span className={styles["profileText"]}>
          <span className={styles["profileName"]}>{person.name}</span>
          {detail ? <span className={styles["profileDetail"]}>{detail}</span> : null}
        </span>
        <Icon name="settings" size={14} className={styles["profileGear"]} />
      </button>
      {actions}
    </div>
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

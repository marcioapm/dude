import { useCallback, useEffect, useId, useMemo, useRef, useState, type ButtonHTMLAttributes, type HTMLAttributes, type KeyboardEvent, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { focusedElement, returnFocus } from "../util/focusReturn.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { IconButton, type IconButtonProps } from "../primitives/Button.tsx";
import { EmptyState, Skeleton } from "../primitives/Feedback.tsx";
import { ScrollArea } from "../primitives/ScrollArea.tsx";
import {
  attentionItems,
  flattenNav,
  navKey,
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
  /** Controlled "Mine" (the tree shows only your tasks). Uncontrolled when omitted. */
  readonly mine?: boolean | undefined;
  readonly onMineChange?: ((mine: boolean) => void) | undefined;
  /** Beside the Projects label: a new-project button. */
  readonly treeActions?: ReactNode;
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
  mine,
  onMineChange,
  treeActions,
  menuItems,
  menu,
  width = 300,
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
            leading={waiting.yours.length > 0 ? <NeedsYouCount count={waiting.yours.length} /> : undefined}
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

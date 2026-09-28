import { forwardRef, useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState, type CSSProperties, type HTMLAttributes, type KeyboardEvent, type MouseEvent, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { RowMenu, RowMenuTrigger, focusIsFree, rowMenuOpeners, type RowMenuItem } from "../primitives/RowMenu.tsx";
import { statusSpec } from "../tokens/status.ts";
import {
  ancestorKeys,
  flattenNav,
  liveSessions,
  navKey,
  projectPeople,
  ownerAgents,
  type NavEpic,
  type NavFilter,
  type NavOverrides,
  type NavProject,
  type NavRef,
  type NavRow,
  type NavRun,
  type NavSession,
  type NavTask,
} from "../util/navModel.ts";
import { AgentAvatar, ROLE_LABEL } from "./AgentAvatar.tsx";
import { PersonAvatarStack } from "./PersonAvatar.tsx";
import { PrChip } from "./PrChip.tsx";
import { ProjectAvatar } from "./ProjectAvatar.tsx";
import { StatusBadge } from "./StatusBadge.tsx";
import { NeedsYouCount } from "./StatusMark.tsx";
import styles from "./NavTree.module.css";

export interface NavTreeProps extends Omit<HTMLAttributes<HTMLDivElement>, "onSelect"> {
  readonly projects: ReadonlyArray<NavProject>;
  readonly selected?: NavRef | null | undefined;
  readonly onSelect?: ((ref: NavRef, node: NavRow["node"]) => void) | undefined;
  /** Open/closed overrides by row key. Controlled when given with `onExpandedChange`. */
  readonly expanded?: NavOverrides | undefined;
  readonly onExpandedChange?: ((next: NavOverrides) => void) | undefined;
  readonly filter?: NavFilter | undefined;
  /** Called when the operator presses `/` in the tree: focus the search. */
  readonly onSearchRequest?: (() => void) | undefined;
  /**
   * Actions for a row's "…" menu. Return null (or nothing) for rows with no
   * actions; the tree then draws no trigger. The tree knows nothing about
   * the actions themselves — it only opens the menu on click, right-click
   * and Shift+F10 and returns focus to the row afterwards.
   */
  readonly menuItems?: ((row: NavRow) => ReadonlyArray<RowMenuItem> | null | undefined) | undefined;
  /**
   * Full control over what sits in the row's menu slot; spread `controls`
   * on a `RowMenu` so the row's right-click and Shift+F10 still open it.
   * Takes precedence over `menuItems`.
   */
  readonly menu?: ((row: NavRow, controls: NavRowMenuControls) => ReactNode) | undefined;
  readonly "aria-label"?: string | undefined;
}

/** What a custom row menu needs from the tree: open state and where focus goes when it closes. */
export interface NavRowMenuControls {
  readonly open: boolean;
  readonly onOpenChange: (open: boolean) => void;
  readonly onCloseAutoFocus: (e: Event) => void;
  /** "Actions for CP-41" — the trigger's accessible name. */
  readonly label: string;
}

/**
 * The navigation tree: Project → Epic → Task → Session, with earlier
 * Runs folded into one "Attempt n" row each. Rendered flat (every row is a
 * sibling with `aria-level`) so keyboard movement is index arithmetic and
 * project headers can stick to the top of the scroll.
 *
 * Each level has its own row grammar, so depth never rests on indentation
 * alone: projects are small-caps headers, epics carry the layers glyph,
 * tasks lead with a status mark and a mono key, sessions lead with a
 * role avatar on a guide line.
 *
 * Keyboard: ↑↓ move, → opens or steps in, ← closes or steps out, Home/End,
 * Enter/Space selects, `/` jumps to the search. Focus and selection are
 * separate, as in the ARIA tree pattern: you can walk the tree without
 * opening every row you pass.
 */
export const NavTree = forwardRef<HTMLDivElement, NavTreeProps>(function NavTree(
  { projects, selected, onSelect, expanded, onExpandedChange, filter, onSearchRequest, menuItems, menu, className, "aria-label": ariaLabel = "Projects", ...rest },
  ref,
) {
  const [localExpanded, setLocalExpanded] = useState<NavOverrides>(() => new Map());
  const overrides = expanded ?? localExpanded;
  const setOverrides = useCallback(
    (update: (prev: NavOverrides) => NavOverrides) => {
      const next = update(overrides);
      if (onExpandedChange) onExpandedChange(next);
      if (expanded === undefined) setLocalExpanded(next);
    },
    [overrides, onExpandedChange, expanded],
  );

  const rows = useMemo(() => flattenNav(projects, overrides, filter), [projects, overrides, filter]);
  const chosenKey = selected ? navKey(selected) : null;
  // A finished agent has no row of its own (only what works now does):
  // its task's row stands for it.
  const selectedKey = useMemo(() => {
    if (!selected || rows.some((r) => r.key === chosenKey)) return chosenKey;
    return [...ancestorKeys(projects, selected)].reverse().find((k) => k.startsWith("task:") && rows.some((r) => r.key === k)) ?? chosenKey;
  }, [selected, chosenKey, rows, projects]);

  // Selection is always visible: opening a deep node from elsewhere (a
  // notification, a link) unfolds its ancestors even if the user closed them.
  const lastRevealed = useRef<string | null>(null);
  useEffect(() => {
    if (!selected || chosenKey === lastRevealed.current) return;
    const path = ancestorKeys(projects, selected);
    // Not in the tree yet — just created, the data still loading: try again
    // when it arrives rather than giving up on revealing it.
    if (path.length === 0 && !rows.some((r) => r.key === chosenKey)) return;
    lastRevealed.current = chosenKey;
    // Pinned open, not just opened: an ancestor open only by default (a
    // project with something active) would fold under the selection once
    // that settles.
    const closed = path.filter((k) => overrides.get(k) !== true);
    if (closed.length === 0) return;
    setOverrides((prev) => {
      const next = new Map(prev);
      for (const k of closed) next.set(k, true);
      return next;
    });
  }, [selected, chosenKey, projects, overrides, rows, setOverrides]);

  const containerRef = useRef<HTMLDivElement>(null);
  useImperativeHandle(ref, () => containerRef.current as HTMLDivElement);
  const [focusedKey, setFocusedKey] = useState<string | null>(null);
  const tabStop = rows.find((r) => r.key === focusedKey)?.key ?? rows.find((r) => r.key === selectedKey)?.key ?? rows[0]?.key ?? null;

  const focusRow = (key: string) => {
    setFocusedKey(key);
    const el = containerRef.current?.querySelector<HTMLElement>(`[data-nav-key="${CSS.escape(key)}"]`);
    el?.focus();
  };

  const toggle = (row: NavRow, open?: boolean) => {
    if (!row.expandable || row.forced) return;
    setOverrides((prev) => {
      const next = new Map(prev);
      next.set(row.key, open ?? !row.expanded);
      return next;
    });
  };

  const select = (row: NavRow) => {
    onSelect?.(row.ref, row.node);
  };

  // One menu open at a time, keyed by row; the row itself opens it with a
  // right-click or Shift+F10, and focus comes back to the row on close so
  // the arrow keys keep working from where the operator was.
  const [menuKey, setMenuKey] = useState<string | null>(null);
  // A row can vanish under its open menu (a refresh folds its parent); the
  // menu unmounts with it and would otherwise leave the key pointing at
  // nothing, so the next row with that key would open already-open.
  useEffect(() => {
    if (menuKey !== null && !rows.some((r) => r.key === menuKey)) setMenuKey(null);
  }, [menuKey, rows]);
  const menuFor = (row: NavRow): ReactNode | null => {
    if (!menu && !menuItems) return null;
    const controls: NavRowMenuControls = {
      open: menuKey === row.key,
      onOpenChange: (open) => setMenuKey((cur) => (open ? row.key : cur === row.key ? null : cur)),
      onCloseAutoFocus: (e) => {
        // Never let Radix focus the "…" trigger (it is out of the tab
        // order). Give focus to the row only if nothing else took it: a
        // click on another row's "…" already holds focus, and pulling it
        // away would dismiss the menu that click just opened.
        e.preventDefault();
        if (focusIsFree(document)) focusRow(row.key);
      },
      label: `Actions for ${rowLabel(row)}`,
    };
    if (menu) return menu(row, controls) || null;
    const items = menuItems?.(row);
    if (!items || items.length === 0) return null;
    return <RowMenu items={items} label={controls.label} trigger={<RowMenuTrigger label={controls.label} />} open={controls.open} onOpenChange={controls.onOpenChange} onCloseAutoFocus={controls.onCloseAutoFocus} />;
  };

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>, row: NavRow, index: number, hasMenu: boolean) => {
    // Keys typed inside the row's menu bubble here through the portal; they
    // are the menu's, not the tree's ("/" must not jump to the search, → must
    // not move focus off the submenu).
    if (e.target !== e.currentTarget) return;
    if (hasMenu) {
      rowMenuOpeners(() => setMenuKey(row.key)).onKeyDown(e);
      if (e.defaultPrevented) return;
    }
    switch (e.key) {
      case "ArrowDown": {
        e.preventDefault();
        const next = rows[index + 1];
        if (next) focusRow(next.key);
        break;
      }
      case "ArrowUp": {
        e.preventDefault();
        const prev = rows[index - 1];
        if (prev) focusRow(prev.key);
        break;
      }
      case "ArrowRight": {
        e.preventDefault();
        if (row.expandable && !row.expanded) toggle(row, true);
        else if (row.expanded) {
          const child = rows[index + 1];
          if (child && child.parentKey === row.key) focusRow(child.key);
        }
        break;
      }
      case "ArrowLeft": {
        e.preventDefault();
        if (row.expanded && !row.forced) toggle(row, false);
        else if (row.parentKey) focusRow(row.parentKey);
        break;
      }
      case "Home": {
        e.preventDefault();
        const first = rows[0];
        if (first) focusRow(first.key);
        break;
      }
      case "End": {
        e.preventDefault();
        const last = rows[rows.length - 1];
        if (last) focusRow(last.key);
        break;
      }
      case "Enter":
      case " ": {
        e.preventDefault();
        select(row);
        break;
      }
      case "/": {
        if (onSearchRequest) {
          e.preventDefault();
          onSearchRequest();
        }
        break;
      }
      default:
        break;
    }
  };

  return (
    <div ref={containerRef} className={cx(styles["tree"], className)} role="tree" aria-label={ariaLabel} {...rest}>
      {rows.map((row, i) => {
        const rowMenu = menuFor(row);
        const hasMenu = rowMenu !== null;
        return (
          <NavTreeRow
            key={row.key}
            row={row}
            selected={row.key === selectedKey}
            tabIndex={row.key === tabStop ? 0 : -1}
            onFocus={() => setFocusedKey(row.key)}
            onKeyDown={(e) => onKeyDown(e, row, i, hasMenu)}
            onClick={() => select(row)}
            onToggle={() => toggle(row)}
            menu={rowMenu}
            menuOpen={menuKey === row.key}
            onContextMenu={hasMenu ? rowMenuOpeners(() => setMenuKey(row.key)).onContextMenu : undefined}
          />
        );
      })}
    </div>
  );
});

/** A row's accessible name: its title, and a task's key before it. */
function treeItemLabel(row: NavRow): string {
  if (row.ref.kind === "task") {
    const wi = row.node as NavTask;
    return wi.key ? `${wi.key} ${wi.title}` : wi.title;
  }
  return rowLabel(row);
}

/** A short name for a row, for accessible labels ("CP-41", "Webhook reliability"). */
export function rowLabel(row: NavRow): string {
  switch (row.ref.kind) {
    case "project":
      return (row.node as NavProject).name;
    case "epic":
      return (row.node as NavEpic).title;
    case "task": {
      const wi = row.node as NavTask;
      return wi.key ?? wi.title;
    }
    case "run":
      return `Attempt ${(row.node as NavRun).attempt}`;
    case "session": {
      const s = row.node as NavSession;
      return s.title ?? ROLE_LABEL[s.role];
    }
  }
}

// ---------------------------------------------------------------------------
// Rows
// ---------------------------------------------------------------------------

export interface NavTreeRowProps {
  readonly row: NavRow;
  readonly selected: boolean;
  readonly tabIndex: number;
  readonly onFocus: () => void;
  readonly onKeyDown: (e: KeyboardEvent<HTMLDivElement>) => void;
  readonly onClick: () => void;
  readonly onToggle: () => void;
  /** The row's "…" menu, shown on hover, focus, or while open. */
  readonly menu?: ReactNode;
  readonly menuOpen?: boolean | undefined;
  readonly onContextMenu?: ((e: MouseEvent<HTMLDivElement>) => void) | undefined;
}

/** One row of the tree; dispatches on `row.ref.kind`. */
export function NavTreeRow({ row, selected, tabIndex, onFocus, onKeyDown, onClick, onToggle, menu, menuOpen, onContextMenu }: NavTreeRowProps) {
  const kind = row.ref.kind;
  const menuSlot = menu ? (
    <span className={styles["menuSlot"]} data-open={menuOpen ? "true" : undefined}>
      {menu}
    </span>
  ) : null;
  const needsYou = row.triage === "needs_you";
  const onToggleClick = (e: MouseEvent) => {
    e.stopPropagation();
    onToggle();
  };
  const chevron =
    row.expandable && !row.forced ? (
      <button type="button" className={cx(styles["toggle"], row.expanded && styles["toggleOpen"])} aria-label={row.expanded ? "Collapse" : "Expand"} tabIndex={-1} onClick={onToggleClick}>
        <Icon name="chevron-right" size={14} className={styles["toggleIcon"]} />
      </button>
    ) : (
      <span className={cx(styles["toggle"], styles["toggleSpacer"])} aria-hidden>
        {row.expandable && row.forced ? <Icon name="chevron-right" size={14} className={cx(styles["toggleIcon"], styles["toggleIconForced"])} /> : null}
      </span>
    );

  const common = {
    role: "treeitem" as const,
    // Named by what it is: without this, a screen reader (and the row's
    // accessible name) would include its buttons — "Expand Greeter Actions
    // for Greeter".
    "aria-label": treeItemLabel(row),
    "aria-level": row.depth + 1,
    "aria-expanded": row.expandable ? row.expanded : undefined,
    "aria-selected": selected,
    tabIndex,
    onFocus,
    onKeyDown,
    onClick,
    onContextMenu,
    "data-nav-key": row.key,
    "data-kind": kind,
    "data-triage": row.triage ?? undefined,
    style: { "--depth": row.depth } as CSSProperties,
  };

  if (kind === "project") {
    const p = row.node as NavProject;
    const people = projectPeople(p);
    return (
      <div {...common} className={cx(styles["row"], styles["project"], selected && styles["selected"], menu ? styles["swapsForMenu"] : null)}>
        {chevron}
        <ProjectAvatar project={p} size={20} aria-hidden title={undefined} />
        <span className={styles["projectName"]}>{p.name}</span>
        {row.counts && row.counts.needs_you > 0 && !row.expanded ? <NeedsYouCount count={row.counts.needs_you} className={styles["count"]} /> : null}
        {people.length > 0 ? <PersonAvatarStack people={people} size={20} max={4} className={styles["rest"]} aria-hidden /> : null}
        {menuSlot}
      </div>
    );
  }

  if (kind === "epic") {
    const e = row.node as NavEpic;
    return (
      <div {...common} className={cx(styles["row"], styles["epic"], selected && styles["selected"])}>
        {chevron}
        <span className={styles["epicTitle"]}>{e.title}</span>
        {row.counts && row.counts.needs_you > 0 ? <NeedsYouCount count={row.counts.needs_you} className={styles["count"]} /> : null}
        <span className={styles["epicCount"]}>{e.tasks.length}</span>
        {menuSlot}
      </div>
    );
  }

  if (kind === "task") {
    const wi = row.node as NavTask;
    const spec = statusSpec(wi.status);
    const live = liveSessions(wi);
    const working = live.find((s) => s.status === "running");
    const people = wi.people ?? [];
    const agents = ownerAgents(wi, working);
    const pr = wi.pullRequests?.[0];
    return (
      <div {...common} className={cx(styles["row"], styles["task"], needsYou && styles["needsYou"], spec.terminal && styles["finished"], selected && styles["selected"])}>
        {chevron}
        <span className={styles["wiMain"]}>
          {wi.key ? <span className={styles["wiKey"]}>{wi.key}</span> : null}
          <span className={styles["wiTitle"]} title={wi.title}>
            {wi.title}
          </span>
        </span>
        {needsYou ? (
          <NeedsYouCount count={1} className={styles["count"]} />
        ) : pr ? (
          <PrChip pr={pr} iconOnly size="sm" className={styles["pr"]} onClick={(e) => e.stopPropagation()} tabIndex={-1} />
        ) : (
          <StatusBadge status={wi.status} variant="dot" iconOnly className={styles["mark"]} />
        )}
        {people.length > 0 ? <PersonAvatarStack people={people} size={20} max={3} agents={agents} className={styles["wiPeople"]} /> : null}
        {menuSlot}
      </div>
    );
  }

  if (kind === "run") {
    const r = row.node as NavRun;
    return (
      <div {...common} className={cx(styles["row"], styles["run"], selected && styles["selected"])}>
        {chevron}
        <StatusBadge status={r.status} variant="dot" iconOnly className={styles["mark"]} />
        <span className={styles["runTitle"]}>Attempt {r.attempt}</span>
        {menuSlot}
      </div>
    );
  }

  // An agent working now: its tile, what it is and what it is doing, its plan.
  const s = row.node as NavSession;
  const title = s.title ?? ROLE_LABEL[s.role];
  return (
    <div {...common} className={cx(styles["row"], styles["session"], needsYou && styles["needsYou"], selected && styles["selected"])}>
      <AgentAvatar role={s.role} size="xs" live={s.status === "running"} className={styles["sessionAvatar"]} />
      <span className={styles["sessionMain"]} title={s.activity ? `${title} · ${s.activity}` : title}>
        <span className={styles["sessionTitle"]}>{title}</span>
        {s.activity ? <span className={styles["sessionActivity"]}> · {s.activity}</span> : null}
      </span>
      {s.plan && s.plan.total > 0 ? (
        <span className={styles["sessionPlan"]} title={`Plan: ${s.plan.done} of ${s.plan.total}${s.plan.current ? `\nNow: ${s.plan.current}` : ""}`}>
          {s.plan.done}/{s.plan.total}
        </span>
      ) : needsYou ? (
        <StatusBadge status={s.status} variant="dot" iconOnly className={styles["mark"]} />
      ) : null}
      {menuSlot}
    </div>
  );
}

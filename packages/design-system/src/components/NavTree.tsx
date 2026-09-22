import { forwardRef, useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState, type CSSProperties, type HTMLAttributes, type KeyboardEvent, type MouseEvent } from "react";
import type { AgentRole } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { statusSpec } from "../tokens/status.ts";
import {
  ancestorKeys,
  flattenNav,
  navKey,
  workingRoles,
  type NavEpic,
  type NavFilter,
  type NavOverrides,
  type NavProject,
  type NavRef,
  type NavRow,
  type NavRun,
  type NavSession,
  type NavWorkItem,
} from "../util/navModel.ts";
import { AgentAvatar, ROLE_LABEL } from "./AgentAvatar.tsx";
import { HumanAvatarStack } from "./HumanAvatar.tsx";
import { StatusBadge } from "./StatusBadge.tsx";
import { TriageRollup } from "./TriageRollup.tsx";
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
  readonly "aria-label"?: string | undefined;
}

/**
 * The navigation tree: Project → Epic → Work item → Session, with earlier
 * Runs folded into one "Attempt n" row each. Rendered flat (every row is a
 * sibling with `aria-level`) so keyboard movement is index arithmetic and
 * project headers can stick to the top of the scroll.
 *
 * Each level has its own row grammar, so depth never rests on indentation
 * alone: projects are small-caps headers, epics carry the layers glyph,
 * work items lead with a status mark and a mono key, sessions lead with a
 * role avatar on a guide line.
 *
 * Keyboard: ↑↓ move, → opens or steps in, ← closes or steps out, Home/End,
 * Enter/Space selects, `/` jumps to the search. Focus and selection are
 * separate, as in the ARIA tree pattern: you can walk the tree without
 * opening every row you pass.
 */
export const NavTree = forwardRef<HTMLDivElement, NavTreeProps>(function NavTree(
  { projects, selected, onSelect, expanded, onExpandedChange, filter, onSearchRequest, className, "aria-label": ariaLabel = "Projects", ...rest },
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
  const selectedKey = selected ? navKey(selected) : null;

  // Selection is always visible: opening a deep node from elsewhere (a
  // notification, a link) unfolds its ancestors even if the user closed them.
  const lastRevealed = useRef<string | null>(null);
  useEffect(() => {
    if (!selected || selectedKey === lastRevealed.current) return;
    lastRevealed.current = selectedKey;
    const path = ancestorKeys(projects, selected);
    const closed = path.filter((k) => overrides.get(k) === false || !rows.some((r) => r.key === k && r.expanded));
    if (closed.length === 0) return;
    setOverrides((prev) => {
      const next = new Map(prev);
      for (const k of closed) next.set(k, true);
      return next;
    });
  }, [selected, selectedKey, projects, overrides, rows, setOverrides]);

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

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>, row: NavRow, index: number) => {
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
      {rows.map((row, i) => (
        <NavTreeRow
          key={row.key}
          row={row}
          selected={row.key === selectedKey}
          tabIndex={row.key === tabStop ? 0 : -1}
          onFocus={() => setFocusedKey(row.key)}
          onKeyDown={(e) => onKeyDown(e, row, i)}
          onClick={() => select(row)}
          onToggle={() => toggle(row)}
        />
      ))}
    </div>
  );
});

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
}

/** One row of the tree; dispatches on `row.ref.kind`. */
export function NavTreeRow({ row, selected, tabIndex, onFocus, onKeyDown, onClick, onToggle }: NavTreeRowProps) {
  const kind = row.ref.kind;
  const needsYou = row.triage === "needs_you";
  const onToggleClick = (e: MouseEvent) => {
    e.stopPropagation();
    onToggle();
  };
  const chevron =
    row.expandable && !row.forced ? (
      <button type="button" className={cx(styles["toggle"], row.expanded && styles["toggleOpen"])} aria-label={row.expanded ? "Collapse" : "Expand"} tabIndex={-1} onClick={onToggleClick}>
        <Icon name="chevron-right" size={12} className={styles["toggleIcon"]} />
      </button>
    ) : (
      <span className={cx(styles["toggle"], styles["toggleSpacer"])} aria-hidden>
        {row.expandable && row.forced ? <Icon name="chevron-right" size={12} className={cx(styles["toggleIcon"], styles["toggleIconForced"])} /> : null}
      </span>
    );

  const common = {
    role: "treeitem" as const,
    "aria-level": row.depth + 1,
    "aria-expanded": row.expandable ? row.expanded : undefined,
    "aria-selected": selected,
    tabIndex,
    onFocus,
    onKeyDown,
    onClick,
    "data-nav-key": row.key,
    "data-kind": kind,
    "data-triage": row.triage ?? undefined,
    style: { "--depth": row.depth } as CSSProperties,
  };

  if (kind === "project") {
    const p = row.node as NavProject;
    return (
      <div {...common} className={cx(styles["row"], styles["project"], selected && styles["selected"])}>
        {chevron}
        <span className={styles["projectName"]}>{p.name}</span>
        {row.counts && !row.expanded ? <TriageRollup counts={row.counts} className={styles["rollup"]} /> : null}
        {row.counts && row.expanded && row.counts.needs_you > 0 ? <TriageRollup counts={row.counts} only={["needs_you"]} className={styles["rollup"]} /> : null}
      </div>
    );
  }

  if (kind === "epic") {
    const e = row.node as NavEpic;
    return (
      <div {...common} className={cx(styles["row"], styles["epic"], selected && styles["selected"])}>
        {chevron}
        <Icon name="layers" size={12} className={styles["epicGlyph"]} />
        <span className={styles["epicTitle"]}>{e.title}</span>
        <span className={styles["epicCount"]}>{e.workItems.length}</span>
        {row.counts && !row.expanded ? <TriageRollup counts={row.counts} className={styles["rollup"]} /> : null}
        {row.counts && row.expanded && row.counts.needs_you > 0 ? <TriageRollup counts={row.counts} only={["needs_you"]} className={styles["rollup"]} /> : null}
      </div>
    );
  }

  if (kind === "workItem") {
    const wi = row.node as NavWorkItem;
    const roles = row.expanded ? [] : workingRoles(wi);
    const spec = statusSpec(wi.status);
    const finished = spec.terminal;
    return (
      <div {...common} className={cx(styles["row"], styles["workItem"], needsYou && styles["needsYou"], finished && styles["finished"], selected && styles["selected"])}>
        {chevron}
        <StatusBadge status={wi.status} variant="dot" iconOnly className={styles["mark"]} />
        <span className={styles["wiMain"]}>
          {wi.key ? <span className={styles["wiKey"]}>{wi.key}</span> : null}
          <span className={styles["wiTitle"]} title={wi.title}>
            {wi.title}
          </span>
        </span>
        <span className={styles["wiTrailing"]}>
          {roles.length > 0 ? <RoleStack roles={roles} /> : null}
          {wi.people && wi.people.length > 0 ? <HumanAvatarStack people={wi.people} size="xs" max={2} /> : null}
        </span>
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
        <span className={styles["runCount"]}>{r.sessions.length}</span>
      </div>
    );
  }

  const s = row.node as NavSession;
  const live = s.status === "running";
  const sspec = statusSpec(s.status);
  return (
    <div {...common} className={cx(styles["row"], styles["session"], needsYou && styles["needsYou"], sspec.terminal && styles["finished"], selected && styles["selected"])}>
      {chevron}
      <AgentAvatar role={s.role} size="xs" live={live} className={styles["sessionAvatar"]} />
      <span className={styles["sessionMain"]}>
        <span className={styles["sessionTitle"]}>{s.title ?? ROLE_LABEL[s.role]}</span>
        {s.activity && (live || needsYou) ? <span className={styles["sessionActivity"]}>{s.activity}</span> : null}
      </span>
      <StatusBadge status={s.status} variant="dot" iconOnly className={styles["mark"]} />
    </div>
  );
}

/** Roles working right now, as xs avatars. Distinct from the human stack: square, glyph, vivid. */
function RoleStack({ roles }: { readonly roles: ReadonlyArray<AgentRole> }) {
  const label = roles.map((r) => ROLE_LABEL[r]).join(", ");
  return (
    <span className={styles["roles"]} role="group" aria-label={`Working: ${label}`} title={`Working: ${label}`}>
      {roles.slice(0, 3).map((r) => (
        <AgentAvatar key={r} role={r} size="xs" aria-hidden />
      ))}
      {roles.length > 3 ? <span className={styles["rolesMore"]}>+{roles.length - 3}</span> : null}
    </span>
  );
}

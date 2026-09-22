import { useId, useMemo, useRef, useState, type HTMLAttributes, type KeyboardEvent, type ReactNode } from "react";
import type { AgentRole } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { EmptyState, Skeleton } from "../primitives/Feedback.tsx";
import { ScrollArea } from "../primitives/ScrollArea.tsx";
import { statusSpec } from "../tokens/status.ts";
import { sumTriage } from "../tokens/triage.ts";
import { formatDuration } from "../util/format.ts";
import { toMs, useNow } from "../util/useNow.ts";
import { navKey, workingRoles, type NavEpic, type NavProject, type NavRef, type NavRow, type NavSession } from "../util/navModel.ts";
import { BOARD_COLUMN_KINDS, BOARD_COLUMN_SPECS, boardCardCount, boardColumns, boardCost, liveActivity, type BoardCard, type BoardColumn, type LiveActivity } from "../util/boardModel.ts";
import { AgentAvatar } from "./AgentAvatar.tsx";
import { HumanAvatarStack } from "./HumanAvatar.tsx";
import { CostDisplay, Duration } from "./Numbers.tsx";
import { RoleStack } from "./RoleStack.tsx";
import { StatusBadge } from "./StatusBadge.tsx";
import { TriageRollup } from "./TriageRollup.tsx";
import styles from "./Board.module.css";

export interface BoardProps extends Omit<HTMLAttributes<HTMLElement>, "onSelect" | "title"> {
  readonly project: NavProject;
  /** Narrow the board to one epic. Cards then drop their epic line. */
  readonly epic?: NavEpic | null | undefined;
  /** A work item or one of its sessions; the card is marked current. */
  readonly selected?: NavRef | null | undefined;
  /** A needs-you card hands over the asking session, so one click lands on the question. */
  readonly onSelect?: ((ref: NavRef, node: NavRow["node"]) => void) | undefined;
  /** Cards shown per column before "N more". Urgent cards are never behind it. */
  readonly cap?: number | undefined;
  /** Data has not arrived yet. Skeleton columns instead of "nothing here". */
  readonly loading?: boolean | undefined;
  /** Right side of the header: filters, a new-task button. */
  readonly headerActions?: ReactNode;
  readonly hideHeader?: boolean | undefined;
}

/**
 * The overview for a project or an epic — what the sidebar opens when the
 * selection is one of those rather than a work item. Five columns by
 * lifecycle stage (`boardModel.ts`), always all five, always in order, so
 * the eye learns where to look; a column with nothing in it folds to a
 * labelled rail rather than an empty box.
 *
 * Nothing on the board is draggable. Every transition between columns is
 * the workflow's — the scheduler starts work, the agent opens the PR, the
 * checks make it ready, the merge closes it — and the two a person does
 * perform (confirm a plan, abort a run) are decisions with context, taken
 * in the transcript, not gestures. A card is a way in, not a handle.
 *
 * Keyboard: one tab stop; ↑↓ move within a column, ←→ across, Home/End,
 * Enter/Space open. Selection and focus are separate.
 */
export function Board({ project, epic, selected, onSelect, cap = 12, loading, headerActions, hideHeader, className, ...rest }: BoardProps) {
  const columns = useMemo(() => boardColumns(project, epic), [project, epic]);
  const total = boardCardCount(columns);
  const cost = boardCost(columns);
  const counts = useMemo(() => sumTriage(columns.map((c) => c.counts)), [columns]);
  // Time in column is read in hours and days; one clock for every card,
  // once a minute, is plenty and keeps fifty cards from owning fifty timers.
  const now = useNow(!loading, 60_000);

  const [revealed, setRevealed] = useState<ReadonlySet<string>>(() => new Set());
  const reveal = (kind: string) => setRevealed((prev) => new Set(prev).add(kind));

  const containerRef = useRef<HTMLElement>(null);
  const [focusedKey, setFocusedKey] = useState<string | null>(null);
  const selectedKey = selected ? navKey(selected) : null;

  const visible = useMemo(
    () => columns.map((c) => ({ column: c, cards: revealed.has(c.kind) ? c.cards : c.cards.slice(0, cap), hidden: revealed.has(c.kind) ? 0 : Math.max(0, c.cards.length - cap) })),
    [columns, revealed, cap],
  );
  const cardKey = (c: BoardCard) => navKey({ kind: "workItem", id: c.workItem.id });
  const allKeys = visible.flatMap((v) => v.cards.map(cardKey));
  const selectedCardKey = visible.flatMap((v) => v.cards).find((c) => cardContains(c, selectedKey)) ?? null;
  const tabStop = (focusedKey && allKeys.includes(focusedKey) ? focusedKey : null) ?? (selectedCardKey ? cardKey(selectedCardKey) : null) ?? allKeys[0] ?? null;

  const focusCard = (key: string) => {
    setFocusedKey(key);
    containerRef.current?.querySelector<HTMLElement>(`[data-board-key="${CSS.escape(key)}"]`)?.focus();
  };

  const onKeyDown = (e: KeyboardEvent<HTMLElement>, col: number, row: number) => {
    const list = (i: number) => visible[i]?.cards ?? [];
    const step = (dc: number) => {
      // Skip folded columns; land on the same row, or the last one there is.
      for (let i = col + dc; i >= 0 && i < visible.length; i += dc) {
        const cards = list(i);
        const target = cards[Math.min(row, cards.length - 1)];
        if (target) return focusCard(cardKey(target));
      }
    };
    switch (e.key) {
      case "ArrowDown": {
        e.preventDefault();
        const next = list(col)[row + 1];
        if (next) focusCard(cardKey(next));
        break;
      }
      case "ArrowUp": {
        e.preventDefault();
        const prev = list(col)[row - 1];
        if (prev) focusCard(cardKey(prev));
        break;
      }
      case "ArrowRight":
        e.preventDefault();
        step(1);
        break;
      case "ArrowLeft":
        e.preventDefault();
        step(-1);
        break;
      case "Home": {
        e.preventDefault();
        const first = list(col)[0];
        if (first) focusCard(cardKey(first));
        break;
      }
      case "End": {
        e.preventDefault();
        const cards = list(col);
        const last = cards[cards.length - 1];
        if (last) focusCard(cardKey(last));
        break;
      }
      default:
        break;
    }
  };

  const select = (card: BoardCard) => {
    if (!onSelect) return;
    if (card.asking) onSelect({ kind: "session", id: card.asking.id }, card.asking);
    else onSelect({ kind: "workItem", id: card.workItem.id }, card.workItem);
  };

  const scopeLabel = epic ? epic.title : project.name;

  return (
    <section ref={containerRef} className={cx(styles["root"], className)} aria-label={`${scopeLabel} board`} aria-busy={loading || undefined} {...rest}>
      {!hideHeader ? (
        <header className={styles["header"]}>
          <span className={styles["scope"]}>
            {epic ? (
              <>
                <span className={styles["scopeParent"]}>{project.name}</span>
                <Icon name="chevron-right" size={12} className={styles["scopeSep"]} />
                <Icon name="layers" size={12} className={styles["scopeGlyph"]} />
              </>
            ) : null}
            <span className={styles["scopeTitle"]}>{scopeLabel}</span>
          </span>
          {!loading ? (
            <span className={styles["summary"]}>
              <span className={styles["summaryCount"]}>{total === 1 ? "1 work item" : `${total} work items`}</span>
              <TriageRollup counts={counts} verbose className={styles["summaryRollup"]} />
              {cost > 0 ? <CostDisplay usd={cost} compact tone="muted" className={styles["summaryCost"]} /> : null}
            </span>
          ) : null}
          {headerActions ? <span className={styles["headerActions"]}>{headerActions}</span> : null}
        </header>
      ) : null}

      {loading ? (
        <BoardSkeleton />
      ) : total === 0 ? (
        <EmptyState icon="layers" title={epic ? "Nothing in this epic yet" : "No work items yet"} description="Give an agent a task and it appears here." className={styles["empty"]} />
      ) : (
        <div className={styles["columns"]}>
          {visible.map(({ column, cards, hidden }, ci) => (
            <BoardColumnView key={column.kind} column={column} folded={column.cards.length === 0}>
              {cards.map((card, ri) => {
                const key = cardKey(card);
                return (
                  <BoardCardView
                    key={key}
                    card={card}
                    now={now}
                    showEpic={!epic}
                    selected={cardContains(card, selectedKey)}
                    tabIndex={key === tabStop ? 0 : -1}
                    onFocus={() => setFocusedKey(key)}
                    onKeyDown={(e) => onKeyDown(e, ci, ri)}
                    onClick={() => select(card)}
                  />
                );
              })}
              {hidden > 0 ? (
                <li className={styles["more"]}>
                  <button type="button" className={styles["moreButton"]} onClick={() => reveal(column.kind)}>
                    {hidden} more
                  </button>
                </li>
              ) : null}
            </BoardColumnView>
          ))}
        </div>
      )}
    </section>
  );
}

/** Is `key` this card's work item, or a session or run inside it? */
function cardContains(card: BoardCard, key: string | null): boolean {
  if (key === null) return false;
  if (navKey({ kind: "workItem", id: card.workItem.id }) === key) return true;
  const inSessions = (list: ReadonlyArray<NavSession>): boolean => list.some((s) => navKey({ kind: "session", id: s.id }) === key || inSessions(s.children ?? []));
  return (card.workItem.runs ?? []).some((r) => navKey({ kind: "run", id: r.id }) === key || inSessions(r.sessions));
}

// ---------------------------------------------------------------------------
// Column
// ---------------------------------------------------------------------------

function BoardColumnView({ column, folded, children }: { readonly column: BoardColumn; readonly folded: boolean; readonly children: ReactNode }) {
  const spec = BOARD_COLUMN_SPECS[column.kind];
  const headingId = useId();
  return (
    <section className={cx(styles["column"], folded && styles["columnFolded"])} aria-labelledby={headingId} data-column={column.kind}>
      <header className={styles["columnHead"]} title={spec.description}>
        <span id={headingId} className={styles["columnLabel"]}>
          {spec.label}
        </span>
        <span className={styles["columnCount"]}>{column.cards.length}</span>
        {!folded ? <TriageRollup counts={column.counts} className={styles["columnRollup"]} /> : null}
      </header>
      {!folded ? (
        <ScrollArea fill className={styles["columnScroll"]}>
          <ul className={styles["cards"]}>{children}</ul>
        </ScrollArea>
      ) : null}
    </section>
  );
}

// ---------------------------------------------------------------------------
// Card
// ---------------------------------------------------------------------------

interface BoardCardViewProps {
  readonly card: BoardCard;
  readonly now: number;
  readonly showEpic: boolean;
  readonly selected: boolean;
  readonly tabIndex: number;
  readonly onFocus: () => void;
  readonly onKeyDown: (e: KeyboardEvent<HTMLElement>) => void;
  readonly onClick: () => void;
}

/**
 * One work item, three lines: where and how long · what · who and what it
 * costs. The status dot is the only colour at rest; a needs-you card adds
 * the same wash and bar the tree row gets, plus the question in attention
 * ink, so it is loud in the same way in both places.
 */
function BoardCardView({ card, now, showEpic, selected, tabIndex, onFocus, onKeyDown, onClick }: BoardCardViewProps) {
  const wi = card.workItem;
  const spec = statusSpec(wi.status);
  const needsYou = card.triage === "needs_you";
  const since = toMs(wi.statusSince);
  const roles = workingRoles(wi);
  const live = needsYou ? null : liveActivity(wi);
  return (
    <li>
      <button
        type="button"
        className={cx(styles["card"], needsYou && styles["needsYou"], spec.terminal && styles["finished"], selected && styles["selected"])}
        data-board-key={navKey({ kind: "workItem", id: wi.id })}
        data-triage={card.triage}
        data-status={wi.status}
        aria-current={selected ? "true" : undefined}
        tabIndex={tabIndex}
        onFocus={onFocus}
        onKeyDown={onKeyDown}
        onClick={onClick}
      >
        <span className={styles["top"]}>
          <StatusBadge status={wi.status} variant="dot" iconOnly className={styles["mark"]} />
          {wi.key ? <span className={styles["key"]}>{wi.key}</span> : null}
          {showEpic && card.epic ? (
            <span className={styles["epic"]} title={card.epic.title}>
              {card.epic.title}
            </span>
          ) : null}
          {since !== null ? <Duration ms={Math.max(0, now - since)} format="age" tone="muted" className={styles["age"]} title={`${spec.label} for ${formatDuration(Math.max(0, now - since), { style: "long" })}`} /> : null}
        </span>
        <span className={styles["title"]} title={wi.title}>
          {wi.title}
        </span>
        <span className={styles["meta"]}>
          {needsYou ? <AskLine card={card} /> : live ? <LiveLine roles={roles} live={live} /> : roles.length > 0 ? <RoleStack roles={roles} /> : null}
          <span className={styles["metaRight"]}>
            {wi.costUsd !== undefined && wi.costUsd > 0 ? <CostDisplay usd={wi.costUsd} compact tone="muted" className={styles["cost"]} /> : null}
            {wi.people && wi.people.length > 0 ? <HumanAvatarStack people={wi.people} size="xs" max={2} /> : null}
          </span>
        </span>
      </button>
    </li>
  );
}

function AskLine({ card }: { readonly card: BoardCard }) {
  const s = card.asking;
  const text = s ? (s.activity ?? "is waiting for you") : card.workItem.status === "awaiting_confirmation" ? "plan needs your confirmation" : "waiting for you";
  return (
    <span className={styles["ask"]} title={text}>
      {s ? <AgentAvatar role={s.role} size="xs" className={styles["asker"]} /> : <Icon name="question" size={12} className={styles["askGlyph"]} />}
      <span className={styles["askText"]}>{text}</span>
    </span>
  );
}

function LiveLine({ roles, live }: { readonly roles: ReadonlyArray<AgentRole>; readonly live: LiveActivity }) {
  return (
    <span className={styles["live"]} title={live.activity}>
      <RoleStack roles={roles} />
      <span className={styles["activity"]}>{live.activity}</span>
    </span>
  );
}

// ---------------------------------------------------------------------------

function BoardSkeleton() {
  const perColumn = [3, 2, 4, 2, 5];
  return (
    <div className={styles["columns"]} aria-label="Loading board">
      {BOARD_COLUMN_KINDS.map((kind, i) => (
        <section key={kind} className={styles["column"]} aria-hidden>
          <header className={styles["columnHead"]}>
            <span className={styles["columnLabel"]}>{BOARD_COLUMN_SPECS[kind].label}</span>
          </header>
          <ul className={styles["cards"]}>
            {Array.from({ length: perColumn[i] ?? 3 }, (_, j) => (
              <li key={j} className={styles["skeletonCard"]}>
                <span className={styles["skeletonRow"]}>
                  <Skeleton variant="circle" width={8} height={8} />
                  <Skeleton variant="text" width={44} />
                </span>
                <Skeleton variant="text" width={`${60 + ((i * 7 + j * 13) % 35)}%`} />
                <span className={styles["skeletonRow"]}>
                  <Skeleton variant="text" width={28} />
                  <Skeleton variant="circle" width={16} height={16} className={styles["skeletonAvatar"]} />
                </span>
              </li>
            ))}
          </ul>
        </section>
      ))}
    </div>
  );
}

import { useId, useMemo, useRef, useState, type HTMLAttributes, type KeyboardEvent, type ReactNode } from "react";
import type { AgentRole } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { EmptyState, Skeleton } from "../primitives/Feedback.tsx";
import { ScrollArea } from "../primitives/ScrollArea.tsx";
import { statusSpec } from "../tokens/status.ts";
import { sumTriage } from "../tokens/triage.ts";
import { firstName, formatDuration } from "../util/format.ts";
import { toMs, useNow } from "../util/useNow.ts";
import { liveSessions, navKey, ownerAgents, projectPeople, taskOwner, waitingWords, type NavEpic, type NavProject, type NavRef, type NavRow, type NavSession, type NavTask } from "../util/navModel.ts";
import {
  BOARD_COLUMN_KINDS,
  BOARD_COLUMN_SPECS,
  boardCardCount,
  boardColumns,
  boardCost,
  boardSwimlanes,
  type BoardCard,
  type BoardColumn,
  type BoardSwimlane,
} from "../util/boardModel.ts";
import { AgentAvatar, ROLE_LABEL } from "./AgentAvatar.tsx";
import { PlanMeter } from "./AgentPlan.tsx";
import { Cost } from "./Cost.tsx";
import { Duration } from "./Numbers.tsx";
import { PersonAvatarStack } from "./PersonAvatar.tsx";
import { PrChip } from "./PrChip.tsx";
import { ProjectAvatar } from "./ProjectAvatar.tsx";
import { NeedsYouCount, StatusMark } from "./StatusMark.tsx";
import styles from "./Board.module.css";

export interface BoardProps extends Omit<HTMLAttributes<HTMLElement>, "onSelect" | "title"> {
  readonly project: NavProject;
  /** The viewer's person id: only their asks are needs-you cards (`taskTriage`). */
  readonly you?: string | null | undefined;
  /** Narrow the board to one epic. Cards then drop their epic line. */
  readonly epic?: NavEpic | null | undefined;
  /** A task or one of its sessions; the card is marked current. */
  readonly selected?: NavRef | null | undefined;
  /** A needs-you card hands over the asking session, so one click lands on the question. */
  readonly onSelect?: ((ref: NavRef, node: NavRow["node"]) => void) | undefined;
  /** Cards shown per column before "N more". Urgent cards are never behind it. */
  readonly cap?: number | undefined;
  /** Data has not arrived yet. Skeleton columns instead of "nothing here". */
  readonly loading?: boolean | undefined;
  /** Right side of the header: filters, a new-task button. */
  readonly headerActions?: ReactNode;
  /** Under the header, above the columns: figures about the whole scope (an epic's time and cost). */
  readonly overview?: ReactNode;
  readonly hideHeader?: boolean | undefined;
  /**
   * Project boards only: one swimlane per epic in the project's order, then
   * "No epic", across the same five lanes. Ignored when `epic` is given.
   */
  readonly groupBy?: "epic" | null | undefined;
  /** Collapsed swimlanes by key (`epic:<id>` / `none`). Controlled when given with `onCollapsedChange`. */
  readonly collapsed?: ReadonlySet<string> | undefined;
  readonly onCollapsedChange?: ((next: ReadonlySet<string>) => void) | undefined;
  /** A swimlane header's "…" menu (a `RowMenu`); the DS knows no actions. */
  readonly laneMenu?: ((lane: BoardSwimlane) => ReactNode) | undefined;
}

/** What the board is drawing: one anonymous group, or a swimlane per epic. */
interface Group {
  readonly key: string;
  readonly lane: BoardSwimlane | null;
  readonly columns: ReadonlyArray<BoardColumn>;
}

/**
 * The overview for a project or an epic — what the sidebar opens when the
 * selection is one of those rather than a task. Five columns by
 * lifecycle stage (`boardModel.ts`), always all five, always in order, so
 * the eye learns where to look; an empty one keeps its heading and count.
 *
 * With `groupBy="epic"` the project board becomes swimlanes: a row per
 * epic in the project's order, then "No epic", each with the same five
 * columns, so the order an operator set in the tree is legible here and a
 * lane can be folded to its header.
 *
 * Nothing on the board is draggable. Every transition between columns is
 * the workflow's — the scheduler starts work, the agent opens the PR, the
 * checks make it ready, the merge closes it — and the two a person does
 * perform (confirm a plan, abort a run) are decisions with context, taken
 * in the transcript, not gestures. A card is a way in, not a handle.
 *
 * Keyboard: one tab stop; ↑↓ move within a column (continuing into the
 * next swimlane), ←→ across, Home/End, Enter/Space open. Selection and
 * focus are separate.
 */
export function Board({ project, you, epic, selected, onSelect, cap = 12, loading, headerActions, overview, hideHeader, groupBy, collapsed, onCollapsedChange, laneMenu, className, ...rest }: BoardProps) {
  const swimlanes = groupBy === "epic" && !epic;
  const columns = useMemo(() => boardColumns(project, epic, you), [project, epic, you]);
  const lanes = useMemo(() => (swimlanes ? boardSwimlanes(project, you) : []), [swimlanes, project, you]);
  const total = boardCardCount(columns);
  const cost = boardCost(columns);
  const counts = useMemo(() => sumTriage(columns.map((c) => c.counts)), [columns]);
  const scopePeople = useMemo(() => projectPeople(epic ? { id: project.id, name: project.name, epics: [epic] } : project), [project, epic]);
  // Time in column is read in hours and days; one clock for every card,
  // once a minute, is plenty and keeps fifty cards from owning fifty timers.
  const now = useNow(!loading, 60_000);

  // Controlled only with both props; `collapsed` alone is the initial state.
  const collapsedControlled = collapsed !== undefined && onCollapsedChange !== undefined;
  const [localCollapsed, setLocalCollapsed] = useState<ReadonlySet<string>>(() => collapsed ?? new Set());
  const collapsedSet = collapsedControlled ? collapsed : localCollapsed;
  const toggleLane = (key: string) => {
    const next = new Set(collapsedSet);
    if (next.has(key)) next.delete(key);
    else next.add(key);
    onCollapsedChange?.(next);
    if (!collapsedControlled) setLocalCollapsed(next);
  };

  const [revealed, setRevealed] = useState<ReadonlySet<string>>(() => new Set());
  const reveal = (key: string) => setRevealed((prev) => new Set(prev).add(key));

  const containerRef = useRef<HTMLElement>(null);
  const [focusedKey, setFocusedKey] = useState<string | null>(null);
  const selectedKey = selected ? navKey(selected) : null;

  const groups = useMemo<Group[]>(
    () => (swimlanes ? lanes.map((l) => ({ key: l.key, lane: l, columns: l.columns })) : [{ key: "all", lane: null, columns }]),
    [swimlanes, lanes, columns],
  );

  // What is actually drawn: per group and column, the cards before the cap
  // (or all of them once revealed); a collapsed swimlane draws nothing.
  const visible = useMemo(
    () =>
      groups.map((g) => ({
        group: g,
        open: !collapsedSet.has(g.key),
        columns: g.columns.map((c) => {
          const revealKey = `${g.key}/${c.kind}`;
          const all = revealed.has(revealKey);
          return { column: c, revealKey, cards: all ? c.cards : c.cards.slice(0, cap), hidden: all ? 0 : Math.max(0, c.cards.length - cap) };
        }),
      })),
    [groups, collapsedSet, revealed, cap],
  );
  const cardKey = (c: BoardCard) => navKey({ kind: "task", id: c.task.id });
  const allCards = visible.flatMap((g) => (g.open ? g.columns.flatMap((v) => v.cards) : []));
  const allKeys = allCards.map(cardKey);
  const selectedCard = allCards.find((c) => cardContains(c, selectedKey)) ?? null;
  const tabStop = (focusedKey && allKeys.includes(focusedKey) ? focusedKey : null) ?? (selectedCard ? cardKey(selectedCard) : null) ?? allKeys[0] ?? null;

  const focusCard = (key: string) => {
    setFocusedKey(key);
    containerRef.current?.querySelector<HTMLElement>(`[data-board-key="${CSS.escape(key)}"]`)?.focus();
  };

  const onKeyDown = (e: KeyboardEvent<HTMLElement>, gi: number, col: number, row: number) => {
    const cardsAt = (g: number, c: number) => (visible[g]?.open ? (visible[g]?.columns[c]?.cards ?? []) : []);
    // Same column, previous / next card — walking into the neighbouring
    // swimlane when this one runs out, so ↓ reads the column top to bottom.
    const vertical = (dir: 1 | -1) => {
      const here = cardsAt(gi, col)[row + dir];
      if (here) return focusCard(cardKey(here));
      for (let g = gi + dir; g >= 0 && g < visible.length; g += dir) {
        const cards = cardsAt(g, col);
        const target = dir === 1 ? cards[0] : cards[cards.length - 1];
        if (target) return focusCard(cardKey(target));
      }
    };
    // Across columns in this swimlane: skip folded ones; land on the same
    // row, or the last one there is.
    const horizontal = (dir: 1 | -1) => {
      for (let c = col + dir; c >= 0 && c < BOARD_COLUMN_KINDS.length; c += dir) {
        const cards = cardsAt(gi, c);
        const target = cards[Math.min(row, cards.length - 1)];
        if (target) return focusCard(cardKey(target));
      }
    };
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        vertical(1);
        break;
      case "ArrowUp":
        e.preventDefault();
        vertical(-1);
        break;
      case "ArrowRight":
        e.preventDefault();
        horizontal(1);
        break;
      case "ArrowLeft":
        e.preventDefault();
        horizontal(-1);
        break;
      case "Home": {
        e.preventDefault();
        const first = cardsAt(gi, col)[0];
        if (first) focusCard(cardKey(first));
        break;
      }
      case "End": {
        e.preventDefault();
        const cards = cardsAt(gi, col);
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
    else onSelect({ kind: "task", id: card.task.id }, card.task);
  };

  const scopeLabel = epic ? epic.title : project.name;
  // The heading says where you are with a face: the project's.

  return (
    <section ref={containerRef} className={cx(styles["root"], className)} aria-label={`${scopeLabel} board`} aria-busy={loading || undefined} {...rest}>
      {!hideHeader ? (
        <header className={styles["header"]}>
          <span className={styles["scope"]}>
            <ProjectAvatar project={project} size={epic ? 20 : 28} aria-hidden title={undefined} />
            {epic ? (
              <>
                <span className={styles["scopeParent"]}>{project.name}</span>
                <Icon name="chevron-right" size={14} className={styles["scopeSep"]} />
              </>
            ) : null}
            <h2 className={styles["scopeTitle"]}>{scopeLabel}</h2>
            {scopePeople.length > 0 ? <PersonAvatarStack people={scopePeople} size={28} max={6} className={styles["scopePeople"]} /> : null}
          </span>
          {!loading ? (
            <span className={styles["summary"]}>
              {counts.needs_you > 0 ? <NeedsYouCount count={counts.needs_you} verbose /> : null}
              <span className={styles["summaryCount"]}>{total === 1 ? "1 task" : `${total} tasks`}</span>
              {cost > 0 ? <Cost tokensUsd={cost} size="sm" tone="muted" className={styles["summaryCost"]} /> : null}
            </span>
          ) : null}
          {headerActions ? <span className={styles["headerActions"]}>{headerActions}</span> : null}
        </header>
      ) : null}
      {overview && !loading ? <div className={styles["overview"]}>{overview}</div> : null}

      {loading ? (
        <BoardSkeleton />
      ) : total === 0 ? (
        <EmptyState icon="layers" title={epic ? "Nothing in this epic yet" : "No tasks yet"} description="Give an agent a task and it appears here." className={styles["empty"]} />
      ) : (
        <div className={cx(styles["body"], swimlanes && styles["bodyLanes"])}>
          {swimlanes ? (
            <div className={styles["laneHead"]} aria-hidden>
              {columns.map((c) => (
                <span key={c.kind} className={styles["laneHeadCell"]}>
                  <span className={styles["columnLabel"]}>{c.spec.label}</span>
                  <span className={styles["columnCount"]}>{c.cards.length}</span>
                </span>
              ))}
            </div>
          ) : null}
          {visible.map(({ group, open, columns: cols }, gi) => (
            <BoardGroup key={group.key} lane={group.lane} open={open} onToggle={() => toggleLane(group.key)} menu={group.lane && laneMenu ? laneMenu(group.lane) : null}>
              {cols.map(({ column, revealKey, cards, hidden }, ci) => (
                <BoardColumnView key={column.kind} column={column} folded={swimlanes && column.cards.length === 0} inLane={swimlanes}>
                  {cards.map((card, ri) => {
                    const key = cardKey(card);
                    return (
                      <BoardCardView
                        key={key}
                        card={card}
                        now={now}
                        showEpic={!epic && !swimlanes}
                        selected={cardContains(card, selectedKey)}
                        tabIndex={key === tabStop ? 0 : -1}
                        onFocus={() => setFocusedKey(key)}
                        onKeyDown={(e) => onKeyDown(e, gi, ci, ri)}
                        onClick={() => select(card)}
                      />
                    );
                  })}
                  {hidden > 0 ? (
                    <li className={styles["more"]}>
                      <button type="button" className={styles["moreButton"]} onClick={() => reveal(revealKey)}>
                        {hidden} more
                      </button>
                    </li>
                  ) : null}
                </BoardColumnView>
              ))}
            </BoardGroup>
          ))}
        </div>
      )}
    </section>
  );
}

/** Is `key` this card's task, or a session or run inside it? */
function cardContains(card: BoardCard, key: string | null): boolean {
  if (key === null) return false;
  if (navKey({ kind: "task", id: card.task.id }) === key) return true;
  const inSessions = (list: ReadonlyArray<NavSession>): boolean => list.some((s) => navKey({ kind: "session", id: s.id }) === key || inSessions(s.children ?? []));
  return (card.task.runs ?? []).some((r) => navKey({ kind: "run", id: r.id }) === key || inSessions(r.sessions));
}

// ---------------------------------------------------------------------------
// Group: the plain five columns, or a swimlane with a header
// ---------------------------------------------------------------------------

interface BoardGroupProps {
  readonly lane: BoardSwimlane | null;
  readonly open: boolean;
  readonly onToggle: () => void;
  readonly menu: ReactNode;
  readonly children: ReactNode;
}

/**
 * A swimlane: the epic's title (with the layers glyph, as in the tree), a
 * count, the roll-up and the spend, then the five columns. The header is a
 * button that folds the lane to one 28px row. "No epic" has no glyph.
 */
function BoardGroup({ lane, open, onToggle, menu, children }: BoardGroupProps) {
  const headingId = useId();
  if (!lane) return <div className={styles["columns"]}>{children}</div>;
  const empty = lane.count === 0;
  return (
    <section className={cx(styles["lane"], !open && styles["laneClosed"], empty && styles["laneEmpty"])} aria-labelledby={headingId} data-lane={lane.key}>
      <header className={styles["laneHeader"]}>
        <button type="button" className={styles["laneToggle"]} aria-expanded={open} aria-controls={open ? `${headingId}-body` : undefined} onClick={onToggle}>
          <Icon name="chevron-right" size={14} className={cx(styles["laneChevron"], open && styles["laneChevronOpen"])} />
          {lane.epic ? <Icon name="layers" size={14} className={styles["laneGlyph"]} /> : null}
          <span id={headingId} className={cx(styles["laneTitle"], !lane.epic && styles["laneTitleNone"])}>
            {lane.title}
          </span>
          <span className={styles["laneCount"]}>{lane.count}</span>
        </button>
        {lane.counts.needs_you > 0 ? <NeedsYouCount count={lane.counts.needs_you} className={styles["laneRollup"]} /> : null}
        {lane.costUsd > 0 ? <Cost tokensUsd={lane.costUsd} size="sm" tone="muted" className={styles["laneCost"]} /> : null}
        {menu ? <span className={styles["laneMenu"]}>{menu}</span> : null}
      </header>
      {open ? (
        <div id={`${headingId}-body`} className={cx(styles["columns"], styles["laneColumns"])}>
          {empty ? <div className={styles["laneNothing"]}>Nothing in this epic yet.</div> : children}
        </div>
      ) : null}
    </section>
  );
}

// ---------------------------------------------------------------------------
// Column
// ---------------------------------------------------------------------------

function BoardColumnView({ column, folded, inLane, children }: { readonly column: BoardColumn; readonly folded: boolean; readonly inLane: boolean; readonly children: ReactNode }) {
  const spec = BOARD_COLUMN_SPECS[column.kind];
  const headingId = useId();
  // Inside a swimlane the labels live in the shared head row above; a
  // folded column is then just an empty cell, keeping the grid aligned.
  if (inLane) {
    return (
      <section className={cx(styles["column"], styles["columnInLane"], folded && styles["columnInLaneEmpty"])} aria-label={spec.label} data-column={column.kind}>
        {!folded ? <ul className={styles["cards"]}>{children}</ul> : null}
      </section>
    );
  }
  return (
    <section className={cx(styles["column"], folded && styles["columnFolded"])} aria-labelledby={headingId} data-column={column.kind}>
      <header className={styles["columnHead"]} title={spec.description}>
        <span id={headingId} className={styles["columnLabel"]}>
          {spec.label}
        </span>
        <span className={styles["columnCount"]}>{column.cards.length}</span>
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
 * One task: its key (and the one loud pill when it needs you), its title,
 * the question when an agent asks, its pull requests' states, how far the
 * working agent's plan has got, and its people with what is happening —
 * the agent working for the owner sits on the owner's face.
 */
function BoardCardView({ card, now, showEpic, selected, tabIndex, onFocus, onKeyDown, onClick }: BoardCardViewProps) {
  const wi = card.task;
  const spec = statusSpec(wi.status);
  const needsYou = card.triage === "needs_you";
  const since = toMs(wi.statusSince);
  const people = wi.people ?? [];
  const live = liveSessions(wi).filter((s) => s.status === "running");
  const working = live[0];
  const agents = ownerAgents(wi, working);
  const plan = working?.plan;
  const prs = wi.pullRequests ?? [];
  return (
    <li>
      <div
        role="button"
        className={cx(styles["card"], needsYou && styles["needsYou"], spec.terminal && styles["finished"], selected && styles["selected"])}
        data-board-key={navKey({ kind: "task", id: wi.id })}
        data-triage={card.triage}
        data-status={wi.status}
        aria-current={selected ? "true" : undefined}
        aria-label={wi.key ? `${wi.key} ${wi.title}` : wi.title}
        tabIndex={tabIndex}
        onFocus={onFocus}
        onKeyDown={(e) => {
          if ((e.key === "Enter" || e.key === " ") && e.target === e.currentTarget) {
            e.preventDefault();
            onClick();
            return;
          }
          onKeyDown(e);
        }}
        onClick={onClick}
      >
        <span className={styles["top"]}>
          {wi.key ? <span className={styles["key"]}>{wi.key}</span> : null}
          {showEpic && card.epic ? (
            <span className={styles["epic"]} title={card.epic.title}>
              {card.epic.title}
            </span>
          ) : null}
          <span className={styles["topEnd"]}>
            {needsYou ? (
              <StatusMark status="awaiting_input" size="sm" />
            ) : since !== null ? (
              <Duration ms={Math.max(0, now - since)} format="age" tone="muted" className={styles["age"]} title={`${spec.label} for ${formatDuration(Math.max(0, now - since), { style: "long" })}`} />
            ) : null}
          </span>
        </span>
        <span className={styles["title"]} title={wi.title}>
          {wi.title}
        </span>
        {needsYou ? <AskLine card={card} /> : null}
        {prs.length > 0 ? (
          <span className={styles["prs"]}>
            {prs.map((pr) => (
              <PrChip key={pr.url} pr={pr} size="sm" onClick={(e) => e.stopPropagation()} tabIndex={-1} />
            ))}
          </span>
        ) : null}
        {plan && plan.total > 0 ? (
          <span className={styles["plan"]} title={`Plan: ${plan.done} of ${plan.total} done${plan.current ? `\nNow: ${plan.current}` : ""}`}>
            <PlanMeter done={plan.done} total={plan.total} width={64} />
            <span className={styles["planCount"]}>
              {plan.done}/{plan.total}
            </span>
          </span>
        ) : null}
        <span className={styles["foot"]}>
          {people.length > 0 ? <PersonAvatarStack people={people} size={28} max={3} agents={agents} /> : working ? <AgentAvatar role={working.role} size="md" live /> : null}
          <span className={styles["doing"]}>
            <DoingLine task={wi} working={live} needsYou={needsYou} />
          </span>
          {wi.costUsd !== undefined && wi.costUsd > 0 ? <Cost tokensUsd={wi.costUsd} size="sm" tone="muted" className={styles["cost"]} /> : null}
        </span>
      </div>
    </li>
  );
}

/** Who, and what is happening: the working agent and what it is doing, or where the task stands. */
function DoingLine({ task, working, needsYou }: { readonly task: NavTask; readonly working: ReadonlyArray<NavSession>; readonly needsYou: boolean }) {
  const names = (task.people ?? []).map((p) => firstName(p.name)).join(", ");
  const owner = taskOwner(task);
  if (needsYou) return <><b>{names || "Nobody"}</b>waiting on {owner ? firstName(owner.name) : "a person"}</>;
  if (working.length > 1) return <><b>{working.length} agents working</b>{names}</>;
  const w = working[0];
  if (w) return <><b>{w.title ?? ROLE_LABEL[w.role]}</b>{w.activity ?? "working"}</>;
  const where: Partial<Record<NavTask["status"], string>> = {
    review: "waiting on GitHub", ready_to_merge: "ready to merge", done: "merged", failed: "failed", aborted: "stopped",
    queued: "not started", received: "not started", intake: "being shaped", awaiting_confirmation: "plan to confirm",
  };
  return <><b>{names || statusSpec(task.status).label}</b>{where[task.status] ?? statusSpec(task.status).label.toLowerCase()}</>;
}

function AskLine({ card }: { readonly card: BoardCard }) {
  const s = card.asking;
  const text = s ? (s.activity ?? "is waiting for you") : waitingWords(card.task);
  return (
    <span className={styles["ask"]} title={text}>
      {s ? `“${text}”` : text}
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

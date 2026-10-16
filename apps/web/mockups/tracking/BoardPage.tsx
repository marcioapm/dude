/*
 * Board screens: board-later, board-kinds, board-filters.
 *
 * The board is the design system's own `Board`. What the proposal adds:
 * the kind and priority marks on each card's first line (portalled into
 * the real card's top line, standing in for a proposed `cardLead` slot),
 * the Later shelf under the columns (`LaterShelf`, standing in for a
 * proposed `Board later` prop), the filter chips beside the existing
 * relations filter, and the archived count in the header.
 */

import { useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Board, RelationChip, StartMenu } from "@dude/design-system/components";
import { Icon } from "@dude/design-system";
import { Button } from "@dude/design-system/primitives";
import type { BoardCard } from "../../../../packages/design-system/src/util/boardModel.ts";
import { EPICS, KIND, PRIORITY, ROLE, YOU, archived, firstName, keyOf, laterOf, shelf, task, world, type Kind, type Priority, type Task } from "./data.ts";
import { KindMark, LaterGate, LaterMark, PriorityMark, TaskFilterBar } from "./Marks.tsx";
import styles from "./pages.module.css";

export interface BoardFilters {
  readonly kind: Kind | null;
  readonly priority: Priority | null;
  readonly mine: boolean;
  readonly relations: "all" | "startable" | "blocked";
}
export const NO_FILTERS: BoardFilters = { kind: null, priority: null, mine: false, relations: "all" };

const keep = (f: BoardFilters) => (t: Task) =>
  (f.kind === null || t.kind === f.kind) && (f.priority === null || t.priority === f.priority) && (!f.mine || t.owner.id === YOU.id);

/** Tasks sort by priority within a lane, then age: the order the board model would apply. */
const RANK: Record<Priority, number> = { P0: 0, P1: 1, P2: 2, P3: 3 };

/** The kind and priority, put into each real card's first line, after its key. */
function useLeadSlots(root: HTMLElement | null, version: unknown) {
  const [slots, setSlots] = useState<ReadonlyArray<{ key: string; node: HTMLElement }>>([]);
  useLayoutEffect(() => {
    if (!root) return;
    const out: { key: string; node: HTMLElement }[] = [];
    root.querySelectorAll<HTMLElement>("[data-board-key]").forEach((card) => {
      const key = keyOf(card.dataset["boardKey"]!.replace(/^task:/, ""));
      const top = card.firstElementChild as HTMLElement | null;
      if (!top) return;
      let node = top.querySelector<HTMLElement>(":scope > [data-lead-slot]");
      if (!node) {
        node = document.createElement("span");
        node.dataset["leadSlot"] = "";
        node.className = styles["leadSlot"]!;
        top.insertBefore(node, top.children[1] ?? null);
      }
      out.push({ key, node });
      const t = task(key);
      const words = [t.kind !== "feature" ? KIND[t.kind].label : null, t.priority !== "P2" ? t.priority : null].filter(Boolean).join(", ");
      if (words) card.setAttribute("aria-label", `${card.getAttribute("aria-label") ?? key}, ${words}`);
    });
    setSlots(out);
  }, [root, version]);
  return slots;
}

/** The Later shelf: under the columns, folded to one line by default. */
export function LaterShelf({ open, onOpenChange, onOpen }: { readonly open: boolean; readonly onOpenChange: (o: boolean) => void; readonly onOpen: (k: string) => void }) {
  const items = shelf();
  const own = items.filter((t) => t.later);
  const epicTasks = items.filter((t) => !t.later);
  return (
    <section className={styles["shelf"]} aria-label="Later" data-testid="later-shelf" data-open={open || undefined}>
      <button type="button" className={styles["shelfHead"]} aria-expanded={open} onClick={() => onOpenChange(!open)}>
        <Icon name={open ? "chevron-down" : "chevron-right"} size={14} />
        <Icon name="clock" size={14} />
        <span className={styles["shelfLabel"]}>Later</span>
        <span className={styles["shelfCount"]}>{items.length}</span>
        {!open ? (
          <span className={styles["shelfPeek"]}>
            {own.map((t) => <span key={t.key}><span className={styles["mono"]}>{t.key}</span> {t.later!.until ?? "no date"}</span>)}
            <span><Icon name="layers" size={12} /> {EPICS.alerts.title} · {epicTasks.length} tasks · {EPICS.alerts.later!.until}</span>
          </span>
        ) : <span className={styles["shelfPeek"]}>Not started, and won't be until brought back. Nothing here counts on the board.</span>}
      </button>
      {open ? (
        <div className={styles["shelfBody"]}>
          {own.map((t) => <ShelfCard key={t.key} t={t} onOpen={onOpen} />)}
          <div className={styles["shelfEpic"]} data-deferred="" data-testid="shelf-epic">
            <span className={styles["shelfEpicHead"]}>
              <Icon name="layers" size={14} /> <b>{EPICS.alerts.title}</b>
              <LaterMark until={EPICS.alerts.later!.until} />
              <span className={styles["muted"]}>put later by {firstName(EPICS.alerts.later!.by)}, with its {epicTasks.length} tasks</span>
            </span>
            <span className={styles["shelfEpicKeys"]}>{epicTasks.map((t) => <button key={t.key} type="button" className={styles["linkish"]} onClick={() => onOpen(t.key)}><span className={styles["mono"]}>{t.key}</span> {t.title}</button>)}</span>
          </div>
        </div>
      ) : null}
    </section>
  );
}

function ShelfCard({ t, onOpen }: { readonly t: Task; readonly onOpen: (k: string) => void }) {
  const l = laterOf(t)!;
  const reason = l.until ? `${t.key} is on Later until ${l.until}: bring it back to start it.` : `${t.key} is on Later with no date: bring it back to start it.`;
  const mayBring = ROLE === "owner";
  return (
    <div className={styles["shelfCard"]} data-deferred="" data-testid={`shelf-${t.key}`}>
      <button type="button" className={styles["shelfCardOpen"]} onClick={() => onOpen(t.key)} aria-label={`${t.key} ${t.title}, on Later${l.until ? ` until ${l.until}` : ""}`}>
        <span className={styles["shelfTop"]}>
          <span className={styles["mono"]}>{t.key}</span>
          <KindMark kind={t.kind} quietDefault />
          <PriorityMark priority={t.priority} />
          <span className={styles["grow"]} />
          <LaterMark until={l.until} compact />
        </span>
        <span className={styles["shelfTitle"]}>{t.title}</span>
        <span className={styles["muted"]}>{l.until ? `Comes back on ${l.until}` : "No date: comes back when someone brings it back"} · {firstName(l.by)}</span>
      </button>
      <span className={styles["shelfGate"]}>
        <LaterGate taskKey={t.key} reason={reason} size="sm" starts={[{ id: "start", label: "Start", icon: "play" }]}
          onBringBack={mayBring ? () => undefined : undefined}
          bringBackHint={mayBring ? undefined : `Only ${firstName(t.owner)}, the owner, or an admin can bring it back`} />
      </span>
    </div>
  );
}

export interface BoardPageProps {
  readonly filters: BoardFilters;
  readonly onFilters: (f: BoardFilters) => void;
  readonly shelfOpen: boolean;
  readonly onShelfOpen: (o: boolean) => void;
  readonly onOpen: (key: string) => void;
  readonly onArchived: () => void;
  readonly showChips: boolean;
}

export function BoardPage({ filters, onFilters, shelfOpen, onShelfOpen, onOpen, onArchived, showChips }: BoardPageProps) {
  const ref = useRef<HTMLDivElement>(null);
  const [root, setRoot] = useState<HTMLElement | null>(null);
  useLayoutEffect(() => setRoot(ref.current), []);
  const project = useMemo(() => {
    // Stands in for the proposed sort (priority, then age, after triage): the
    // board keeps the caller's order within a triage rank, so the epics are
    // handed over one priority band at a time.
    const p = world(keep(filters));
    const bands = (["P0", "P1", "P2", "P3"] as const).flatMap((band) => [
      ...(p.epics ?? []).map((e) => ({ ...e, tasks: e.tasks.filter((t) => task(t.key!).priority === band) })),
    ]);
    const loose = [...(p.tasks ?? [])].sort((a, b) => RANK[task(a.key!).priority] - RANK[task(b.key!).priority]);
    return { ...p, epics: bands, tasks: loose };
  }, [filters]);
  const slots = useLeadSlots(root, JSON.stringify(filters));
  const set = (patch: Partial<BoardFilters>) => onFilters({ ...filters, ...patch });
  const nArchived = archived().length;

  const cardMarks = useMemo(() => (card: BoardCard) => {
    const blockers = card.task.blockedBy ?? [];
    return blockers.length ? <RelationChip kind="blocked-by" tasks={blockers} /> : null;
  }, []);
  const cardStart = useMemo(() => (card: BoardCard, focus: { tabIndex: 0 | -1 }) => (
    <StartMenu taskKey={card.task.key!} blockers={card.task.blockedBy ?? []} ownerName={firstName(task(card.task.key!).owner)} onStart={() => undefined}
      tabIndex={focus.tabIndex} onStartAnyway={card.task.blockedBy?.length ? () => undefined : undefined} />
  ), []);

  const chips = [
    { id: "kind", label: "Kind", value: filters.kind ? KIND[filters.kind].label : undefined, onClear: () => set({ kind: null }),
      items: [{ kind: "radio" as const, id: "kind", label: "Kind", value: filters.kind ?? "any", onValueChange: (v: string) => set({ kind: v === "any" ? null : (v as Kind) }),
        items: [{ id: "any", label: "Any kind" }, ...(Object.keys(KIND) as Kind[]).map((k) => ({ id: k, label: KIND[k].plural, description: KIND[k].line, descriptionWraps: true }))] }] },
    { id: "priority", label: "Priority", value: filters.priority ?? undefined, onClear: () => set({ priority: null }),
      items: [{ kind: "radio" as const, id: "priority", label: "Priority", value: filters.priority ?? "any", onValueChange: (v: string) => set({ priority: v === "any" ? null : (v as Priority) }),
        items: [{ id: "any", label: "Any priority" }, ...(Object.keys(PRIORITY) as Priority[]).map((p) => ({ id: p, label: p, description: PRIORITY[p].line }))] }] },
    { id: "mine", label: "Mine", pressed: filters.mine, onToggle: () => set({ mine: !filters.mine }) },
  ];

  return (
    <div ref={ref} className={styles["boardHost"]} data-testid="board-page">
      <Board
        project={project}
        you={YOU.id}
        onSelect={(r) => onOpen(keyOf(r.id))}
        cardMarks={cardMarks}
        cardStart={cardStart}
        filter={filters.relations}
        onFilterChange={(relations) => set({ relations })}
        headerActions={
          <span className={styles["boardActions"]}>
            {showChips ? <TaskFilterBar chips={chips} /> : null}
            <Button variant="quiet" size="sm" leadingIcon="archive" onClick={onArchived} data-testid="archived-hint">{nArchived} archived</Button>
            <Button variant="primary" size="sm" leadingIcon="plus">New task</Button>
          </span>
        }
        overview={<LaterShelf open={shelfOpen} onOpenChange={onShelfOpen} onOpen={onOpen} />}
      />
      {slots.map((s) => {
        const t = task(s.key);
        return createPortal(<><KindMark kind={t.kind} quietDefault /><PriorityMark priority={t.priority} /></>, s.node, `lead-${s.key}`);
      })}
    </div>
  );
}

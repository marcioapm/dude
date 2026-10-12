/*
 * Screen 1 and 6: the project board and the epic board with relations.
 *
 * The board is the design system's own `Board`, untouched. What the
 * proposal adds to it — a slot on each card for its relation chips and its
 * start, and `highlight` (a card's relations stay lit, the rest dim) — is
 * drawn into the real cards through a portal, standing in for the two
 * props the doc proposes (`cardMarks`, `highlight`). Nothing here restyles
 * the board's own parts.
 */

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Board, Segmented } from "@dude/design-system/components";
import type { NavEpic, NavProject, NavTask } from "../../../../packages/design-system/src/util/navModel.ts";
import { NOT_STARTED, allTasks, blockedOf, isBlocked, keyOfId, neighbours, openBlockers, relatedOf, startable, task, world, YOU, blockersOf, resolved } from "./data.ts";
import { DependencyStrip, RelationChip, relationWords, type StripTask } from "./Relations.tsx";
import styles from "./pages.module.css";

export type BoardFilter = "all" | "startable" | "blocked";

/**
 * The card's relation slot: why it can't start, and what waits on it. No
 * start button: a card is a way in, and starting is a choice between two
 * ways made on the task (README, Board and "Starting is a choice"); the
 * lock chip carries the reason the start there is refused.
 */
function CardMarks({ k }: { readonly k: string }) {
  const t = task(k);
  const notStarted = NOT_STARTED.has(t.status);
  const blockers = notStarted ? openBlockers(k).map((b) => ({ key: b.key, title: b.title, status: b.status, project: b.project !== t.project ? b.project : undefined })) : [];
  // A blocker that is done holds nothing up any more: it says nothing.
  const waiting = resolved(t) ? [] : blockedOf(k).filter((b) => NOT_STARTED.has(b.task.status))
    .map((b) => ({ key: b.task.key, title: b.task.title, status: b.task.status, project: b.task.project !== t.project ? b.task.project : undefined }));
  if (blockers.length === 0 && waiting.length === 0) return null;
  return (
    <span className={styles["cardMarks"]}>
      {blockers.length > 0 ? <RelationChip kind="blocked-by" tasks={blockers} /> : null}
      {waiting.length > 0 ? <RelationChip kind="blocks" tasks={waiting} /> : null}
    </span>
  );
}

/**
 * Stands in for `Board cardMarks`: a node of ours inside each real card,
 * before its foot, that the marks portal into; and for `Board highlight`:
 * `data-related` / `data-dimmed` on the cards, which the board's CSS would own.
 */
function useCardSlots(root: HTMLElement | null, version: unknown, highlight: ReadonlySet<string> | null, focus: string | null) {
  const [slots, setSlots] = useState<ReadonlyArray<{ key: string; el: HTMLElement }>>([]);
  useLayoutEffect(() => {
    if (!root) return;
    const out: { key: string; el: HTMLElement }[] = [];
    root.querySelectorAll<HTMLElement>("[data-board-key]").forEach((card) => {
      const key = keyOfId(card.dataset["boardKey"]!.replace(/^task:/, ""));
      let slot = card.querySelector<HTMLElement>(":scope > [data-rel-slot]");
      if (!slot) {
        slot = document.createElement("span");
        slot.dataset["relSlot"] = "";
        slot.style.display = "contents";
        card.insertBefore(slot, card.lastElementChild);
      }
      out.push({ key, el: slot });
      // What a screen reader hears for the card: its key and title, then why it can't start.
      const t = task(key);
      const ob = openBlockers(key);
      const base = `${t.key} ${t.title}`;
      card.setAttribute("aria-label", NOT_STARTED.has(t.status) && ob.length ? `${base}, ${relationWords("blocked-by", ob).replace(/^B/, "b")}` : base);
    });
    setSlots(out);
  }, [root, version]);
  useEffect(() => {
    if (!root) return;
    root.querySelectorAll<HTMLElement>("[data-board-key]").forEach((card) => {
      const key = keyOfId(card.dataset["boardKey"]!.replace(/^task:/, ""));
      if (!highlight) {
        delete card.dataset["dimmed"];
        delete card.dataset["related"];
        return;
      }
      if (key === focus || highlight.has(key)) {
        delete card.dataset["dimmed"];
        card.dataset["related"] = key === focus ? "self" : "yes";
      } else {
        card.dataset["dimmed"] = "";
        delete card.dataset["related"];
      }
    });
  }, [root, highlight, focus, version, slots]);
  return slots;
}

function filtered(project: NavProject, keep: (t: NavTask) => boolean): NavProject {
  return { ...project, epics: project.epics?.map((e) => ({ ...e, tasks: e.tasks.filter(keep) })), tasks: project.tasks?.filter(keep) };
}

export interface BoardPageProps {
  readonly scope: "project" | "epic";
  /** A card's relations lit, for the screenshot; otherwise hover or focus sets it. */
  readonly hover?: string | null | undefined;
  readonly filter: BoardFilter;
  readonly onFilter: (f: BoardFilter) => void;
  readonly onOpen: (key: string) => void;
}

export function BoardPage({ scope, hover: forced, filter, onFilter, onOpen }: BoardPageProps) {
  const ref = useRef<HTMLDivElement>(null);
  const [root, setRoot] = useState<HTMLElement | null>(null);
  useLayoutEffect(() => setRoot(ref.current), []);
  const [hovered, setHovered] = useState<string | null>(null);
  const focus = forced ?? hovered;
  const highlight = useMemo(() => (focus ? neighbours(focus) : null), [focus]);

  const inScope = scope === "epic" ? allTasks().filter((t) => t.epic === "billing") : allTasks().filter((t) => t.project === "control-plane");
  const counts = { all: inScope.length, startable: inScope.filter(startable).length, blocked: inScope.filter(isBlocked).length };
  const { project, epic } = useMemo(() => {
    const keep = (n: NavTask) => {
      const t = task(n.key!);
      return filter === "all" || (filter === "startable" ? startable(t) : isBlocked(t));
    };
    const w = world();
    const e: NavEpic | null = scope === "epic" ? { ...w.billing, tasks: w.billing.tasks.filter(keep) } : null;
    return { project: filtered(w.controlPlane, keep), epic: e };
  }, [scope, filter]);
  const slots = useCardSlots(root, `${scope}/${filter}`, highlight, focus);

  // Hover or keyboard focus on a card lights its relations; leaving the board puts them out.
  const onOver = useCallback((e: { target: EventTarget | null }) => {
    const card = (e.target as HTMLElement | null)?.closest<HTMLElement>("[data-board-key]");
    setHovered(card ? keyOfId(card.dataset["boardKey"]!.replace(/^task:/, "")) : null);
  }, []);

  const strip = scope === "epic" ? <EpicStrip highlight={highlight} focus={focus} onOpen={onOpen} /> : null;
  const focusTask = focus ? task(focus) : null;
  // Said, not shown: a visible line here would move the board under the pointer.
  const lit = focusTask ? (
    <span className="ds-sr-only" role="status">
      {focusTask.key}'s relations: {[
        blockersOf(focusTask.key).length ? `blocked by ${blockersOf(focusTask.key).map((b) => b.task.key).join(", ")}` : null,
        blockedOf(focusTask.key).length ? `blocks ${blockedOf(focusTask.key).map((b) => b.task.key).join(", ")}` : null,
        relatedOf(focusTask.key).length ? `related to ${relatedOf(focusTask.key).map((b) => b.task.key).join(", ")}` : null,
      ].filter(Boolean).join("; ") || "none"}.
    </span>
  ) : null;

  return (
    <div ref={ref} className={styles["boardHost"]} onMouseOver={forced ? undefined : onOver} onFocus={forced ? undefined : onOver}
      onMouseLeave={forced ? undefined : () => setHovered(null)} data-testid="board-page">
      <Board
        project={project}
        epic={epic}
        you={YOU.id}
        onSelect={(r) => onOpen(keyOfId(r.id))}
        overview={strip ?? undefined}
        headerActions={
          <Segmented size="sm" label="Show" value={filter} onChange={onFilter}
            options={[
              { value: "all", label: `All ${counts.all}` },
              { value: "startable", label: `Can start ${counts.startable}` },
              { value: "blocked", label: `Blocked ${counts.blocked}` },
            ]} />
        }
      />
      {slots.map((s) => createPortal(<CardMarks k={s.key} />, s.el, s.key))}
      {lit}
    </div>
  );
}

/** The Billing v2 chain as steps: each task after the last step any of its blockers is in. */
function EpicStrip({ highlight, focus, onOpen }: { readonly highlight: ReadonlySet<string> | null; readonly focus: string | null; readonly onOpen: (k: string) => void }) {
  const tasks = allTasks().filter((t) => t.epic === "billing");
  const depth = new Map<string, number>();
  const level = (k: string, seen = new Set<string>()): number => {
    if (depth.has(k)) return depth.get(k)!;
    seen.add(k);
    const inEpic = blockersOf(k).filter((b) => b.task.epic === "billing" && !seen.has(b.task.key));
    const d = inEpic.length ? Math.max(...inEpic.map((b) => level(b.task.key, seen))) + 1 : 0;
    depth.set(k, d);
    return d;
  };
  tasks.forEach((t) => level(t.key));
  const steps: StripTask[][] = [];
  for (const t of tasks) {
    const d = depth.get(t.key)!;
    (steps[d] ??= []).push({ key: t.key, title: t.title, status: t.status, startable: startable(t), blockedBy: openBlockers(t.key).map((b) => b.key) });
  }
  const lit = highlight && focus ? new Set([...highlight, focus]) : null;
  return (
    <DependencyStrip steps={steps} onOpen={onOpen} highlight={lit}
      counts={{
        startable: tasks.filter(startable).length,
        blocked: tasks.filter(isBlocked).length,
        working: tasks.filter((t) => !NOT_STARTED.has(t.status) && !["done", "failed", "aborted"].includes(t.status)).length,
        done: tasks.filter(resolved).length,
        failed: tasks.filter((t) => t.status === "failed").length,
      }} />
  );
}

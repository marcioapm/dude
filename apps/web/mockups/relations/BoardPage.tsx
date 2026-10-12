/*
 * Screen 1 and 6: the project board and the epic board with relations.
 *
 * The board is the design system's own `Board`, untouched. What the
 * proposal adds to it — a slot on each card for its relation chips, a
 * Backlog card's start beside the card (a sibling of it, never inside),
 * and `highlight` (a card's relations stay lit, the rest dim) — is drawn
 * into the real cards through portals, standing in for the props the doc
 * proposes (`cardMarks`, `cardStart`, `highlight`). Nothing here restyles
 * the board's own parts.
 */

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Board, Segmented } from "@dude/design-system/components";
import type { NavEpic, NavProject, NavTask } from "../../../../packages/design-system/src/util/navModel.ts";
import {
  NOT_STARTED, allTasks, autoStartOf, blockedOf, doneUnmerged, isBlocked, isOwner, keyOfId, merged, neighbours, neverFires, openBlockers, relatedOf, startable, task, world, YOU, blockersOf,
} from "./data.ts";
import { AutoStartMark, DependencyStrip, RelationChip, StartMenu, relationWords, startsWhen, type RelatedTask, type StripTask } from "./Relations.tsx";
import { AutoStartDialog, OverrideDialog } from "./StartDialogs.tsx";
import styles from "./pages.module.css";

export type BoardFilter = "all" | "startable" | "blocked";

const firstName = (n: string) => n.split(" ")[0]!;

/** An open blocker as a chip, a menu and a dialog name it. */
const related = (b: ReturnType<typeof task>, here: ReturnType<typeof task>): RelatedTask & { unmerged: boolean } => ({
  key: b.key, title: b.title, status: b.status, project: b.project !== here.project ? b.project : undefined, unmerged: doneUnmerged(b),
});

/** The card's relation slot: why it can't start, what waits on it, and when it starts on its own. */
function CardMarks({ k }: { readonly k: string }) {
  const t = task(k);
  const notStarted = NOT_STARTED.has(t.status);
  const blockers = notStarted ? openBlockers(k).map((b) => related(b, t)) : [];
  // A blocker that has merged holds nothing up any more: it says nothing.
  const waiting = merged(t) ? [] : blockedOf(k).filter((b) => NOT_STARTED.has(b.task.status))
    .map((b) => ({ key: b.task.key, title: b.task.title, status: b.task.status, project: b.task.project !== t.project ? b.task.project : undefined }));
  const auto = blockers.length ? autoStartOf(k) : undefined;
  if (blockers.length === 0 && waiting.length === 0) return null;
  return (
    <span className={styles["cardMarks"]}>
      {blockers.length > 0 ? <RelationChip kind="blocked-by" tasks={blockers} /> : null}
      {waiting.length > 0 ? <RelationChip kind="blocks" tasks={waiting} /> : null}
      {auto ? (
        <span className={styles["cardAuto"]}>
          <AutoStartMark compact blockers={blockers} auto={{ how: auto.how, by: firstName(auto.by.name), ago: auto.ago }} />
        </span>
      ) : null}
    </span>
  );
}

type Dialogs = { readonly kind: "override" | "auto"; readonly key: string } | null;

/**
 * Stands in for `Board cardStart`: the start of a Backlog card, a sibling
 * of the card in its list item. The card stays one button that opens the
 * task; the start is its own button with its own focus stop, which the
 * board makes tabbable only on the card that holds the board's focus.
 */
function CardStart({ k, open, onOpenChange, tabbable, onDialog }: {
  readonly k: string; readonly open: boolean; readonly onOpenChange: (o: boolean) => void; readonly tabbable: boolean; readonly onDialog: (d: Dialogs) => void;
}) {
  const t = task(k);
  const blockers = openBlockers(k).map((b) => related(b, t));
  const auto = blockers.length ? autoStartOf(k) : undefined;
  const mine = isOwner(t);
  return (
    <span className={styles["cardStart"]}>
      <StartMenu taskKey={k} blockers={blockers} ownerName={firstName(t.owner.name)} open={open} onOpenChange={onOpenChange} tabIndex={tabbable ? 0 : -1}
        onStart={() => undefined}
        onStartAnyway={blockers.length ? () => onDialog({ kind: "override", key: k }) : undefined}
        onStartWhenUnblocked={mine ? () => onDialog({ kind: "auto", key: k }) : undefined}
        startWhenUnblockedRefused={neverFires(k)}
        auto={auto ? { how: auto.how, by: firstName(auto.by.name), ago: auto.ago, onCancel: mine ? () => undefined : undefined } : undefined} />
    </span>
  );
}

interface Slot { readonly key: string; readonly marks: HTMLElement; readonly start: HTMLElement | null }

/**
 * Stands in for `Board cardMarks` and `cardStart`: a node of ours inside
 * each real card, before its foot, that the marks portal into, and one
 * after the card in its list item for the start; and for `Board
 * highlight`: `data-related` / `data-dimmed` on the cards, which the
 * board's CSS would own.
 */
function useCardSlots(root: HTMLElement | null, version: unknown, highlight: ReadonlySet<string> | null, focus: string | null) {
  const [slots, setSlots] = useState<ReadonlyArray<Slot>>([]);
  useLayoutEffect(() => {
    if (!root) return;
    const out: Slot[] = [];
    root.querySelectorAll<HTMLElement>("[data-board-key]").forEach((card) => {
      const key = keyOfId(card.dataset["boardKey"]!.replace(/^task:/, ""));
      let marks = card.querySelector<HTMLElement>(":scope > [data-rel-slot]");
      if (!marks) {
        marks = document.createElement("span");
        marks.dataset["relSlot"] = "";
        marks.style.display = "contents";
        card.insertBefore(marks, card.lastElementChild);
      }
      const t = task(key);
      const item = card.parentElement!;
      let start = item.querySelector<HTMLElement>(":scope > [data-start-slot]");
      if (NOT_STARTED.has(t.status)) {
        if (!start) {
          start = document.createElement("span");
          start.dataset["startSlot"] = "";
          start.dataset["boardFor"] = key;
          start.style.display = "contents";
          item.style.position = "relative";
          item.appendChild(start);
        }
        card.dataset["hasStart"] = "";
      } else {
        start?.remove();
        start = null;
        delete card.dataset["hasStart"];
      }
      out.push({ key, marks, start });
      // Stands in for the proposed DoingLine change: done with nothing merged never reads "merged".
      if (doneUnmerged(t)) {
        const doing = card.querySelector<HTMLElement>("[class*='doing']");
        const words = doing?.lastChild;
        if (words && words.nodeType === Node.TEXT_NODE) words.textContent = "done, nothing merged";
      }
      // What a screen reader hears for the card: its key and title, then why it can't start, and when it will.
      const ob = openBlockers(key).map((b) => related(b, t));
      const auto = ob.length ? autoStartOf(key) : undefined;
      const base = `${t.key} ${t.title}`;
      const why = NOT_STARTED.has(t.status) && ob.length ? `, ${relationWords("blocked-by", ob).replace(/^B/, "b")}` : "";
      card.setAttribute("aria-label", `${base}${why}${auto ? `; ${startsWhen(ob).toLowerCase()}, ${auto.how === "deliver" ? "Deliver" : "Talk it through"}` : ""}`);
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
  /** A card's start menu open, for the screenshot. */
  readonly menu?: string | null | undefined;
  readonly filter: BoardFilter;
  readonly onFilter: (f: BoardFilter) => void;
  readonly onOpen: (key: string) => void;
}

export function BoardPage({ scope, hover: forced, menu: forcedMenu, filter, onFilter, onOpen }: BoardPageProps) {
  const ref = useRef<HTMLDivElement>(null);
  const [root, setRoot] = useState<HTMLElement | null>(null);
  useLayoutEffect(() => setRoot(ref.current), []);
  const [hovered, setHovered] = useState<string | null>(null);
  const [menu, setMenu] = useState<string | null>(forcedMenu ?? null);
  const [lastCard, setLastCard] = useState<string | null>(null);
  const [dialog, setDialog] = useState<Dialogs>(null);
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

  // Hover or keyboard focus on a card, or on its start, lights its relations; leaving the board puts them out.
  const onOver = useCallback((e: { target: EventTarget | null }) => {
    const el = (e.target as HTMLElement | null)?.closest<HTMLElement>("[data-board-key], [data-board-for]");
    setHovered(el ? (el.dataset["boardFor"] ?? keyOfId(el.dataset["boardKey"]!.replace(/^task:/, ""))) : null);
  }, []);
  // The start that takes Tab is the one under the card the board's focus is on.
  const onFocusCard = useCallback((e: { target: EventTarget | null }) => {
    const card = (e.target as HTMLElement | null)?.closest<HTMLElement>("[data-board-key]");
    if (card) setLastCard(keyOfId(card.dataset["boardKey"]!.replace(/^task:/, "")));
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
  const dialogTask = dialog ? task(dialog.key) : null;

  return (
    <div ref={ref} className={styles["boardHost"]} onMouseOver={forced ? undefined : onOver}
      onFocus={(e) => { onFocusCard(e); if (!forced) onOver(e); }}
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
      {slots.map((s) => createPortal(<CardMarks k={s.key} />, s.marks, `m-${s.key}`))}
      {slots.map((s) => (s.start ? createPortal(
        <CardStart k={s.key} open={menu === s.key} onOpenChange={(o) => setMenu(o ? s.key : null)} tabbable={lastCard === s.key} onDialog={setDialog} />,
        s.start, `s-${s.key}`) : null))}
      {lit}
      {dialogTask ? (
        dialog!.kind === "override"
          ? <OverrideDialog open onOpenChange={(o) => (o ? undefined : setDialog(null))} here={dialogTask} blockers={openBlockers(dialogTask.key).map((b) => ({ ...b, unmerged: doneUnmerged(b) }))} />
          : <AutoStartDialog open onOpenChange={(o) => (o ? undefined : setDialog(null))} here={dialogTask} blockers={openBlockers(dialogTask.key)} />
      ) : null}
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
        merged: tasks.filter(merged).length,
        doneUnmerged: tasks.filter(doneUnmerged).length,
        failed: tasks.filter((t) => t.status === "failed").length,
      }} />
  );
}

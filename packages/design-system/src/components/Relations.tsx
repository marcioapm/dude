import { forwardRef, useId, useState, type ReactNode } from "react";
import type { AgentRole } from "@dude/domain";
import { AUTO_START_HOW_LABEL, blockerList, blockerStateWord, keyList, onceMerged, startsWhen } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { Button, IconButton, type ButtonProps } from "../primitives/Button.tsx";
import { RowMenu, type RowMenuItem } from "../primitives/RowMenu.tsx";
import { Tooltip } from "../primitives/Tooltip.tsx";
import type { NavRelated } from "../util/navModel.ts";
import { PersonAvatar, type Person } from "./PersonAvatar.tsx";
import { RefLead } from "./RefLead.tsx";
import { StatusMark } from "./StatusMark.tsx";
import styles from "./Relations.module.css";

/*
 * Task relations (docs/design/task-relations.md): what a task waits on and
 * what it holds up, said in one verb pair everywhere — "Blocked by" and
 * "Blocks" — and the ways a blocked task can still be started. No tone is
 * new: blocked waits on work, not a person, so it is neutral ink with a
 * lock and words, never the attention wash.
 */

/** A related task as these pieces name it. */
export type RelatedTask = NavRelated;

export type RelationChipKind = "blocked-by" | "blocks";

/** "Blocked by CP-41 (in review) and CP-37 (done, nothing merged)": the chip's name, never a glyph alone. */
export function relationWords(kind: RelationChipKind, tasks: ReadonlyArray<RelatedTask>): string {
  return `${kind === "blocked-by" ? "Blocked by" : "Blocks"} ${kind === "blocked-by" ? blockerList(tasks) : keyList(tasks.map((t) => t.key))}`;
}

/** The reason every way to start a blocked task gives: "Blocked by …: it can start once they're merged." */
export function blockedReason(blockers: ReadonlyArray<RelatedTask>): string {
  return `${relationWords("blocked-by", blockers)}: ${onceMerged(blockers.length)}.`;
}

export { startsWhen };

/**
 * A related task's state as these pieces say it: "in review", "done, nothing
 * merged" for one marked done with nothing merged, "merged" for one done
 * that merged. (An open blocker that is done is always unmerged: the chip's
 * words, `relationWords`, read it so.)
 */
export const stateWord = (t: Pick<RelatedTask, "status" | "unmerged">) =>
  t.unmerged ? blockerStateWord("done", true) : t.status === "done" ? "merged" : blockerStateWord(t.status);

export interface RelationChipProps {
  readonly kind: RelationChipKind;
  /** The open edges only: a blocker that has merged no longer shows. */
  readonly tasks: ReadonlyArray<RelatedTask>;
  readonly className?: string | undefined;
}

/**
 * On a board card, between the title and its foot: why it can't start, or
 * what waits on it. "Blocked by" sits on the active wash with the lock;
 * "Blocks" is quieter, words and glyph only. One task is named with its
 * status mark, so the chain reads on the card ("Blocked by ◐ CP-41"); more
 * are counted. The tooltip lists each, with its project when it is another.
 * A blocker done with nothing merged takes no status mark (its check would
 * read as released) and says "not merged".
 */
export function RelationChip({ kind, tasks, className }: RelationChipProps) {
  if (tasks.length === 0) return null;
  const one = tasks.length === 1 ? tasks[0]! : null;
  const blocked = kind === "blocked-by";
  return (
    <Tooltip
      side="bottom"
      content={
        <span className={styles["tip"]}>
          <span className={styles["tipHead"]}>{blocked ? "Can't start until these are merged" : "These wait on it"}</span>
          {tasks.map((t) => (
            <span key={t.key} className={styles["tipRow"]}>
              <RefLead type="task" taskKey={t.key} status={t.status} />
              <span className={styles["tipTitle"]}>{t.title}</span>
              {t.unmerged ? <span className={styles["tipNote"]}>nothing merged</span> : null}
              {t.project ? <span className={styles["tipNote"]}>{t.project}</span> : null}
            </span>
          ))}
        </span>
      }
    >
      <span className={cx(styles["chip"], blocked ? styles["chipBlocked"] : styles["chipBlocks"], className)}
        data-relation={kind} role="img" aria-label={relationWords(kind, tasks)} tabIndex={-1}>
        <Icon name={blocked ? "lock" : "blocks"} size={12} className={styles["glyph"]} />
        <span>{blocked ? "Blocked by" : "Blocks"}</span>
        {one ? (
          <>
            {one.unmerged ? null : <StatusMark status={one.status} iconOnly size="sm" />}
            <span className={styles["chipKey"]}>{one.key}</span>
            {one.unmerged ? <span className={styles["chipNote"]}>not merged</span> : null}
          </>
        ) : (
          <span className={styles["chipCount"]}>{tasks.length}</span>
        )}
      </span>
    </Tooltip>
  );
}

// ---------------------------------------------------------------------------
// Start when unblocked, once set
// ---------------------------------------------------------------------------

export interface AutoStartView {
  readonly how: "deliver" | "talk";
  /** Who set it, by first name, and how long ago ("2d"). */
  readonly by: string;
  readonly ago: string;
  /** The owner only: takes it back. Absent, the mark is read only. */
  readonly onCancel?: (() => void) | undefined;
}

export { AUTO_START_HOW_LABEL as HOW_LABEL };

export interface AutoStartMarkProps {
  readonly auto: AutoStartView;
  readonly blockers: ReadonlyArray<RelatedTask>;
  /** On a card: one muted line, no wash, no Cancel (the card's start menu has it). */
  readonly compact?: boolean | undefined;
}

/**
 * A blocked task its owner set to start when unblocked: the play glyph,
 * "Starts when CP-41 merges · Deliver", who set it, and for the owner a
 * quiet Cancel. Its tooltip says every blocker is read again when it fires.
 * A promise about a start, not a status: no tone, no count.
 */
export function AutoStartMark({ auto, blockers, compact }: AutoStartMarkProps) {
  const words = `${startsWhen(blockers)} · ${AUTO_START_HOW_LABEL[auto.how]}`;
  const who = `${auto.by} set it ${auto.ago} ago. Every blocker is checked again when it fires: if one is open, nothing starts.`;
  return (
    <span className={styles["autoWrap"]} data-testid="auto-start-mark">
      <Tooltip content={who} side="bottom">
        <span className={cx(styles["auto"], compact && styles["autoCompact"])} tabIndex={compact ? -1 : 0} aria-label={`${words}. ${who}`}>
          <Icon name="play" size={12} className={styles["glyph"]} />
          <span className={styles["autoWords"]}>{words}</span>
        </span>
      </Tooltip>
      {!compact ? <span className={styles["autoWho"]} aria-hidden>{auto.by} set it {auto.ago} ago</span> : null}
      {!compact && auto.onCancel ? (
        <Button variant="quiet" size="sm" onClick={auto.onCancel} aria-label={`Cancel: ${words}`} data-testid="cancel-auto-start">Cancel</Button>
      ) : null}
    </span>
  );
}

// ---------------------------------------------------------------------------
// The start, refused
// ---------------------------------------------------------------------------

export interface StartGateProps {
  /** The blockers still open. */
  readonly blockers: ReadonlyArray<RelatedTask>;
  /** The ways to start, each refused for the same reason, in the header's order. */
  readonly starts: ReadonlyArray<{ readonly id: string; readonly label: string; readonly icon: IconName }>;
  /** A person may override: opens Start anyway. Absent for a reader with no right to start. */
  readonly onStartAnyway?: (() => void) | undefined;
  /** The owner, until it is set: opens the choice of how it starts once unblocked. */
  readonly onStartWhenUnblocked?: (() => void) | undefined;
  /** Why even the owner can't: a blocker that will never merge. Shown on the item, disabled. */
  readonly startWhenUnblockedRefused?: string | null | undefined;
}

/**
 * Where Deliver and Talk it through sit on a blocked task: both stay,
 * disabled — never gone — with the reason in their tooltip and their
 * description. Then, for its owner, Start when unblocked…, and Start
 * anyway…, which opens the confirmation naming the blockers.
 */
export function StartGate({ blockers, starts, onStartAnyway, onStartWhenUnblocked, startWhenUnblockedRefused }: StartGateProps) {
  const reasonId = useId();
  const refusedId = useId();
  const reason = blockedReason(blockers);
  return (
    <span className={styles["gate"]} data-testid="start-gate">
      <span id={reasonId} className="ds-sr-only">{reason}</span>
      {starts.map((s) => (
        <Tooltip key={s.id} content={reason} side="bottom">
          {/* A focusable wrapper: a disabled button takes no pointer or focus, so its tooltip would never open.
              The wrapper is the control assistive tech meets (a button's children are presentational). */}
          <span className={styles["gateTrigger"]} tabIndex={0} role="button" aria-disabled="true" aria-label={s.label}
            aria-describedby={reasonId} data-testid={`gate-${s.id}`}>
            <Button variant="secondary" leadingIcon={s.icon} disabled>{s.label}</Button>
          </span>
        </Tooltip>
      ))}
      {onStartWhenUnblocked && startWhenUnblockedRefused ? (
        <Tooltip content={startWhenUnblockedRefused} side="bottom">
          <span className={styles["gateTrigger"]} tabIndex={0} role="button" aria-disabled="true" aria-label="Start when unblocked…"
            aria-describedby={refusedId}>
            <span id={refusedId} className="ds-sr-only">{startWhenUnblockedRefused}</span>
            <Button variant="quiet" leadingIcon="play" disabled>Start when unblocked…</Button>
          </span>
        </Tooltip>
      ) : onStartWhenUnblocked ? (
        <Button variant="quiet" leadingIcon="play" onClick={onStartWhenUnblocked} data-testid="start-when-unblocked">Start when unblocked…</Button>
      ) : null}
      {onStartAnyway ? <Button variant="quiet" onClick={onStartAnyway} data-testid="start-anyway">Start anyway…</Button> : null}
    </span>
  );
}

// ---------------------------------------------------------------------------
// A Backlog card's start
// ---------------------------------------------------------------------------

/** The menu's trigger with the reason as its tooltip: one element takes both slots' props and ref. */
const StartTrigger = forwardRef<HTMLButtonElement, ButtonProps & { readonly tip: string | null }>(function StartTrigger({ tip, ...rest }, ref) {
  const button = <Button ref={ref} {...rest} />;
  return tip ? <Tooltip content={tip} side="bottom">{button}</Tooltip> : button;
});

export interface StartMenuProps {
  /** The task, by key: names the trigger ("Start CP-44"). */
  readonly taskKey: string;
  /** Open blockers; none, the ways work. */
  readonly blockers: ReadonlyArray<RelatedTask>;
  readonly onStart: (how: "deliver" | "talk") => void;
  /** A person may: the same dialog as the task page's. */
  readonly onStartAnyway?: (() => void) | undefined;
  /** Set: the note and its Cancel at the menu's foot. */
  readonly auto?: AutoStartView | undefined;
  /** The owner sets it; absent, the item is disabled, saying whose it is. */
  readonly onStartWhenUnblocked?: (() => void) | undefined;
  /** Why even the owner can't: a blocker that will never merge. */
  readonly startWhenUnblockedRefused?: string | null | undefined;
  /** The owner's first name, for the reason the item is someone else's. */
  readonly ownerName: string;
  readonly open?: boolean | undefined;
  readonly onOpenChange?: ((open: boolean) => void) | undefined;
  /** The board's roving rule: 0 only beside the card that holds the board's focus. */
  readonly tabIndex?: 0 | -1 | undefined;
  /** Gallery only: drawn open in place. */
  readonly forceMount?: true | undefined;
  readonly container?: HTMLElement | null | undefined;
}

/**
 * A Backlog card's start: "▷ Start ⌄", opening `RowMenu` with Talk it
 * through and Deliver, neither first by default (starting is a choice, so
 * never a split button: its face would be one way preselected). Blocked,
 * the trigger carries the lock and the reason, both ways are disabled with
 * it, and the menu still offers Start when unblocked… and Start anyway…,
 * which is why the trigger itself stays operable.
 */
export function StartMenu({ taskKey, blockers, onStart, onStartAnyway, auto, onStartWhenUnblocked, startWhenUnblockedRefused, ownerName, open, onOpenChange, tabIndex, forceMount, container }: StartMenuProps) {
  const blocked = blockers.length > 0;
  const reason = blocked ? blockedReason(blockers) : null;
  const ways: RowMenuItem[] = [
    { id: "talk", label: "Talk it through", icon: "message", description: "The conductor plans with you first", disabled: blocked, disabledReason: reason ?? undefined, onSelect: () => onStart("talk") },
    { id: "deliver", label: "Deliver", icon: "zap", description: "The automatic pipeline", disabled: blocked, disabledReason: reason ?? undefined, onSelect: () => onStart("deliver") },
  ];
  const around: RowMenuItem[] = !blocked ? [] : [
    { kind: "separator" },
    ...(auto ? [
      { kind: "note" as const, id: "auto-note", text: `${startsWhen(blockers)} · ${AUTO_START_HOW_LABEL[auto.how]}, as ${auto.by} set ${auto.ago} ago.` },
      auto.onCancel
        ? { id: "auto-cancel", label: "Cancel start when unblocked", icon: "close" as const, onSelect: auto.onCancel }
        : { id: "auto-cancel", label: "Cancel start when unblocked", icon: "close" as const, disabled: true, disabledReason: `Only ${ownerName}, its owner, can cancel this.` },
    ] : [
      onStartWhenUnblocked && !startWhenUnblockedRefused
        ? { id: "auto", label: "Start when unblocked…", icon: "play" as const, onSelect: onStartWhenUnblocked }
        : { id: "auto", label: "Start when unblocked…", icon: "play" as const, disabled: true,
            disabledReason: startWhenUnblockedRefused ?? `Only ${ownerName}, its owner, can set this.` },
    ]),
    ...(onStartAnyway ? [{ id: "anyway", label: "Start anyway…", icon: "arrow-right" as const, onSelect: onStartAnyway }] : []),
  ];
  return (
    <RowMenu items={[...ways, ...around]} align="end" open={open} onOpenChange={onOpenChange} forceMount={forceMount} container={container}
      trigger={
        <StartTrigger tip={reason} size="sm" variant="quiet" tabIndex={tabIndex}
          className={cx(styles["startTrigger"], blocked && styles["startBlocked"])}
          aria-label={blocked ? `Start ${taskKey}: ${reason}` : `Start ${taskKey}`} data-testid={`start-menu-${taskKey}`}>
          <Icon name={blocked ? "lock" : "play"} size={12} className={styles["glyph"]} />
          <span>Start</span>
          <Icon name="chevron-down" size={12} className={styles["glyph"]} />
        </StartTrigger>
      } />
  );
}

// ---------------------------------------------------------------------------
// A task's Relations
// ---------------------------------------------------------------------------

export type RelationGroupKind = "blocked-by" | "blocks" | "related";

export interface RelationRowData extends RelatedTask {
  readonly owner?: Person | null | undefined;
  /** The owner's agent at work, on their face. */
  readonly agent?: AgentRole | undefined;
  /** A blocker that is merged: shown settled. */
  readonly settled?: boolean | undefined;
  /**
   * Set when the reader may not remove this row: who may, in words ("Only
   * Ana, the owner, can remove this"). No ✕ is drawn; its place is kept.
   */
  readonly removeHint?: string | undefined;
}

export interface RelationGroupData {
  readonly kind: RelationGroupKind;
  readonly rows: ReadonlyArray<RelationRowData>;
}

const GROUP_LABEL: Record<RelationGroupKind, string> = { "blocked-by": "Blocked by", blocks: "Blocks", related: "Related" };
const GROUP_GLYPH: Record<RelationGroupKind, IconName> = { "blocked-by": "lock", blocks: "blocks", related: "link" };

export interface RelationRowProps {
  readonly group: RelationGroupKind;
  readonly row: RelationRowData;
  /** The hint's element, in the group's head. */
  readonly hintId: string;
  readonly onRemove?: ((group: RelationGroupKind, key: string) => void) | undefined;
  readonly onOpen?: ((key: string) => void) | undefined;
  /** Hover or focus on a row whose remove is someone else's. */
  readonly onHint?: ((on: boolean) => void) | undefined;
}

/**
 * One relation, in `RemovableList`'s grammar: status mark and key, title,
 * project if another, "nothing merged" for a blocker marked done without a
 * merge, status in words, the owner's face with the working agent on it,
 * and the remove — or, for a reader who may not, its place kept empty.
 */
export function RelationRow({ group, row: r, hintId, onRemove, onOpen, onHint }: RelationRowProps) {
  return (
    <li className={cx(styles["row"], r.settled && styles["settled"])} data-key={r.key}
      onMouseEnter={r.removeHint ? () => onHint?.(true) : undefined} onMouseLeave={r.removeHint ? () => onHint?.(false) : undefined}
      onFocus={r.removeHint ? () => onHint?.(true) : undefined} onBlur={r.removeHint ? () => onHint?.(false) : undefined}>
      <button type="button" className={styles["rowOpen"]} onClick={() => onOpen?.(r.key)} aria-label={`${r.key} ${r.title}, ${stateWord(r)}`}
        aria-describedby={r.removeHint ? hintId : undefined}>
        <RefLead type="task" taskKey={r.key} status={r.status} />
        <span className={styles["rowTitle"]}>{r.title}</span>
        {r.project ? <span className={styles["rowNote"]}>{r.project}</span> : null}
      </button>
      {r.removeHint ? <span id={hintId} className="ds-sr-only">{r.removeHint}</span> : null}
      {r.unmerged ? <span className={styles["rowNote"]}>nothing merged</span> : null}
      <StatusMark status={r.status} size="sm" className={styles["rowStatus"]} />
      {r.owner ? (
        <PersonAvatar person={r.owner} size={20} agent={r.agent} live={r.agent ? true : undefined}
          title={r.agent ? `${r.owner.name}'s ${r.agent} is working` : r.owner.name} />
      ) : <span className={styles["faceSlot"]} aria-hidden />}
      {onRemove && !r.removeHint ? (
        <IconButton size="sm" icon="close" label={`Remove ${GROUP_LABEL[group].toLowerCase()} ${r.key}`} onClick={() => onRemove(group, r.key)} />
      ) : onRemove ? <span className={styles["removeSlot"]} aria-hidden /> : null}
    </li>
  );
}

export interface RelationListProps {
  readonly groups: ReadonlyArray<RelationGroupData>;
  /** Fill a rail page's main column instead of the default relations measure. */
  readonly wide?: boolean | undefined;
  /** Given, each row the reader may change has a quiet remove. */
  readonly onRemove?: ((group: RelationGroupKind, key: string) => void) | undefined;
  /** Given, the head has Add relation (any member may add). */
  readonly onAdd?: (() => void) | undefined;
  readonly onOpen?: ((key: string) => void) | undefined;
  /** The add form, open under the head (a `RelationPicker`). */
  readonly adding?: ReactNode;
  /** A row whose hint shows from the start: a static picture of hovering it (gallery). */
  readonly hinting?: string | undefined;
}

/**
 * A task's relations: three groups under small-caps heads with their glyph
 * and count — Blocked by ("1 of 2 open" once one has merged), Blocks,
 * Related. An empty group is left out; with none at all, one line says
 * what relations are for. A row the reader may not remove says whose it
 * is, in the group's head, on its hover or focus, so nothing in the row
 * moves under the pointer; the same words are the row's description.
 */
export function RelationList({ groups, onRemove, onAdd, onOpen, adding, hinting, wide }: RelationListProps) {
  const shown = groups.filter((g) => g.rows.length > 0);
  const headId = useId();
  const [hovered, setHovered] = useState<string | null>(hinting ?? null);
  return (
    <section className={cx(styles["relations"], wide && styles["relationsWide"])} aria-labelledby={headId} data-testid="relations">
      <header className={styles["relationsHead"]}>
        <h2 id={headId} className="ds-label">Relations</h2>
        {onAdd && !adding ? <Button variant="quiet" size="sm" leadingIcon="plus" onClick={onAdd} data-testid="add-relation">Add relation</Button> : null}
      </header>
      {adding}
      {shown.length === 0 && !adding ? (
        <p className={styles["muted"]}>No relations. A task that must wait for another says so here, and can't start before it is merged.</p>
      ) : null}
      {shown.map((g) => {
        const open = g.kind === "blocked-by" ? g.rows.filter((r) => !r.settled).length : null;
        const hint = g.rows.find((r) => `${g.kind}:${r.key}` === hovered)?.removeHint;
        const hintId = `${headId}-${g.kind}-hint`;
        return (
          <div key={g.kind} className={styles["group"]} role="group" aria-label={GROUP_LABEL[g.kind]} data-group={g.kind}>
            <div className={styles["groupHead"]}>
              <Icon name={GROUP_GLYPH[g.kind]} size={12} className={styles["glyph"]} />
              <span>{GROUP_LABEL[g.kind]}</span>
              <span className={styles["groupCount"]}>{open !== null && open < g.rows.length ? `${open} of ${g.rows.length} open` : g.rows.length}</span>
              <span className={cx(styles["removeHint"], hint && styles["removeHintOn"])} aria-hidden data-testid={hint ? "remove-hint" : undefined}>{hint ?? ""}</span>
            </div>
            <ul className={styles["rows"]}>
              {g.rows.map((r) => {
                const id = `${g.kind}:${r.key}`;
                return (
                  <RelationRow key={r.key} group={g.kind} row={r} hintId={`${hintId}-${r.key}`} onRemove={onRemove} onOpen={onOpen}
                    onHint={(on) => setHovered((h) => (on ? id : h === id ? null : h))} />
                );
              })}
            </ul>
          </div>
        );
      })}
    </section>
  );
}

// ---------------------------------------------------------------------------
// The epic's order
// ---------------------------------------------------------------------------

export interface StripTask extends RelatedTask {
  /** Not started, nothing open in front of it. */
  readonly startable: boolean;
  /** Its open blockers' keys. */
  readonly blockedBy: ReadonlyArray<string>;
}

export interface DependencyCounts {
  readonly startable: number;
  readonly blocked: number;
  readonly working: number;
  readonly merged: number;
  readonly doneUnmerged: number;
  readonly failed: number;
}

export interface DependencyStripProps {
  /** Steps in order (boardModel's `dependencySteps`): each step's tasks wait only on earlier steps'. */
  readonly steps: ReadonlyArray<ReadonlyArray<StripTask>>;
  readonly counts: DependencyCounts;
  readonly onOpen?: ((key: string) => void) | undefined;
  /** Lit keys (a hovered card and its relations); the rest dim. */
  readonly highlight?: ReadonlySet<string> | null | undefined;
}

/**
 * An epic's order: steps left to right on the chrome shade, a task as its
 * status mark and key with "can start" or the lock, and the counts in
 * words above. Not a graph: the steps are the order, the tooltip the
 * edges, and editing happens on the task. Omitted (null) with one step.
 */
export function DependencyStrip({ steps, counts, onOpen, highlight }: DependencyStripProps) {
  if (steps.length < 2) return null;
  const parts = ([
    [counts.startable, "can start"], [counts.blocked, "blocked"], [counts.working, "in progress or review"],
    [counts.merged, "merged"], [counts.doneUnmerged, "done, nothing merged"], [counts.failed, "failed"],
  ] as const).filter(([n]) => n > 0);
  return (
    <section className={styles["strip"]} aria-label="Order of work" data-testid="dependency-strip">
      <div className={styles["stripHead"]}>
        <span className="ds-label">Order</span>
        <span className={styles["stripCounts"]}>
          {parts.map(([n, w], i) => (
            <span key={w}>{i > 0 ? <span className={styles["dot"]} aria-hidden>· </span> : null}<b className={styles["stripNum"]}>{n}</b> {w}</span>
          ))}
        </span>
      </div>
      <ol className={styles["steps"]}>
        {steps.map((tasks, i) => (
          <li key={i} className={styles["step"]}>
            <span className={styles["stepNo"]} aria-label={`Step ${i + 1}`}>{i + 1}</span>
            <ul className={styles["stepTasks"]}>
              {tasks.map((t) => {
                const dim = highlight && !highlight.has(t.key);
                const name = `${t.key} ${t.title}, ${stateWord(t)}${t.startable ? ", can start" : t.blockedBy.length ? `, blocked by ${keyList(t.blockedBy)}` : ""}`;
                return (
                  <li key={t.key}>
                    <Tooltip side="bottom" content={<span className={styles["tip"]}><span className={styles["tipTitle"]}>{t.title}</span>
                      {t.blockedBy.length ? <span className={styles["tipHead"]}>Waits on {keyList(t.blockedBy)}</span> : null}</span>}>
                      <button type="button" className={cx(styles["stripTask"], dim && styles["dim"])} onClick={() => onOpen?.(t.key)} aria-label={name} data-key={t.key} data-dim={dim || undefined}>
                        <RefLead type="task" taskKey={t.key} status={t.status} />
                        {t.startable ? <span className={styles["canStart"]}>can start</span>
                          : t.blockedBy.length ? <Icon name="lock" size={12} className={cx(styles["glyph"], styles["stripLock"])} /> : null}
                      </button>
                    </Tooltip>
                  </li>
                );
              })}
            </ul>
            {i < steps.length - 1 ? <Icon name="chevron-right" size={14} className={styles["stepArrow"]} /> : null}
          </li>
        ))}
      </ol>
    </section>
  );
}

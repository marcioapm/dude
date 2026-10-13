/*
 * The design system's proposed relation pieces, built here at its quality
 * so the mockup can show them: tokens only, both themes and densities, and
 * names a screen reader reads. Each is listed in docs/design/task-relations.md
 * with its props, its README text and its gallery section.
 *
 *   RelationGlyph   the `lock`, `blocks` and `link` glyphs on the icon grid
 *   RelationChip    "Blocked by CP-41" / "Blocked by 2" / "Blocks 3" on a card
 *   RelationList    a task's Relations: Blocked by, Blocks, Related
 *   StartGate       a start that is refused, why, Start when unblocked and Start anyway
 *   AutoStartMark   "Starts when CP-41 merges · Deliver", once set
 *   StartMenu       a Backlog card's start: Talk it through or Deliver, and the ways around a block
 *   DependencyStrip an epic's order, in steps, with what can start
 */

import { forwardRef, useId, useState, type ReactNode } from "react";
import type { TaskStatus } from "@dude/domain";
import { Icon } from "@dude/design-system";
import { PersonAvatar, RefLead, StatusMark, type Person } from "@dude/design-system/components";
import { Button, IconButton, RowMenu, Tooltip, type RowMenuItem } from "@dude/design-system/primitives";
import type { ButtonProps } from "../../../../packages/design-system/src/primitives/Button.tsx";
import type { AgentRole } from "@dude/domain";
import styles from "./relations.module.css";

// ---------------------------------------------------------------------------
// Glyphs. Proposed for `Icon` (src/icons): paths on the 16px grid, stroked at
// the icon's own rule (1.5 under 20px). `link` exists already.
// ---------------------------------------------------------------------------

export type RelationGlyphName = "lock" | "blocks" | "link";

const PATHS: Record<Exclude<RelationGlyphName, "link">, string> = {
  /* A padlock: the task cannot start. */
  lock: "M4.75 7.25h6.5a1 1 0 0 1 1 1v4.25a1 1 0 0 1-1 1h-6.5a1 1 0 0 1-1-1V8.25a1 1 0 0 1 1-1ZM5.75 7.25V5.5a2.25 2.25 0 0 1 4.5 0v1.75",
  /* An arrow into a bar: this holds something else up. */
  blocks: "M2.5 8h7M6.75 5.25 9.5 8l-2.75 2.75M12.75 3.5v9",
};

export function RelationGlyph({ name, size = 12, className }: { readonly name: RelationGlyphName; readonly size?: number; readonly className?: string | undefined }) {
  if (name === "link") return <Icon name="link" size={size} className={[styles["glyph"], className].filter(Boolean).join(" ")} />;
  return (
    <svg viewBox="0 0 16 16" width={size} height={size} fill="none" stroke="currentColor" strokeWidth={size >= 20 ? 1.75 : 1.5}
      strokeLinecap="round" strokeLinejoin="round" aria-hidden className={[styles["glyph"], className].filter(Boolean).join(" ")} data-icon={name}>
      <path d={PATHS[name]} />
    </svg>
  );
}

// ---------------------------------------------------------------------------
// The chip
// ---------------------------------------------------------------------------

export interface RelatedTask {
  readonly key: string;
  readonly title: string;
  readonly status: TaskStatus;
  /** Its project, when it is not the board's: said in the tooltip. */
  readonly project?: string | undefined;
  /** Done with nothing merged: settled for its owner, still blocking. */
  readonly unmerged?: boolean | undefined;
}

export type RelationChipKind = "blocked-by" | "blocks";

export interface RelationChipProps {
  readonly kind: RelationChipKind;
  /** The open edges only: a blocker that has merged no longer shows. */
  readonly tasks: ReadonlyArray<RelatedTask>;
  readonly className?: string | undefined;
}

const STATUS_WORD: Partial<Record<TaskStatus, string>> = {
  review: "in review", running: "in progress", awaiting_input: "needs a person", ready_to_merge: "ready to merge",
  failed: "failed", aborted: "aborted", received: "not started", queued: "not started", intake: "being shaped", awaiting_confirmation: "plan to confirm",
};
const word = (s: TaskStatus) => STATUS_WORD[s] ?? s.replace(/_/g, " ");
/** A task's state in the words a blocker is named with: "in review", "done, nothing merged". */
export const stateWord = (t: Pick<RelatedTask, "status" | "unmerged">) => (t.unmerged ? "done, nothing merged" : word(t.status));

/** "CP-41 and CP-43", "CP-40, CP-41 and CP-43". */
export const keyList = (keys: ReadonlyArray<string>) => (keys.length > 1 ? `${keys.slice(0, -1).join(", ")} and ${keys.at(-1)}` : (keys[0] ?? ""));

/** "Blocked by CP-41 (in review) and CP-43 (in progress)": the chip's name, so it is never a glyph alone. */
export function relationWords(kind: RelationChipKind, tasks: ReadonlyArray<RelatedTask>): string {
  return `${kind === "blocked-by" ? "Blocked by" : "Blocks"} ${keyList(tasks.map((t) => `${t.key} (${stateWord(t)})`))}`;
}

/** "it can start once that's merged" / "once they're merged": the one sentence every refusal ends with. */
export const onceMerged = (n: number) => `it can start once ${n === 1 ? "that's" : "they're"} merged`;

/**
 * On a board card, between the title and its people: why it cannot start,
 * or what waits on it. "Blocked by" sits on the neutral wash with the lock;
 * "Blocks" is quieter, words and glyph only. One blocker is named with its
 * status mark, so the chain reads on the card ("Blocked by ◐ CP-41"); more
 * are counted. The tooltip lists each. Neutral, never the attention tone:
 * a blocked task waits on work, not on a person.
 */
export function RelationChip({ kind, tasks, className }: RelationChipProps) {
  if (tasks.length === 0) return null;
  const one = tasks.length === 1 ? tasks[0]! : null;
  const words = relationWords(kind, tasks);
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
              {t.unmerged ? <span className={styles["tipProject"]}>nothing merged</span> : null}
              {t.project ? <span className={styles["tipProject"]}>{t.project}</span> : null}
            </span>
          ))}
        </span>
      }
    >
      <span
        className={[styles["chip"], blocked ? styles["chipBlocked"] : styles["chipBlocks"], className].filter(Boolean).join(" ")}
        data-relation={kind}
        role="img"
        aria-label={words}
        tabIndex={-1}
      >
        <RelationGlyph name={blocked ? "lock" : "blocks"} />
        <span className="ds-cap">{blocked ? "Blocked by" : "Blocks"}</span>
        {one ? (
          <>
            {/* Done with nothing merged takes no status mark: its check would read as released. */}
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
// The task's Relations
// ---------------------------------------------------------------------------

export interface RelationRowData extends RelatedTask {
  readonly owner: Person;
  readonly agent?: AgentRole | undefined;
  /** A blocker that is merged: shown settled. */
  readonly settled?: boolean | undefined;
  /**
   * Who may remove this edge when the reader may not: "Only Márcio, the
   * owner, can remove this". Absent, the row has its remove.
   */
  readonly removeHint?: string | undefined;
}

export interface RelationGroupData {
  readonly kind: "blocked-by" | "blocks" | "related";
  readonly rows: ReadonlyArray<RelationRowData>;
}

const GROUP_LABEL: Record<RelationGroupData["kind"], string> = { "blocked-by": "Blocked by", blocks: "Blocks", related: "Related" };
const GROUP_GLYPH: Record<RelationGroupData["kind"], RelationGlyphName> = { "blocked-by": "lock", blocks: "blocks", related: "link" };

export interface RelationListProps {
  readonly groups: ReadonlyArray<RelationGroupData>;
  /** Given, each row the reader may change has a quiet remove, and the head an Add. */
  readonly onRemove?: ((group: RelationGroupData["kind"], key: string) => void) | undefined;
  readonly onAdd?: (() => void) | undefined;
  readonly onOpen?: ((key: string) => void) | undefined;
  /** The add form, open under the head (a `RelationPicker`). */
  readonly adding?: ReactNode;
  /** The row whose hint shows from the start, for a static picture of hovering it. */
  readonly hinting?: string | undefined;
}

/**
 * A task's relations, three groups under small-caps heads — Blocked by,
 * Blocks, Related — each row in RemovableList's grammar: the task's status
 * mark and key, its title, its status in words, its owner (with the agent
 * at work on their face), a quiet remove. A group with nothing in it is
 * left out; with no relations at all, one line says so.
 *
 * A row the reader may not remove has no remove. Hovering or focusing it
 * shows who may in the group's head, where the line has room, so nothing
 * in the row moves; the same words are the row's description.
 */
export function RelationList({ groups, onRemove, onAdd, onOpen, adding, hinting }: RelationListProps) {
  const shown = groups.filter((g) => g.rows.length > 0);
  const headId = useId();
  const [hovered, setHovered] = useState<string | null>(hinting ?? null);
  return (
    <section className={styles["relations"]} aria-labelledby={headId}>
      <header className={styles["relationsHead"]}>
        <h2 id={headId} className="ds-label">Relations</h2>
        {onAdd && !adding ? <Button variant="quiet" size="sm" leadingIcon="plus" onClick={onAdd}>Add relation</Button> : null}
      </header>
      {adding}
      {shown.length === 0 && !adding ? <p className={styles["muted"]}>No relations. A task that must wait for another says so here, and can't start before it is merged.</p> : null}
      {shown.map((g) => {
        const open = g.kind === "blocked-by" ? g.rows.filter((r) => !r.settled).length : null;
        const hint = g.rows.find((r) => `${g.kind}:${r.key}` === hovered)?.removeHint;
        return (
          <div key={g.kind} className={styles["group"]} role="group" aria-label={GROUP_LABEL[g.kind]}>
            <div className={styles["groupHead"]}>
              <RelationGlyph name={GROUP_GLYPH[g.kind]} size={12} />
              <span>{GROUP_LABEL[g.kind]}</span>
              <span className={styles["groupCount"]}>
                {open !== null && open < g.rows.length ? `${open} of ${g.rows.length} open` : g.rows.length}
              </span>
              <span className={[styles["removeHint"], hint ? styles["removeHintOn"] : ""].join(" ")} aria-hidden data-testid={hint ? "remove-hint" : undefined}>
                {hint ?? ""}
              </span>
            </div>
            <ul className={styles["rows"]}>
              {g.rows.map((r) => {
                const id = `${g.kind}:${r.key}`;
                const hintId = `${headId}-${g.kind}-${r.key}`;
                const on = () => setHovered(id);
                const off = () => setHovered((h) => (h === id ? null : h));
                return (
                  <li key={r.key} className={[styles["row"], r.settled ? styles["settled"] : ""].join(" ")} data-key={r.key}
                    onMouseEnter={r.removeHint ? on : undefined} onMouseLeave={r.removeHint ? off : undefined}
                    onFocus={r.removeHint ? on : undefined} onBlur={r.removeHint ? off : undefined}>
                    <button type="button" className={styles["rowOpen"]} onClick={() => onOpen?.(r.key)} aria-label={`${r.key} ${r.title}, ${stateWord(r)}`}
                      aria-describedby={r.removeHint ? hintId : undefined}>
                      <RefLead type="task" taskKey={r.key} status={r.status} />
                      <span className={styles["rowTitle"]}>{r.title}</span>
                      {r.project ? <span className={styles["rowProject"]}>{r.project}</span> : null}
                    </button>
                    {r.removeHint ? <span id={hintId} className="ds-sr-only">{r.removeHint}</span> : null}
                    {r.unmerged ? <span className={styles["rowUnmerged"]}>nothing merged</span> : null}
                    <StatusMark status={r.status} size="sm" className={styles["rowStatus"]} />
                    <PersonAvatar person={r.owner} size={20} agent={r.agent} live={r.agent ? true : undefined} title={r.agent ? `${r.owner.name}'s ${r.agent} is working` : r.owner.name} />
                    {onRemove && !r.removeHint ? (
                      <IconButton size="sm" icon="close" label={`Remove ${GROUP_LABEL[g.kind].toLowerCase()} ${r.key}`} onClick={() => onRemove(g.kind, r.key)} />
                    ) : onRemove ? <span className={styles["removeSlot"]} aria-hidden /> : null}
                  </li>
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
// The start, refused
// ---------------------------------------------------------------------------

export interface StartGateProps {
  /** The blockers still open, in the words the reason uses. */
  readonly blockers: ReadonlyArray<RelatedTask>;
  /** The ways to start, each refused for the same reason. */
  readonly starts: ReadonlyArray<{ readonly id: string; readonly label: string; readonly icon: "message" | "zap" }>;
  /** A person may override; an agent never sees this. */
  readonly onStartAnyway?: (() => void) | undefined;
  /** The owner only: opens the choice of how it starts once unblocked. Absent once set: the notice under the header says so. */
  readonly onStartWhenUnblocked?: (() => void) | undefined;
}

/**
 * Where Deliver and Talk it through sit, on a task that is blocked: both
 * stay, disabled — never gone — with the reason in their tooltip and in
 * their description (`aria-describedby`). After them, for its owner, "Start
 * when unblocked…" (once set, the notice under the header carries
 * `AutoStartMark` with its Cancel instead, so the header keeps its room for
 * the path); then a quiet "Start anyway…" that opens the confirmation
 * naming the blockers.
 */
export function StartGate({ blockers, starts, onStartAnyway, onStartWhenUnblocked }: StartGateProps) {
  const reasonId = useId();
  const reason = `${relationWords("blocked-by", blockers)}: ${onceMerged(blockers.length)}.`;
  return (
    <span className={styles["gate"]} data-testid="start-gate">
      <span id={reasonId} className="ds-sr-only">{reason}</span>
      {starts.map((s) => (
        <Tooltip key={s.id} content={reason} side="bottom">
          {/* A focusable wrapper: a disabled button takes no pointer or focus, so its tooltip would never open. */}
          <span className={styles["gateTrigger"]} tabIndex={0} aria-describedby={reasonId}>
            <Button variant="secondary" leadingIcon={s.icon} disabled aria-describedby={reasonId}>
              {s.label}
            </Button>
          </span>
        </Tooltip>
      ))}
      {onStartWhenUnblocked ? (
        <Button variant="quiet" leadingIcon="play" onClick={onStartWhenUnblocked} data-testid="start-when-unblocked">
          Start when unblocked…
        </Button>
      ) : null}
      {onStartAnyway ? (
        <Button variant="quiet" onClick={onStartAnyway} data-testid="start-anyway">
          Start anyway…
        </Button>
      ) : null}
    </span>
  );
}

// ---------------------------------------------------------------------------
// Start when unblocked: how it reads once set
// ---------------------------------------------------------------------------

export interface AutoStartView {
  readonly how: "deliver" | "talk";
  /** Who set it, by first name, and how long ago. */
  readonly by: string;
  readonly ago: string;
  /** The owner only: takes it back. Absent, the mark is read only. */
  readonly onCancel?: (() => void) | undefined;
}

const HOW: Record<AutoStartView["how"], string> = { deliver: "Deliver", talk: "Talk it through" };

/** "Starts when CP-41 merges", "Starts when CP-41 and CP-43 merge". */
export const startsWhen = (blockers: ReadonlyArray<{ readonly key: string }>) =>
  `Starts when ${keyList(blockers.map((b) => b.key))} ${blockers.length === 1 ? "merges" : "merge"}`;

/**
 * A blocked task set to start on its own: the play glyph, "Starts when
 * CP-41 merges · Deliver", who set it in the tooltip, and for the owner a
 * quiet Cancel. On a card (`compact`) it is one muted line with no Cancel:
 * the card's start menu has it.
 */
export function AutoStartMark({ auto, blockers, compact }: { readonly auto: AutoStartView; readonly blockers: ReadonlyArray<RelatedTask>; readonly compact?: boolean | undefined }) {
  const words = `${startsWhen(blockers)} · ${HOW[auto.how]}`;
  const who = `${auto.by} set it ${auto.ago} ago. Every blocker is checked again when it fires: if one is open, nothing starts.`;
  const mark = (
    <span className={[styles["auto"], compact ? styles["autoCompact"] : ""].join(" ")} data-testid="auto-start" tabIndex={compact ? -1 : 0}
      aria-label={`${words}. ${who}`}>
      <Icon name="play" size={12} className={styles["glyph"]} />
      <span className={styles["autoWords"]}>{words}</span>
    </span>
  );
  return (
    <span className={styles["autoWrap"]}>
      <Tooltip content={who} side="bottom">{mark}</Tooltip>
      {!compact ? <span className={styles["autoWho"]} aria-hidden>{auto.by} set it {auto.ago} ago</span> : null}
      {!compact && auto.onCancel ? (
        <Button variant="quiet" size="sm" onClick={auto.onCancel} data-testid="cancel-auto-start" aria-label={`Cancel: ${words}`}>Cancel</Button>
      ) : null}
    </span>
  );
}

// ---------------------------------------------------------------------------
// The card's start
// ---------------------------------------------------------------------------

/** The menu's trigger with the reason as its tooltip: one element takes both slots' props and ref. */
const StartTrigger = forwardRef<HTMLButtonElement, ButtonProps & { readonly tip: string | null }>(function StartTrigger({ tip, ...rest }, ref) {
  const button = <Button ref={ref} {...rest} />;
  return tip ? <Tooltip content={tip} side="bottom">{button}</Tooltip> : button;
});

export interface StartMenuProps {
  /** The task, by key: names the trigger ("Start CP-44"). */
  readonly taskKey: string;
  /** Open blockers; none, the start works. */
  readonly blockers: ReadonlyArray<RelatedTask>;
  readonly onStart: (how: "deliver" | "talk") => void;
  /** A person may: the same dialog as the task page's. */
  readonly onStartAnyway?: (() => void) | undefined;
  readonly auto?: AutoStartView | undefined;
  /** The owner sets it; anyone else sees the item disabled, saying whose it is. */
  readonly onStartWhenUnblocked?: (() => void) | undefined;
  /** Why it can't be set even by the owner: a blocker that will never merge. */
  readonly startWhenUnblockedRefused?: string | null | undefined;
  /** The owner's first name, for the reason the item is someone else's. */
  readonly ownerName: string;
  readonly open?: boolean | undefined;
  readonly onOpenChange?: ((open: boolean) => void) | undefined;
  readonly tabIndex?: number | undefined;
}

/**
 * A Backlog card's start: "Start" with a chevron, opening `RowMenu` with
 * Talk it through and Deliver side by side, neither first by default
 * (starting is a choice, so no split button: a split button's face is one
 * way preselected). Blocked, the trigger carries the lock and the reason
 * as its tooltip, both ways are disabled items with the reason, and the
 * menu still offers Start when unblocked… and Start anyway…, which is why
 * the trigger itself stays operable.
 */
export function StartMenu({ taskKey, blockers, onStart, onStartAnyway, auto, onStartWhenUnblocked, startWhenUnblockedRefused, ownerName, open, onOpenChange, tabIndex }: StartMenuProps) {
  const blocked = blockers.length > 0;
  const reason = blocked ? `${relationWords("blocked-by", blockers)}: ${onceMerged(blockers.length)}.` : null;
  const ways: RowMenuItem[] = [
    { id: "talk", label: "Talk it through", icon: "message", description: "The conductor plans with you first", disabled: blocked, disabledReason: reason ?? undefined, onSelect: () => onStart("talk") },
    { id: "deliver", label: "Deliver", icon: "zap", description: "The automatic pipeline", disabled: blocked, disabledReason: reason ?? undefined, onSelect: () => onStart("deliver") },
  ];
  const around: RowMenuItem[] = !blocked ? [] : [
    { kind: "separator" },
    ...(auto ? [
      { kind: "note" as const, id: "auto-note", text: `${startsWhen(blockers)} · ${HOW[auto.how]}, as ${auto.by} set ${auto.ago} ago.` },
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
    <RowMenu items={[...ways, ...around]} align="end" open={open} onOpenChange={onOpenChange}
      trigger={
        <StartTrigger tip={reason} size="sm" variant="quiet" tabIndex={tabIndex}
          className={[styles["startTrigger"], blocked ? styles["startBlocked"] : ""].join(" ")}
          aria-label={blocked ? `Start ${taskKey}: ${reason}` : `Start ${taskKey}`} data-testid={`start-menu-${taskKey}`}>
          {blocked ? <RelationGlyph name="lock" size={12} /> : <Icon name="play" size={12} />}
          <span>Start</span>
          <Icon name="chevron-down" size={12} />
        </StartTrigger>
      } />
  );
}

// ---------------------------------------------------------------------------
// The epic's order
// ---------------------------------------------------------------------------

export interface StripTask extends RelatedTask {
  /** Not started, nothing open in front of it. */
  readonly startable: boolean;
  readonly blockedBy: ReadonlyArray<string>;
}

export interface DependencyStripProps {
  /** Steps in order: each step's tasks wait only on earlier steps'. */
  readonly steps: ReadonlyArray<ReadonlyArray<StripTask>>;
  readonly counts: { readonly startable: number; readonly blocked: number; readonly working: number; readonly merged: number; readonly doneUnmerged: number; readonly failed: number };
  readonly onOpen?: ((key: string) => void) | undefined;
  /** Highlighted (hovered or focused): this key and its neighbours stay, the rest dims. */
  readonly highlight?: ReadonlySet<string> | null | undefined;
}

/**
 * An epic's tasks in the order their relations put them: steps left to
 * right, each a column of tasks that wait only on the steps before. A
 * task is its status mark and key; one that can start now carries a
 * "can start" word, one that cannot the lock. Above it, the counts in
 * words. Not a graph: the steps are the order, the tooltip the edges.
 */
export function DependencyStrip({ steps, counts, onOpen, highlight }: DependencyStripProps) {
  const parts: ReactNode[] = [];
  const say = (n: number, w: string) => (n > 0 ? <span key={w}><b className={styles["stripNum"]}>{n}</b> {w}</span> : null);
  parts.push(say(counts.startable, "can start"), say(counts.blocked, "blocked"), say(counts.working, "in progress or review"), say(counts.merged, "merged"), say(counts.doneUnmerged, "done, nothing merged"), say(counts.failed, "failed"));
  return (
    <section className={styles["strip"]} aria-label="Order of work">
      <div className={styles["stripHead"]}>
        <span className="ds-label">Order</span>
        <span className={styles["stripCounts"]}>{parts.filter(Boolean).flatMap((p, i) => (i ? [<span key={`d${i}`} className={styles["dot"]} aria-hidden>·</span>, p] : [p]))}</span>
      </div>
      <ol className={styles["steps"]}>
        {steps.map((tasks, i) => (
          <li key={i} className={styles["step"]}>
            <span className={styles["stepNo"]} aria-label={`Step ${i + 1}`}>{i + 1}</span>
            <ul className={styles["stepTasks"]}>
              {tasks.map((t) => {
                const dim = highlight && !highlight.has(t.key);
                const name = `${t.key} ${t.title}, ${word(t.status)}${t.startable ? ", can start" : t.blockedBy.length ? `, blocked by ${t.blockedBy.join(" and ")}` : ""}`;
                return (
                  <li key={t.key}>
                    <Tooltip side="bottom" content={<span className={styles["tip"]}><span className={styles["tipTitle"]}>{t.title}</span>{t.blockedBy.length ? <span className={styles["tipHead"]}>Waits on {t.blockedBy.join(", ")}</span> : null}</span>}>
                      <button type="button" className={[styles["stripTask"], dim ? styles["dim"] : "", t.startable ? styles["startable"] : ""].join(" ")} onClick={() => onOpen?.(t.key)} aria-label={name} data-key={t.key}>
                        <RefLead type="task" taskKey={t.key} status={t.status} />
                        {t.startable ? <span className={styles["canStart"]}>can start</span> : t.blockedBy.length ? <RelationGlyph name="lock" size={12} className={styles["stripLock"]} /> : null}
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

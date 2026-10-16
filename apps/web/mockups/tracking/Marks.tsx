/*
 * The design-system pieces this proposal adds, drawn here and specified in
 * docs/design/tracking-work.md: KindMark, PriorityMark, ResolutionMark,
 * LaterMark, LaterGate (the start refused for the shelf), IntroducedByChip,
 * TaskFilterBar and TaskRow. Built from the real primitives (Icon, Badge,
 * Button, Tooltip, RowMenu) and tokens only; nothing in
 * packages/design-system is changed.
 */

import { useId, type ReactNode } from "react";
import { Icon, type IconName } from "@dude/design-system";
import { PersonAvatar, RefLead, StatusMark } from "@dude/design-system/components";
import { Badge, Button, RowMenu, Tooltip, type RowMenuItem } from "@dude/design-system/primitives";
import type { Person } from "../../../../packages/design-system/src/components/PersonAvatar.tsx";
import { KIND, PRIORITY, RESOLUTION, type Kind, type Priority, type Resolution } from "./data.ts";
import styles from "./marks.module.css";

// ---------------------------------------------------------------------------
// KindMark: what kind of work, as a glyph with its word in the tooltip.
// ---------------------------------------------------------------------------

/** Each kind's glyph from the existing set; a feature, the default, draws none on cards and rows. */
const KIND_GLYPH: Record<Kind, IconName> = { bug: "alert", feature: "sparkle", chore: "wrench", spike: "question" };

export interface KindMarkProps {
  readonly kind: Kind;
  /** `word` puts the label after the glyph (the task header, the dialog); `glyph` alone on cards and rows. */
  readonly variant?: "glyph" | "word" | undefined;
  /** Cards and rows: a feature says nothing, as P2 says nothing. */
  readonly quietDefault?: boolean | undefined;
}

export function KindMark({ kind, variant = "glyph", quietDefault }: KindMarkProps) {
  if (quietDefault && kind === "feature") return null;
  const label = KIND[kind].label;
  if (variant === "word") {
    return (
      <span className={styles["kind"]} data-kind={kind} data-testid="kind-mark">
        <Icon name={KIND_GLYPH[kind]} size={14} />
        <span>{label}</span>
      </span>
    );
  }
  return (
    <Tooltip content={label}>
      <span className={styles["kind"]} data-kind={kind} role="img" aria-label={label} tabIndex={-1} data-testid="kind-mark">
        <Icon name={KIND_GLYPH[kind]} size={13} />
      </span>
    </Tooltip>
  );
}

// ---------------------------------------------------------------------------
// PriorityMark: P0 and P1 in strong ink, P2 silent, P3 muted.
// ---------------------------------------------------------------------------

export interface PriorityMarkProps {
  readonly priority: Priority;
  /** The header and the dialog say P2 too; cards and rows don't. */
  readonly showDefault?: boolean | undefined;
}

export function PriorityMark({ priority, showDefault }: PriorityMarkProps) {
  if (priority === "P2" && !showDefault) return null;
  return (
    <Tooltip content={`${priority} · ${PRIORITY[priority].line}`}>
      <span className={styles["priority"]} data-priority={priority} tabIndex={-1} aria-label={`Priority ${priority}`} data-testid="priority-mark">
        {priority === "P0" ? <Icon name="arrow-up" size={12} /> : null}
        {priority}
      </span>
    </Tooltip>
  );
}

// ---------------------------------------------------------------------------
// ResolutionMark: why a person closed it, beside the status.
// ---------------------------------------------------------------------------

export function ResolutionMark({ resolution, of }: { readonly resolution: Resolution; readonly of?: string | undefined }) {
  return (
    <Badge tone="neutral" size="sm" data-testid="resolution">
      {RESOLUTION[resolution].label}{of ? <> of <span className={styles["mono"]}>{of}</span></> : null}
    </Badge>
  );
}

// ---------------------------------------------------------------------------
// LaterMark and ArchivedMark: neutral, never a tone. Waiting by choice needs nobody.
// ---------------------------------------------------------------------------

export function LaterMark({ until, compact }: { readonly until: string | null; readonly compact?: boolean | undefined }) {
  const words = until ? `Later · ${until}` : "Later";
  return (
    <span className={styles["later"]} data-testid="later-mark" title={until ? `On the Later shelf until ${until}` : "On the Later shelf until someone brings it back"}>
      <Icon name="clock" size={12} />
      {compact ? (until ?? "Later") : words}
    </span>
  );
}

export function ArchivedMark() {
  return (
    <span className={styles["later"]} data-testid="archived-mark">
      <Icon name="archive" size={12} />
      Archived
    </span>
  );
}

// ---------------------------------------------------------------------------
// LaterGate: the start of a task on the shelf, refused with its reason, and
// the one way out. StartGate's shape: the ways stay, disabled, reachable.
// ---------------------------------------------------------------------------

export interface LaterGateProps {
  readonly taskKey: string;
  /** "Later until 3 Nov", or the epic's shelf. */
  readonly reason: string;
  readonly starts: ReadonlyArray<{ readonly id: string; readonly label: string; readonly icon: IconName }>;
  /** Bring it back, for whoever may; absent with `bringBackHint` for a reader who may not. */
  readonly onBringBack?: (() => void) | undefined;
  readonly bringBackHint?: string | undefined;
  readonly size?: "sm" | "md" | undefined;
}

export function LaterGate({ reason, starts, onBringBack, bringBackHint, size = "md" }: LaterGateProps) {
  const reasonId = useId();
  const hintId = useId();
  return (
    <span className={styles["gate"]} data-testid="later-gate">
      <span id={reasonId} className="ds-sr-only">{reason}</span>
      {starts.map((s) => (
        <Tooltip key={s.id} content={reason} side="bottom">
          <span className={styles["gateTrigger"]} tabIndex={0} role="button" aria-disabled="true" aria-label={s.label} aria-describedby={reasonId} data-testid={`later-${s.id}`} data-start-control="">
            <Button variant="secondary" size={size} leadingIcon={s.icon} disabled>{s.label}</Button>
          </span>
        </Tooltip>
      ))}
      {onBringBack ? (
        <Button variant={size === "sm" ? "quiet" : "secondary"} size={size} leadingIcon="arrow-up" onClick={onBringBack} data-testid="bring-back" data-lifecycle-action="">Bring it back</Button>
      ) : (
        <Tooltip content={bringBackHint ?? ""} side="bottom">
          <span className={styles["gateTrigger"]} tabIndex={0} role="button" aria-disabled="true" aria-label="Bring it back" aria-describedby={hintId} data-lifecycle-action="">
            <span id={hintId} className="ds-sr-only" data-lifecycle-hint="">{bringBackHint}</span>
            <Button variant="quiet" size={size} leadingIcon="arrow-up" disabled>Bring it back</Button>
          </span>
        </Tooltip>
      )}
    </span>
  );
}

// ---------------------------------------------------------------------------
// IntroducedByChip: the change that caused a bug, proposed or confirmed.
// ---------------------------------------------------------------------------

export interface IntroducedByChipProps {
  readonly taskKey: string;
  readonly status: "done";
  readonly title: string;
  readonly merged: string;
  readonly state: "proposed" | "confirmed";
  readonly by: string;
  /** The owner, on a proposal: keep it as the cause, or clear it. */
  readonly onConfirm?: (() => void) | undefined;
  readonly onClear?: (() => void) | undefined;
  /** For anyone else: whose call it is. */
  readonly hint?: string | undefined;
}

export function IntroducedByChip({ taskKey, title, merged, state, by, onConfirm, onClear, hint }: IntroducedByChipProps) {
  return (
    <span className={styles["introduced"]} data-state={state} data-testid="introduced-by">
      <span className={styles["introducedChip"]}>
        <RefLead type="task" taskKey={taskKey} status="done" />
        <span className={styles["introducedWords"]}>
          {state === "proposed" ? "Likely " : ""}{taskKey} · merged {merged}
        </span>
      </span>
      <span className={styles["introducedTitle"]} title={title}>{title}</span>
      <span className={styles["introducedBy"]}>{state === "proposed" ? `Proposed by ${by}` : `Confirmed by ${by}`}</span>
      {state === "proposed" && onConfirm ? (
        <span className={styles["introducedActions"]}>
          <Button size="sm" variant="secondary" leadingIcon="check" onClick={onConfirm} data-testid="confirm-introduced">Confirm</Button>
          <Button size="sm" variant="quiet" onClick={onClear} data-testid="clear-introduced">Clear</Button>
        </span>
      ) : state === "proposed" && hint ? (
        <span className={styles["introducedBy"]}>{hint}</span>
      ) : null}
    </span>
  );
}

// ---------------------------------------------------------------------------
// TaskFilterBar: the board's and the list's chips. Each chip is a RowMenu
// of radio items; a set chip says its value and has its own ✕.
// ---------------------------------------------------------------------------

export interface FilterChip {
  readonly id: string;
  readonly label: string;
  /** Set: "Kind: Bug". Unset: the label alone, quiet. */
  readonly value?: string | undefined;
  readonly items?: ReadonlyArray<RowMenuItem> | undefined;
  readonly onClear?: (() => void) | undefined;
  /** A toggle chip (Mine): pressed or not, no menu. */
  readonly pressed?: boolean | undefined;
  readonly onToggle?: (() => void) | undefined;
}

export function TaskFilterBar({ chips, children, label = "Filter tasks" }: { readonly chips: ReadonlyArray<FilterChip>; readonly children?: ReactNode; readonly label?: string }) {
  return (
    <div className={styles["filters"]} role="group" aria-label={label} data-testid="filter-bar">
      {children}
      {chips.map((c) => c.onToggle ? (
        <button key={c.id} type="button" className={styles["chip"]} aria-pressed={c.pressed} data-set={c.pressed || undefined} onClick={c.onToggle} data-testid={`chip-${c.id}`}>
          {c.label}
        </button>
      ) : (
        <span key={c.id} className={styles["chipWrap"]}>
          <RowMenu label={`${c.label} filter`} align="start" items={c.items ?? []}
            trigger={
              <button type="button" className={styles["chip"]} data-set={c.value ? "" : undefined} data-testid={`chip-${c.id}`}>
                {c.value ? <>{c.label}: <b>{c.value}</b></> : c.label}
                <Icon name="chevron-down" size={12} />
              </button>
            } />
          {c.value && c.onClear ? (
            <button type="button" className={styles["chipClear"]} aria-label={`Clear ${c.label.toLowerCase()} filter`} onClick={c.onClear}>
              <Icon name="close" size={12} />
            </button>
          ) : null}
        </span>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// TaskRow: one dense row of the tasks list.
// ---------------------------------------------------------------------------

export interface TaskRowData {
  readonly key: string;
  readonly title: string;
  readonly status: import("@dude/domain").TaskStatus;
  readonly kind: Kind;
  readonly priority: Priority;
  readonly epic?: string | undefined;
  readonly owner: Person;
  readonly age: string;
  readonly later?: string | null | undefined;
  readonly onShelf?: boolean | undefined;
  readonly archived?: boolean | undefined;
  readonly resolution?: Resolution | undefined;
  readonly duplicateOf?: string | undefined;
}

export function TaskRow({ row, menu, onOpen }: { readonly row: TaskRowData; readonly menu?: ReactNode; readonly onOpen?: (key: string) => void }) {
  return (
    <li className={styles["row"]} data-archived={row.archived || undefined} data-key={row.key}>
      <button type="button" className={styles["rowOpen"]} onClick={() => onOpen?.(row.key)} aria-label={`${row.key} ${row.title}`}>
        <span className={styles["rowLead"]}>
          <StatusMark status={row.status} size="sm" />
          <span className={styles["mono"]}>{row.key}</span>
        </span>
        <span className={styles["rowKind"]}><KindMark kind={row.kind} quietDefault /></span>
        <span className={styles["rowTitle"]}>{row.title}</span>
        <span className={styles["rowFacts"]}>
          {row.resolution ? <ResolutionMark resolution={row.resolution} of={row.duplicateOf} /> : null}
          {row.onShelf ? <LaterMark until={row.later ?? null} /> : null}
          {row.archived ? <ArchivedMark /> : null}
        </span>
        <span className={styles["rowEpic"]}>{row.epic ?? ""}</span>
        <span className={styles["rowPriority"]}><PriorityMark priority={row.priority} /></span>
        <span className={styles["rowOwner"]}><PersonAvatar person={row.owner} size={20} /></span>
        <span className={styles["rowAge"]}>{row.age}</span>
      </button>
      <span className={styles["rowMenu"]}>{menu}</span>
    </li>
  );
}

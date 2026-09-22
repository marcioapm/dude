import { useState, type HTMLAttributes, type KeyboardEvent, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { formatTimestamp } from "../util/format.ts";
import type { ActorType } from "../tokens/status.ts";
import type { AgentRoleName } from "../tokens/palette.ts";
import { AgentAvatar, type AvatarKind } from "./AgentAvatar.tsx";
import styles from "./EventRow.module.css";

export type EventSeverity = "normal" | "success" | "attention" | "danger";

export interface EventActor {
  readonly type: ActorType;
  /** Agent role when `type === "agent"`. */
  readonly role?: AgentRoleName | undefined;
  readonly id?: string | undefined;
  readonly name?: string | undefined;
}

export interface EventRowProps extends Omit<HTMLAttributes<HTMLDivElement>, "children" | "title"> {
  readonly occurredAt: string | number | Date;
  readonly actor: EventActor;
  /** Dotted type, e.g. `session.tool_call.completed`. Shown in mono. */
  readonly eventType: string;
  readonly summary: ReactNode;
  readonly severity?: EventSeverity | undefined;
  /** Right-aligned extras: duration, cost, a small badge. */
  readonly trailing?: ReactNode;
  /** Expandable detail. If provided, the row gets a chevron and toggles. */
  readonly detail?: ReactNode;
  /** Key/value pairs shown at the top of the detail area. */
  readonly meta?: ReadonlyArray<readonly [string, ReactNode]> | undefined;
  readonly expanded?: boolean | undefined;
  readonly defaultExpanded?: boolean | undefined;
  readonly onExpandedChange?: ((expanded: boolean) => void) | undefined;
  readonly compact?: boolean | undefined;
  /** Flash on mount: for rows that just arrived over the stream. */
  readonly isNew?: boolean | undefined;
}

const SEV_CLASS: Record<EventSeverity, string | undefined> = {
  normal: undefined,
  success: styles["sevSuccess"],
  attention: styles["sevAttention"],
  danger: styles["sevDanger"],
};

/**
 * One line in the event ledger. Fixed columns so hundreds of rows align:
 * time | actor | type | summary | trailing. Only attention/danger/success
 * events get a left stripe, so the eye can skim for them; everything else
 * is quiet.
 */
export function EventRow({
  occurredAt,
  actor,
  eventType,
  summary,
  severity = "normal",
  trailing,
  detail,
  meta,
  expanded,
  defaultExpanded,
  onExpandedChange,
  compact,
  isNew,
  className,
  onClick,
  ...rest
}: EventRowProps) {
  const [internal, setInternal] = useState(defaultExpanded ?? false);
  const isControlled = expanded !== undefined;
  const open = isControlled ? expanded : internal;
  const expandable = detail !== undefined || (meta !== undefined && meta.length > 0);

  const toggle = () => {
    if (!expandable) return;
    const next = !open;
    if (!isControlled) setInternal(next);
    onExpandedChange?.(next);
  };
  const onKey = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      toggle();
    }
  };

  const avatarRole: AvatarKind = actor.type === "agent" ? (actor.role ?? "implementer") : actor.type;
  const ts = new Date(occurredAt);

  return (
    <>
      <div
        className={cx(styles["row"], SEV_CLASS[severity], compact && styles["compact"], open && styles["rowExpanded"], isNew && styles["rowNew"], className)}
        role={expandable ? "button" : undefined}
        tabIndex={expandable ? 0 : undefined}
        aria-expanded={expandable ? open : undefined}
        onClick={(e) => {
          onClick?.(e);
          toggle();
        }}
        onKeyDown={expandable ? onKey : undefined}
        data-severity={severity}
        data-event-type={eventType}
        {...rest}
      >
        <time className={styles["time"]} dateTime={ts.toISOString()} title={ts.toISOString()}>
          {formatTimestamp(ts, "time-ms")}
        </time>
        <span className={styles["actor"]}>
          <AgentAvatar role={avatarRole} size="xs" {...(actor.name ? { title: actor.name } : {})} />
        </span>
        <span className={styles["type"]} title={eventType}>
          {eventType}
        </span>
        <span className={styles["summary"]}>{summary}</span>
        <span className={styles["trailing"]}>
          {trailing}
          {expandable ? (
            <Icon name="chevron-right" size={12} className={styles["chevron"]} />
          ) : (
            <span className={styles["chevronSpacer"]} />
          )}
        </span>
      </div>
      {expandable && open ? (
        <div className={cx(styles["detail"], SEV_CLASS[severity])}>
          {meta && meta.length > 0 ? (
            <div className={styles["detailMeta"]}>
              {meta.map(([k, v]) => (
                <span key={k}>
                  <b>{k}</b> {v}
                </span>
              ))}
            </div>
          ) : null}
          {detail}
        </div>
      ) : null}
    </>
  );
}

export interface EventStreamProps extends HTMLAttributes<HTMLDivElement> {
  /** Show the column header row. */
  readonly header?: boolean | undefined;
}

/** Container that gives EventRows their column header. */
export function EventStream({ header = true, className, children, ...rest }: EventStreamProps) {
  return (
    <div className={cx(styles["stream"], className)} role="log" aria-live="polite" aria-relevant="additions" {...rest}>
      {header ? (
        <div className={styles["streamHeader"]} aria-hidden>
          <span>Time</span>
          <span />
          <span>Event</span>
          <span>Summary</span>
          <span />
        </div>
      ) : null}
      {children}
    </div>
  );
}

/** A date divider between days in a long stream. */
export function EventDayDivider({ date }: { readonly date: string | number | Date }) {
  return <div className={styles["dayDivider"]}>{formatTimestamp(date, "date")}</div>;
}

import { useId, type HTMLAttributes } from "react";
import type { AgentRole } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { formatTimestamp } from "../util/format.ts";
import { useDisclosure } from "../util/useDisclosure.ts";
import { AgentAvatar, ROLE_LABEL } from "./AgentAvatar.tsx";
import { prettyJson } from "./ArtifactPreview.tsx";
import styles from "./ChatEvent.module.css";

export interface ChatEventProps extends Omit<HTMLAttributes<HTMLDivElement>, "children"> {
  /** The event's type as the agent named it: `tests.finished`, `coverage`. */
  readonly type: string;
  readonly data: unknown;
  readonly at: string | number | Date;
  /** Who recorded it. */
  readonly role: AgentRole;
  readonly expanded?: boolean | undefined;
  readonly defaultExpanded?: boolean | undefined;
  readonly onExpandedChange?: ((open: boolean) => void) | undefined;
}

/** How many `key=value` pairs a summary shows before "…". */
export const EVENT_SUMMARY_PAIRS = 3;

/**
 * One line for the data: a scalar as-is, an object as up to three
 * `key=value` pairs then "…", an array by its length. Values inside are
 * kept short: strings unquoted, nested things as `{…}` / `[n]`.
 */
export function summarizeEventData(data: unknown, maxPairs = EVENT_SUMMARY_PAIRS): string {
  if (data === undefined) return "";
  if (data === null || typeof data !== "object") return scalar(data);
  if (Array.isArray(data)) return `[${data.length}]`;
  const entries = Object.entries(data as Record<string, unknown>);
  if (entries.length === 0) return "{}";
  const shown = entries.slice(0, maxPairs).map(([k, v]) => `${k}=${brief(v)}`);
  return entries.length > maxPairs ? `${shown.join(" ")} …` : shown.join(" ");
}

function scalar(v: unknown): string {
  if (typeof v === "string") return v;
  if (typeof v === "bigint") return `${v}`;
  return String(v);
}

function brief(v: unknown): string {
  if (v === null || typeof v !== "object") return scalar(v);
  if (Array.isArray(v)) return `[${v.length}]`;
  return "{…}";
}

/** The expanded body: a long string as-is, anything else pretty-printed. */
export function eventDetail(data: unknown): string {
  if (typeof data === "string") return data;
  const json = JSON.stringify(data);
  return json === undefined ? String(data) : prettyJson(json);
}

/** Is there anything beyond the one-line summary worth expanding to? */
export function eventHasDetail(data: unknown): boolean {
  if (data === null || data === undefined) return false;
  if (typeof data !== "object") return typeof data === "string" && data.length > 80;
  return Array.isArray(data) ? data.length > 0 : Object.keys(data as object).length > 0;
}

/**
 * A custom event the agent recorded with `dude event`: a quiet 24px line
 * in the transcript, aligned with tool calls and thinking — a glyph, the
 * type in mono, one line of the data, who and when. Not a message: no
 * frame, no bubble, muted ink; the role avatar says who without hue on the
 * text. Expands to the data pretty-printed.
 */
export function ChatEvent({ type, data, at, role, expanded, defaultExpanded, onExpandedChange, className, ...rest }: ChatEventProps) {
  const bodyId = useId();
  const detail = eventHasDetail(data);
  const disc = useDisclosure({ expanded, defaultExpanded, onExpandedChange });
  const open = detail && disc.open;
  const summary = summarizeEventData(data);
  const when = new Date(at);
  const line = (
    <>
      <span className={styles["chevron"]} aria-hidden>
        {detail ? <Icon name="chevron-right" size={12} className={styles["chevronIcon"]} /> : null}
      </span>
      <span className={styles["glyph"]} aria-hidden>
        <Icon name="zap" size={12} />
      </span>
      <span className={styles["type"]}>{type}</span>
      {summary ? (
        <span className={styles["summary"]} title={summary}>
          {summary}
        </span>
      ) : null}
    </>
  );
  return (
    <div className={cx(styles["root"], open && styles["open"], className)} data-event-type={type} {...rest}>
      <div className={styles["row"]}>
        {detail ? (
          <button type="button" className={cx(styles["head"], styles["headButton"])} aria-expanded={open} aria-controls={open ? bodyId : undefined} onClick={disc.toggle}>
            {line}
          </button>
        ) : (
          <span className={styles["head"]}>{line}</span>
        )}
        <span className={styles["trailing"]}>
          <span className={styles["who"]} title={`Recorded by the ${ROLE_LABEL[role].toLowerCase()}`}>
            <AgentAvatar role={role} size="xs" />
          </span>
          <time className={styles["time"]} dateTime={Number.isNaN(when.getTime()) ? undefined : when.toISOString()} title={when.toLocaleString()}>
            {formatTimestamp(at, "time")}
          </time>
        </span>
      </div>
      {open ? (
        <pre id={bodyId} className={styles["json"]}>
          {eventDetail(data)}
        </pre>
      ) : null}
    </div>
  );
}

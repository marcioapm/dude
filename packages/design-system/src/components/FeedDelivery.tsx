import { useState } from "react";
import { Icon } from "../icons/index.tsx";
import { Button, IconButton } from "../primitives/Button.tsx";
import { formatTimestamp } from "../util/format.ts";
import styles from "./FeedDelivery.module.css";

export interface FeedDeliveryProps {
  /** When it reached the run. */
  readonly at: string | number | Date;
  readonly deliveryId: string;
  /** What it was, in a few words: "resolved · HighErrorRate · checkout-api". */
  readonly summary: string;
  /** What the agent was given, shown as pretty JSON when opened; undefined while it is not known. */
  readonly body?: unknown;
  readonly source: "incoming" | "github";
  /** Opens the delivery; without it the id is plain text. */
  readonly onOpen?: (() => void) | undefined;
  /** Starts opened to its body. */
  readonly defaultOpen?: boolean | undefined;
  readonly "data-testid"?: string;
}

/**
 * A delivery sent to a running agent, in its transcript where it arrived:
 * a compact row on the info tint with an info rail — glyph, Sent to this
 * run, what it was in mono, its id (a button when `onOpen`), the time —
 * that expands to the body the agent was given.
 */
export function FeedDelivery({ at, deliveryId, summary, body, source, onOpen, defaultOpen = false, ...rest }: FeedDeliveryProps) {
  const [open, setOpen] = useState(defaultOpen);
  const time = formatTimestamp(new Date(at), "time");
  return (
    <div className={styles["delivery"]} data-source={source} data-open={open || undefined} data-testid={rest["data-testid"] ?? "feed-delivery"}>
      <div className={styles["row"]}>
        <Icon name="send" size={13} className={styles["glyph"]} />
        <span className={styles["lead"]}>Sent to this run</span>
        <span className={`${styles["summary"]} ds-mono`} title={summary}>{summary}</span>
        <span className={styles["id"]}>
          {onOpen ? (
            <Button size="sm" variant="quiet" onClick={onOpen} aria-label={`Open delivery ${deliveryId}`} className="ds-mono">{deliveryId}</Button>
          ) : <span className="ds-mono">{deliveryId}</span>}
        </span>
        <time className={`${styles["time"]} ds-mono ds-tnum`} dateTime={new Date(at).toISOString()}>{time}</time>
        <IconButton size="sm" variant="quiet" icon={open ? "chevron-down" : "chevron-right"} aria-expanded={open}
          label={open ? "Hide what the agent was sent" : "Show what the agent was sent"} onClick={() => setOpen(!open)} />
      </div>
      {open ? (
        <pre className={`${styles["body"]} ds-mono`}>{body === undefined ? "Not kept." : JSON.stringify(body, null, 2)}</pre>
      ) : null}
    </div>
  );
}

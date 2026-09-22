import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { formatTimestamp } from "../util/format.ts";
import { type ActivityKind } from "../tokens/activity.ts";
import { AgentAvatar, ROLE_LABEL, type AvatarKind } from "./AgentAvatar.tsx";
import { ActivityIndicator, type ActivityIndicatorProps } from "./ActivityIndicator.tsx";
import { CostDisplay, Duration, TokenCount } from "./Numbers.tsx";
import { Markdown } from "./Markdown.tsx";
import styles from "./ChatMessage.module.css";

export type ChatMessageKind = "agent" | "human" | "system";

/** Human turns are one of three intents, each with a distinct treatment. */
export type HumanIntent = "prompt" | "answer" | "steer";

export interface ChatMessageProps extends Omit<HTMLAttributes<HTMLElement>, "children" | "title" | "content"> {
  readonly role: AvatarKind;
  /** Defaults from `role`: agent roles are `agent`, `human` is `human`, else `system`. */
  readonly kind?: ChatMessageKind | undefined;
  readonly name?: string | undefined;
  readonly model?: string | undefined;
  /** Markdown source. For agent turns this may still be growing. */
  readonly content?: string | undefined;
  /** Pre-rendered body; replaces `content`. */
  readonly children?: ReactNode;
  /** Tokens are still arriving: the body renders open constructs and a caret. */
  readonly streaming?: boolean | undefined;
  /** What the turn is doing now; renders an ActivityIndicator at the foot. */
  readonly activity?: ActivityKind | undefined;
  /** Extra props for the indicator (tool name, attempt, retryAt…). */
  readonly activityProps?: Omit<ActivityIndicatorProps, "kind"> | undefined;
  /** Human turns only. `steer` interrupts; `answer` unblocks; `prompt` is the initial task. */
  readonly intent?: HumanIntent | undefined;
  /** For `answer`: the question that was answered, quoted above the reply. */
  readonly inReplyTo?: string | undefined;
  readonly startedAt?: string | number | Date | undefined;
  readonly endedAt?: string | number | Date | null | undefined;
  readonly costUsd?: number | undefined;
  readonly tokens?: number | undefined;
  /** Tool cards, nested threads, artifacts — anything between the text and the foot. */
  readonly attachments?: ReactNode;
  /** Flash once on mount (a turn that just arrived). */
  readonly isNew?: boolean | undefined;
  /** Hide the avatar/header: this turn continues the previous one by the same actor. */
  readonly continued?: boolean | undefined;
}

const INTENT_LABEL: Record<HumanIntent, string> = {
  prompt: "Task",
  answer: "Answer",
  steer: "Steer",
};

/**
 * One turn in a transcript. Agent turns sit left with a role avatar, a
 * mono model tag and a live foot (elapsed · tokens · cost · activity).
 * Human turns are visually distinct without being bubbles: a hairline
 * frame tinted by intent, so an operator scanning a long transcript can
 * see where a person intervened, and whether it was an answer (unblocked
 * a question) or a steer (interrupted the agent).
 *
 * The body is Markdown rendered from a typed AST and grows in place while
 * streaming; earlier blocks never reflow when a later token lands.
 */
export function ChatMessage({
  role,
  kind,
  name,
  model,
  content,
  children,
  streaming,
  activity,
  activityProps,
  intent,
  inReplyTo,
  startedAt,
  endedAt,
  costUsd,
  tokens,
  attachments,
  isNew,
  continued,
  className,
  ...rest
}: ChatMessageProps) {
  const k: ChatMessageKind = kind ?? (role === "human" ? "human" : role === "system" || role === "integration" ? "system" : "agent");
  const live = streaming === true || (activity !== undefined && activity !== "completed" && activity !== "failed" && activity !== "aborted");
  const ts = startedAt !== undefined ? new Date(startedAt) : null;
  // A turn has a duration while it is running, or once it has an end to
  // measure to. A settled turn with neither has no duration to show — the
  // header's timestamp already says when it happened.
  const timed = startedAt !== undefined && (live || (endedAt !== undefined && endedAt !== null));

  if (k === "system") {
    return (
      <article className={cx(styles["system"], isNew && styles["new"], className)} data-kind="system" {...rest}>
        <span className={styles["systemLine"]} aria-hidden />
        <span className={styles["systemText"]}>{children ?? content}</span>
        {ts ? (
          <time className={styles["systemTime"]} dateTime={ts.toISOString()}>
            {formatTimestamp(ts, "time")}
          </time>
        ) : null}
        <span className={styles["systemLine"]} aria-hidden />
      </article>
    );
  }

  const humanIntent: HumanIntent = intent ?? "prompt";

  return (
    <article
      className={cx(
        styles["root"],
        k === "human" ? styles["human"] : styles["agent"],
        k === "human" && styles[`intent-${humanIntent}`],
        live && styles["live"],
        continued && styles["continued"],
        isNew && styles["new"],
        className,
      )}
      data-kind={k}
      data-role={role}
      data-intent={k === "human" ? humanIntent : undefined}
      aria-busy={live || undefined}
      {...rest}
    >
      <div className={styles["gutter"]}>{continued ? null : <AgentAvatar role={role} size="sm" live={live} />}</div>
      <div className={styles["main"]}>
        {continued ? null : (
          <header className={styles["header"]}>
            <span className={styles["name"]}>{name ?? ROLE_LABEL[role]}</span>
            {k === "agent" && name ? <span className={styles["roleName"]}>{ROLE_LABEL[role]}</span> : null}
            {k === "human" ? <span className={cx(styles["intent"], styles[`intentTag-${humanIntent}`])}>{INTENT_LABEL[humanIntent]}</span> : null}
            {model ? <code className={styles["model"]}>{model}</code> : null}
            {ts ? (
              <time className={styles["time"]} dateTime={ts.toISOString()} title={ts.toISOString()}>
                {formatTimestamp(ts, "time")}
              </time>
            ) : null}
          </header>
        )}
        {k === "human" && humanIntent === "answer" && inReplyTo ? (
          <div className={styles["quote"]}>
            <span className={styles["quoteLabel"]}>Answering</span>
            <span className={styles["quoteText"]}>{inReplyTo}</span>
          </div>
        ) : null}
        {children !== undefined ? (
          <div className={styles["body"]}>{children}</div>
        ) : content !== undefined && (content.length > 0 || streaming) ? (
          <Markdown source={content} streaming={streaming} className={styles["body"]} />
        ) : null}
        {attachments !== undefined ? <div className={styles["attachments"]}>{attachments}</div> : null}
        {activity !== undefined || (k === "agent" && (costUsd !== undefined || tokens !== undefined || timed)) ? (
          <footer className={styles["foot"]}>
            {activity !== undefined ? <ActivityIndicator kind={activity} {...activityProps} className={styles["activity"]} /> : <span className={styles["footSpacer"]} />}
            <span className={styles["stats"]}>
              {timed ? <Duration since={startedAt} until={endedAt} live={live} tone="muted" /> : null}
              {tokens !== undefined ? <TokenCount tokens={tokens} tone="muted" /> : null}
              {costUsd !== undefined ? <CostDisplay usd={costUsd} tone="secondary" live={live} /> : null}
            </span>
          </footer>
        ) : null}
      </div>
    </article>
  );
}

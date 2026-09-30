import { useLayoutEffect, useRef, useState, type HTMLAttributes, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { formatTimestamp } from "../util/format.ts";
import { toMs } from "../util/useNow.ts";
import { type ActivityKind } from "../tokens/activity.ts";
import { Icon } from "../icons/index.tsx";
import { AgentAvatar, ROLE_LABEL, type AvatarKind } from "./AgentAvatar.tsx";
import { PersonAvatar, type Person } from "./PersonAvatar.tsx";
import { ActivityIndicator, type ActivityIndicatorProps } from "./ActivityIndicator.tsx";
import { CostDisplay, Duration, TokenCount } from "./Numbers.tsx";
import { Markdown } from "./Markdown.tsx";
import styles from "./ChatMessage.module.css";

export type ChatMessageKind = "agent" | "human" | "system";

/** Turns addressed *to* the agent are one of three intents, each with a distinct treatment. */
export type HumanIntent = "prompt" | "answer" | "steer";

export interface ChatMessageProps extends Omit<HTMLAttributes<HTMLElement>, "children" | "title" | "content"> {
  readonly role: AvatarKind;
  /**
   * Defaults from `role` and `intent`: agent roles are `agent`; `human` is
   * `human`; `system` / `integration` are `system` — unless an `intent` is
   * given, in which case the turn is framed like a human one. That is how
   * the factory's own prompt is shown: `role="system" intent="prompt"`.
   */
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
  /** Framed turns only. `steer` interrupts; `answer` unblocks; `prompt` is the initial task. */
  readonly intent?: HumanIntent | undefined;
  /** For `answer`: the question that was answered, quoted above the reply. */
  readonly inReplyTo?: string | undefined;
  /**
   * Framed turns only: when the agent actually received it. `null` means
   * sent but not yet delivered — a steer arriving mid-turn is held until
   * the turn ends, and until then the turn shows a "Queued" mark and a
   * dashed frame. `undefined` means delivery is not tracked for this turn.
   */
  readonly deliveredAt?: string | number | Date | null | undefined;
  readonly startedAt?: string | number | Date | undefined;
  readonly endedAt?: string | number | Date | null | undefined;
  /** `null` = not known (e.g. a subscription harness). Renders as "—", never as $0.00. */
  readonly costUsd?: number | null | undefined;
  /** Total tokens for the turn, when that is all that is known. */
  readonly tokens?: number | undefined;
  /** Size of the context after this turn ("ctx 15.2k"). */
  readonly contextTokens?: number | undefined;
  /** The model's context window; shown as "/ 744k" after `contextTokens` and colours it at 80% and 100%. */
  readonly contextWindowTokens?: number | undefined;
  /** Output tokens the model produced for this turn ("out 1.2k"). */
  readonly outputTokens?: number | undefined;
  /**
   * Clamp the body to this many lines with a "Show all" control. Defaults
   * to 8 for `intent="prompt"` (the factory's phase prompt runs to several
   * paragraphs) and to none otherwise. `false` disables it.
   */
  readonly maxLines?: number | false | undefined;
  /** Tool cards, nested threads, artifacts — anything between the text and the foot. */
  readonly attachments?: ReactNode;
  /** Flash once on mount (a turn that just arrived). */
  readonly isNew?: boolean | undefined;
  /** Hide the avatar/header: this turn continues the previous one by the same actor. */
  readonly continued?: boolean | undefined;
  /** Row actions (copy, quote…). Float over the row's top-right corner on hover or focus. */
  readonly actions?: ReactNode;
  /**
   * The person who wrote a human turn: their face in the gutter and their
   * name in the header, instead of the anonymous human glyph. A steer or
   * an answer is signed whenever the name is known.
   */
  readonly person?: Person | undefined;
  /**
   * A face of its own in the gutter, in place of the role's tile: dude's,
   * which the app owns (the design system carries no brand image).
   */
  readonly avatar?: ReactNode;
}

const INTENT_LABEL: Record<HumanIntent, string> = {
  prompt: "Task",
  answer: "Answer",
  steer: "Steer",
};

const PROMPT_MAX_LINES = 8;

/** A Date for a timestamp prop, or null when it is missing or does not parse — an unparseable string must not take the render down. */
function toDate(v: string | number | Date | null | undefined): Date | null {
  const ms = toMs(v);
  return ms === null ? null : new Date(ms);
}

/**
 * One turn in a transcript, laid out as a Discord message: avatar gutter,
 * then name · tag · model · time on one baseline, then the body. No frame:
 * a hovered row takes a faint full-width wash and shows its `actions`. A
 * turn that continues the same author drops avatar and header. Turns
 * addressed to the agent carry an intent tag; a steer additionally takes
 * the highlight (tinted row + 2px accent bar) because it interrupted the
 * agent and must be findable in a long transcript.
 *
 * The body is Markdown rendered from a typed AST and grows in place while
 * streaming at chat leading, so earlier blocks do not reflow when a later
 * token lands. A multi-block reply takes the long-form rhythm once, when
 * streaming ends.
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
  deliveredAt,
  startedAt,
  endedAt,
  costUsd,
  tokens,
  contextTokens,
  contextWindowTokens,
  outputTokens,
  maxLines,
  attachments,
  isNew,
  continued,
  actions,
  person,
  avatar,
  className,
  ...rest
}: ChatMessageProps) {
  const k: ChatMessageKind =
    kind ?? (role === "human" || intent !== undefined ? "human" : role === "system" || role === "integration" ? "system" : "agent");
  const live = streaming === true || (activity !== undefined && activity !== "completed" && activity !== "failed" && activity !== "aborted");
  const ts = toDate(startedAt);
  // A turn has a duration while it is running, or once it has an end to
  // measure to. A settled turn with neither has no duration to show — the
  // header's timestamp already says when it happened.
  const timed = ts !== null && (live || toMs(endedAt) !== null);

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
  const queued = k === "human" && deliveredAt === null;
  const delivered = k === "human" ? toDate(deliveredAt) : null;
  const clampLines = maxLines === false ? null : maxLines ?? (k === "human" && humanIntent === "prompt" ? PROMPT_MAX_LINES : null);
  const hasStats = k === "agent" && (costUsd !== undefined || tokens !== undefined || contextTokens !== undefined || outputTokens !== undefined || timed);

  return (
    <article
      className={cx(
        styles["root"],
        k === "human" ? styles["human"] : styles["agent"],
        k === "human" && styles[`intent-${humanIntent}`],
        queued && styles["queued"],
        live && styles["live"],
        continued && styles["continued"],
        isNew && styles["new"],
        className,
      )}
      data-kind={k}
      data-role={role}
      data-intent={k === "human" ? humanIntent : undefined}
      data-pending={queued ? "true" : undefined}
      aria-busy={live || undefined}
      {...rest}
    >
      <div className={styles["gutter"]}>
        {continued ? (
          ts ? (
            <time className={styles["gutterTime"]} dateTime={ts.toISOString()} aria-hidden>
              {formatTimestamp(ts, "time-short")}
            </time>
          ) : null
        ) : avatar ? (
          <span className={styles["ownAvatar"]}>{avatar}</span>
        ) : person ? (
          // The chat avatar's size follows the density, as an agent's does.
          <PersonAvatar person={person} size={40} style={{ ["--av-size" as string]: "var(--ds-size-avatar-chat)" }} />
        ) : (
          <AgentAvatar role={role} size="chat" live={live} />
        )}
      </div>
      <div className={styles["main"]}>
        {continued ? null : (
          <header className={styles["header"]}>
            <span className={styles["name"]}>{name ?? person?.name ?? ROLE_LABEL[role]}</span>
            {k === "agent" && name ? <span className={styles["roleName"]}>{ROLE_LABEL[role]}</span> : null}
            {k === "human" ? <span className={cx(styles["intent"], styles[`intentTag-${humanIntent}`])}><span className="ds-cap">{INTENT_LABEL[humanIntent]}</span></span> : null}
            {queued ? (
              <span className={styles["queued-tag"]} title="Sent. The agent is mid-turn; it will read this when the turn ends.">
                <Icon name="clock" size={10} strokeWidth={2} />
                <span className="ds-cap">Queued</span>
              </span>
            ) : null}
            {model ? <code className={styles["model"]}>{model}</code> : null}
            {ts ? (
              <time className={styles["time"]} dateTime={ts.toISOString()} title={ts.toISOString()}>
                {formatTimestamp(ts, "time")}
              </time>
            ) : null}
            {delivered ? (
              <time className={styles["delivered"]} dateTime={delivered.toISOString()} title={`Delivered to the agent at ${delivered.toISOString()}`}>
                delivered {formatTimestamp(delivered, "time")}
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
        <ClampedBody lines={clampLines}>
          {children !== undefined ? (
            <div className={styles["body"]}>{children}</div>
          ) : content !== undefined && (content.length > 0 || streaming) ? (
            <Markdown source={content} streaming={streaming} breaks={k === "human"} unmeasured className={styles["body"]} />
          ) : null}
        </ClampedBody>
        {queued ? <div className={styles["queuedNote"]}>Waiting for the current turn to end before the agent reads this.</div> : null}
        {attachments !== undefined ? <div className={styles["attachments"]}>{attachments}</div> : null}
        {actions !== undefined ? <div className={styles["actions"]}>{actions}</div> : null}
        {activity !== undefined || hasStats ? (
          <footer className={styles["foot"]}>
            {activity !== undefined ? <ActivityIndicator kind={activity} {...activityProps} className={styles["activity"]} /> : <span className={styles["footSpacer"]} />}
            <span className={styles["stats"]}>
              {timed ? <Duration since={startedAt} until={endedAt} live={live} tone="muted" /> : null}
              {contextTokens !== undefined ? <TokenCount tokens={contextTokens} label="ctx" windowTokens={contextWindowTokens} tone="muted" /> : null}
              {outputTokens !== undefined ? <TokenCount tokens={outputTokens} label="out" tone="muted" /> : null}
              {tokens !== undefined ? <TokenCount tokens={tokens} tone="muted" /> : null}
              {costUsd !== undefined ? <CostDisplay usd={costUsd} tone="secondary" live={live && costUsd !== null} /> : null}
            </span>
          </footer>
        ) : null}
      </div>
    </article>
  );
}

/**
 * Reasoning and tool calls between turns, placed on the message text
 * column (past the avatar gutter) so their left edge lines up with the
 * body text above and below. Consecutive asides stack 4px apart.
 */
export function ChatAside({ className, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cx(styles["aside"], className)} {...rest} />;
}

/**
 * Clamps its child to `lines` and offers "Show all" only when the content
 * actually overflows — measured after layout, so a four-line prompt gets
 * no control and a forty-line one gets exactly one.
 */
function ClampedBody({ lines, children }: { readonly lines: number | null; readonly children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [overflows, setOverflows] = useState(false);
  const [limit, setLimit] = useState<number | null>(null);

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el || lines === null) return;
    const measure = () => {
      // The body's own line box: it differs by density and when Markdown
      // switches a multi-block reply to prose leading. The body is the
      // child, so a `1lh` on this wrapper would read the wrong leading.
      const line = Number.parseFloat(getComputedStyle(el.firstElementChild ?? el).lineHeight);
      if (!Number.isFinite(line)) return;
      setLimit(lines * line);
      setOverflows(el.scrollHeight > lines * line + line / 2);
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    // Content that changes size (a prompt that finishes arriving) re-measures
    // through the observer, not through a dependency on `children`, which
    // is a new node every render.
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [lines]);

  if (lines === null) return <>{children}</>;
  const clamped = overflows && !open && limit !== null;
  return (
    <div className={cx(styles["clamp"], clamped && styles["clamped"])}>
      <div ref={ref} className={styles["clampInner"]} style={clamped ? { maxHeight: limit } : undefined}>
        {children}
      </div>
      {overflows ? (
        <button type="button" className={styles["clampToggle"]} onClick={() => setOpen((o) => !o)} aria-expanded={open}>
          <Icon name={open ? "chevron-up" : "chevron-down"} size={11} strokeWidth={2} />
          {open ? "Show less" : "Show all"}
        </button>
      ) : null}
    </div>
  );
}

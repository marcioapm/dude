import type { HTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import { formatTimestamp } from "../util/format.ts";
import { toMs } from "../util/useNow.ts";
import { Icon } from "../icons/index.tsx";
import { AgentAvatar, ROLE_LABEL, type AvatarKind } from "./AgentAvatar.tsx";
import { Duration } from "./Numbers.tsx";
import { Markdown } from "./Markdown.tsx";
import { Tooltip } from "../primitives/Tooltip.tsx";
import styles from "./QuestionCard.module.css";

export type QuestionState = "waiting" | "answered" | "dismissed";

/** A question the agent asks, or a request (for a repository) it makes. */
export type QuestionKind = "question" | "request";

/** How each kind is spoken of: what it is, what a person does with it, and what the agent waits for. */
const WORDS: Record<QuestionKind, { noun: string; verb: string; awaited: string }> = {
  question: { noun: "a question", verb: "answer", awaited: "an answer" },
  request: { noun: "for a repository", verb: "decide", awaited: "a decision" },
};

export interface QuestionCardProps extends Omit<HTMLAttributes<HTMLElement>, "children" | "title"> {
  /** Who is asking — the role that is blocked. */
  readonly role: AvatarKind;
  /** The asking session's name when there are several of that role ("implementer-2"). */
  readonly name?: string | undefined;
  /** The question, as Markdown. */
  readonly text: string;
  /** Choices the agent offered. Shown as chips; one-click only with `onChoose`. */
  readonly options?: ReadonlyArray<string> | undefined;
  readonly askedAt?: string | number | Date | undefined;
  /**
   * When a person answered. `null` or `undefined` means nobody has yet: the
   * card is loud and its wait clock ticks. Once set, the card settles into
   * the history and the answer follows as its own `ChatMessage
   * intent="answer"` turn — the card never repeats it.
   */
  readonly answeredAt?: string | number | Date | null | undefined;
  /**
   * The question was never answered and never will be — the session
   * ended, was aborted, or the run was retried. Settles the card without
   * claiming an answer, so a dead question cannot ring forever.
   */
  readonly dismissed?: boolean | undefined;
  /**
   * Settled without an answer here: what was asked was decided elsewhere,
   * named in a few words ("Decided on the banner"). Settles the card as
   * `dismissed` does, saying where instead of "No longer needed".
   */
  readonly settledBy?: string | undefined;
  /**
   * Make the offered choices one-click replies in the card while waiting.
   * Without it a waiting card does not list them: the composer carries the
   * same choices as buttons, and they are shown once. Settled cards list
   * them as the record of what was offered.
   */
  readonly onChoose?: ((option: string) => void) | undefined;
  /**
   * The person it waits on, when that is someone other than the reader:
   * the card says so, and lists the choices without offering them — only
   * they may answer.
   */
  readonly waitingOn?: string | undefined;
  /** A question to answer (the default), or a request to decide: every word the card says follows. */
  readonly kind?: QuestionKind | undefined;
  /** Flash once on mount (a question that just arrived). */
  readonly isNew?: boolean | undefined;
}

/** A Date for a timestamp prop, or null when it is missing or does not parse. */
function toDate(v: string | number | Date | null | undefined): Date | null {
  const ms = toMs(v);
  return ms === null ? null : new Date(ms);
}

/**
 * An agent's question to a person, as a turn in the transcript. Two
 * states that must not be confused:
 *
 *   waiting   the run is blocked on someone. This is the one loud turn a
 *             transcript is allowed, and it is loud once: the attention
 *             highlight (tint and bar). Everything inside it is neutral —
 *             the transcript header's badge already names the state, the
 *             wait clock is muted, and the choices live in the composer.
 *             In grayscale it is still the only barred, tinted turn.
 *
 *   answered  history. The wash and bar go, and a quiet "Answered" mark
 *             says when and how long it waited. The answer itself is the
 *             `ChatMessage intent="answer"` turn that follows; the card
 *             does not quote it, so nothing in the transcript is said twice.
 */
export function QuestionCard({ role, name, text, options, askedAt, answeredAt, dismissed, settledBy, onChoose, waitingOn, kind = "question", isNew, className, ...rest }: QuestionCardProps) {
  const words = WORDS[kind];
  const takeOver = `Take over this task to ${words.verb}`;
  const answered = toDate(answeredAt);
  const state: QuestionState = answered ? "answered" : dismissed || settledBy ? "dismissed" : "waiting";
  const waiting = state === "waiting";
  const asked = toDate(askedAt);
  const who = name ?? ROLE_LABEL[role];
  const someoneElse = waiting && waitingOn !== undefined;
  const clickable = waiting && !someoneElse && onChoose !== undefined;
  // Someone else's to answer: no composer offers the choices, so the card
  // lists them, as a record of what they will choose from.
  const hasOptions = options !== undefined && options.length > 0 && (!waiting || clickable || someoneElse);

  const list = hasOptions ? (
    <ul className={cx(styles["options"], someoneElse && styles["optionsElse"])}
      aria-label={clickable ? "Reply with one of" : "Choices offered"} data-testid={someoneElse ? "choices-someone-else" : undefined}>
      {options.map((o, i) => (
        <li key={i} className={styles["optionItem"]}>
          {clickable ? (
            <button type="button" className={cx(styles["option"], styles["optionButton"])} onClick={() => onChoose(o)}>
              <span className={styles["optionIndex"]}>{i + 1}</span>
              {o}
            </button>
          ) : (
            <span className={styles["option"]}>
              <span className={styles["optionIndex"]}>{i + 1}</span>
              {o}
            </span>
          )}
        </li>
      ))}
    </ul>
  ) : null;
  return (
    <article
      className={cx(styles["root"], styles[state], isNew && styles["new"], className)}
      data-state={state}
      data-role={role}
      aria-label={someoneElse ? `${who} asks ${words.noun} and is waiting for ${waitingOn} to ${words.verb}`
        : waiting ? `${who} asks ${words.noun} and is waiting for ${words.awaited}`
        : state === "dismissed" ? (settledBy ? `${who} asked a question, settled elsewhere: ${settledBy}`
          : `${who} asked a question that is no longer needed: its run ended`) : `${who} asked a question`}
      {...rest}
    >
      <div className={styles["gutter"]}>
        <AgentAvatar role={role} size="chat" live={waiting} />
      </div>
      <div className={styles["main"]}>
        <header className={styles["header"]}>
          <span className={styles["name"]}>{who}</span>
          {name ? <span className={styles["roleName"]}>{ROLE_LABEL[role]}</span> : null}
          <span className={styles["verb"]}>{waiting ? "asks" : "asked"}</span>
          {/* Announced once when it appears; the clock lives outside the live region so it is not re-read every second. */}
          {someoneElse ? (
            <span role="status" aria-live="polite" className="ds-sr-only">
              Blocked until {waitingOn} {words.verb}s.
            </span>
          ) : waiting ? (
            <span role="status" aria-live="polite" className="ds-sr-only">
              Needs you. Blocked until you answer.
            </span>
          ) : state === "answered" ? (
            <span className={cx(styles["tag"], styles["tagAnswered"])}>
              <Icon name="check" size={10} strokeWidth={2} />
              <span className="ds-cap">Answered</span>
            </span>
          ) : (
            <span className={cx(styles["tag"], styles["tagDismissed"])}>
              <Icon name="cross" size={10} strokeWidth={2} />
              {settledBy
                ? <span className="ds-cap" data-testid="settled-by">{settledBy}</span>
                : <span className="ds-cap" title="Its run ended: nobody would hear an answer">No longer needed</span>}
            </span>
          )}
          {asked ? (
            waiting ? (
              <span className={styles["wait"]} title={`Asked at ${asked.toISOString()}`}>
                waiting <Duration since={asked} live />
              </span>
            ) : answered ? (
              <span className={styles["waited"]} title={`Answered at ${answered.toISOString()}`}>
                after <Duration since={asked} until={answered} />
              </span>
            ) : null
          ) : null}
          {asked ? (
            <time className={styles["time"]} dateTime={asked.toISOString()} title={`Asked at ${asked.toISOString()}`}>
              {formatTimestamp(asked, "time")}
            </time>
          ) : null}
        </header>
        <Markdown source={text} unmeasured className={styles["body"]} />
        {someoneElse ? (
          <p className={styles["waitingOn"]} data-testid="waiting-on">
            <Icon name="hand" size={12} />
            Waiting for {waitingOn} to {words.verb}
            {/* How to make it yours, in words everyone sees: mouse, keyboard, touch, screen reader. */}
            {" "}<span className={styles["takeOver"]} data-testid="take-over">· {takeOver}</span>
          </p>
        ) : null}
        {/* Someone else's: shown but not offered. With a mouse, hovering them says it
            again, and a press (which does nothing) leaves those words up. */}
        {list && someoneElse ? (
          <Tooltip content={takeOver} side="bottom" keepOnPress>
            {list}
          </Tooltip>
        ) : list}
      </div>
    </article>
  );
}

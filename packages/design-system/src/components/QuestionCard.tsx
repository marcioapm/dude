import type { HTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import { formatTimestamp } from "../util/format.ts";
import { toMs } from "../util/useNow.ts";
import { Icon } from "../icons/index.tsx";
import { AgentAvatar, ROLE_LABEL, type AvatarKind } from "./AgentAvatar.tsx";
import { Duration } from "./Numbers.tsx";
import { Markdown } from "./Markdown.tsx";
import { StatusBadge } from "./StatusBadge.tsx";
import styles from "./QuestionCard.module.css";

export type QuestionState = "waiting" | "answered" | "dismissed";

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
   * Make the offered choices one-click replies while waiting. Without it
   * they are read-only — the composer, which has the same choices as
   * buttons, is where a reply is sent from.
   */
  readonly onChoose?: ((option: string) => void) | undefined;
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
 *   waiting   the run is blocked on someone. This is the one loud thing a
 *             transcript is allowed: the needs-you badge with its ring,
 *             an attention frame and bar (the same treatment the tree row
 *             and the board card use for the same state), the avatar
 *             marked live, and a wait clock ticking in attention ink. An
 *             operator scrolling a long chat must land on it at once — and
 *             in grayscale it is still the only framed turn with a solid
 *             badge, a bar and a clock.
 *
 *   answered  history. The frame drops to a hairline, the wash and bar go,
 *             the badge becomes a quiet "Answered" mark with when and how
 *             long it waited. The answer itself is the `ChatMessage
 *             intent="answer"` turn that follows; the card does not quote
 *             it, so nothing in the transcript is said twice.
 *
 * The choices the agent offered are shown so the question reads in full
 * without the composer. They are chips, not buttons, unless `onChoose`
 * is given: the composer already has the one-click reply and one place to
 * act is enough.
 */
export function QuestionCard({ role, name, text, options, askedAt, answeredAt, dismissed, onChoose, isNew, className, ...rest }: QuestionCardProps) {
  const answered = toDate(answeredAt);
  const state: QuestionState = answered ? "answered" : dismissed ? "dismissed" : "waiting";
  const waiting = state === "waiting";
  const asked = toDate(askedAt);
  const who = name ?? ROLE_LABEL[role];
  const clickable = waiting && onChoose !== undefined;
  const hasOptions = options !== undefined && options.length > 0;

  return (
    <article
      className={cx(styles["root"], styles[state], isNew && styles["new"], className)}
      data-state={state}
      data-role={role}
      aria-label={waiting ? `${who} asks a question and is waiting for an answer` : `${who} asked a question`}
      {...rest}
    >
      <div className={styles["gutter"]}>
        <AgentAvatar role={role} size="sm" live={waiting} />
      </div>
      <div className={styles["main"]}>
        <header className={styles["header"]}>
          <span className={styles["name"]}>{who}</span>
          {name ? <span className={styles["roleName"]}>{ROLE_LABEL[role]}</span> : null}
          <span className={styles["verb"]}>{waiting ? "asks" : "asked"}</span>
          {/* Announced once when it appears; the clock lives outside the live region so it is not re-read every second. */}
          {waiting ? (
            <span role="status" aria-live="polite" className={styles["status"]}>
              <StatusBadge status="awaiting_input" size="sm" />
              <span className="ds-sr-only">Blocked until you answer.</span>
            </span>
          ) : state === "answered" ? (
            <span className={cx(styles["tag"], styles["tagAnswered"])}>
              <Icon name="check" size={10} strokeWidth={2} />
              Answered
            </span>
          ) : (
            <span className={cx(styles["tag"], styles["tagDismissed"])}>
              <Icon name="cross" size={10} strokeWidth={2} />
              Not answered
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
        <Markdown source={text} className={styles["body"]} />
        {hasOptions ? (
          <ul className={styles["options"]} aria-label={clickable ? "Reply with one of" : "Choices offered"}>
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
        ) : null}
        {waiting ? <div className={styles["hint"]}>{clickable ? "Pick a choice, or reply in the composer." : "Reply in the composer."}</div> : null}
      </div>
    </article>
  );
}

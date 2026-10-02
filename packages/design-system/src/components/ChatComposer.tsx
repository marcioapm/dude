import { useEffect, useId, useRef, useState, type HTMLAttributes, type KeyboardEvent, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { Button } from "../primitives/Button.tsx";
import styles from "./ChatComposer.module.css";

/**
 * The two ways a human intervenes, plus the initial prompt, plus `chat`:
 * talking with a task's conductor, which is a conversation, not an
 * intervention in someone's turn.
 */
export type ComposerMode = "answer" | "steer" | "prompt" | "chat";

export interface PendingQuestion {
  readonly id: string;
  readonly text: string;
  /** Who asked, for the label ("Orchestrator asks"). */
  readonly askedBy?: string | undefined;
  readonly askedAt?: string | number | Date | undefined;
  /** Offered choices; each becomes a one-click reply. */
  readonly options?: ReadonlyArray<string> | undefined;
}

export interface ChatComposerProps extends Omit<HTMLAttributes<HTMLFormElement>, "onSubmit" | "children"> {
  /**
   * Explicit mode. When omitted: `answer` if a `question` is pending,
   * `steer` if the session is running, `prompt` otherwise.
   */
  readonly mode?: ComposerMode | undefined;
  /** The blocking question, when the session is `awaiting_input`. */
  readonly question?: PendingQuestion | undefined;
  /** The session is running; a steer will interrupt it. */
  readonly running?: boolean | undefined;
  /** Nothing accepts input (terminal session). */
  readonly disabled?: boolean | undefined;
  readonly disabledReason?: string | undefined;
  readonly placeholder?: string | undefined;
  readonly value?: string | undefined;
  readonly defaultValue?: string | undefined;
  readonly onValueChange?: ((value: string) => void) | undefined;
  readonly onSubmit: (submission: ComposerSubmission) => void | Promise<void>;
  /** Extra controls at the left of the action row (attach, templates…). */
  readonly leading?: ReactNode;
  readonly autoFocus?: boolean | undefined;
  /** Who it is sent as: "Sent as Ana" in the action row, so a steer is never anonymous. */
  readonly sentAs?: string | undefined;
  /**
   * Steer only: offer "interrupt now", which stops the current turn so
   * the agent hears the steer at once. Off by default: a steer lands at
   * the agent's next step (or its next turn), without stopping anything.
   */
  readonly canInterrupt?: boolean | undefined;
  /**
   * Steer only: where a steer sent now lands, beside the keys — "Lands
   * after the current tool", "Lands at the agent's next step", "Lands when
   * the turn ends". The app knows; the composer only says it.
   */
  readonly landsHint?: ReactNode;
  /**
   * Chat only: who it goes to and on what terms, where `sentAs` would be —
   * "To **Conductor** · read-only".
   */
  readonly to?: ReactNode;
}

export type ComposerSubmission =
  | { readonly mode: "answer"; readonly questionId: string; readonly text: string }
  | { readonly mode: "steer"; readonly text: string; readonly interrupt: boolean }
  | { readonly mode: "prompt"; readonly text: string }
  | { readonly mode: "chat"; readonly text: string };

const MODE_LABEL: Record<ComposerMode, string> = {
  answer: "Answer",
  steer: "Steer",
  prompt: "Send",
  chat: "Send",
};
const MODE_PLACEHOLDER: Record<ComposerMode, string> = {
  answer: "Type your answer…",
  steer: "Steer the agent…",
  prompt: "Describe the task…",
  chat: "Ask about this task…",
};

/**
 * The human's input. Two modes are distinct on purpose:
 *
 *   answer  the session is blocked on a question. The transcript shows the
 *           question in full (`QuestionCard`); the composer says only
 *           which one it answers, on one truncated line, and puts the
 *           offered choices as one-click chips beside the button. The
 *           button says "Answer" in the attention tone. Submitting
 *           unblocks the session.
 *
 *   steer   the session is running. The frame is accent-tinted and the
 *           button says "Steer". A steer lands at the agent's next step
 *           without stopping it (`landsHint` says where), so sending one
 *           costs nothing: plain Enter sends it, as in any chat.
 *           "Interrupt now" (off unless ticked) stops the turn so it is
 *           heard at once — that is the costly one, and it is a
 *           deliberate tick, not a key.
 *
 *   chat    a task's Chat: a message to its conductor, which answers it.
 *           Accent-toned like a steer — it is talking to an agent — but
 *           with Send and no interrupt: it starts the conductor's next
 *           turn, never cuts one short. `to` names who it goes to.
 *
 * Enter sends in every mode; Shift+Enter always inserts a newline. The
 * action row says who it is sent as.
 */
export function ChatComposer({
  mode: modeProp,
  question,
  running,
  disabled,
  disabledReason,
  placeholder,
  value,
  defaultValue,
  onValueChange,
  onSubmit,
  leading,
  autoFocus,
  sentAs,
  canInterrupt,
  landsHint,
  to,
  className,
  ...rest
}: ChatComposerProps) {
  const [interrupt, setInterrupt] = useState(false);
  const mode: ComposerMode = modeProp ?? (question ? "answer" : running ? "steer" : "prompt");
  const [internal, setInternal] = useState(defaultValue ?? "");
  const text = value ?? internal;
  const [busy, setBusy] = useState(false);
  const areaRef = useRef<HTMLTextAreaElement>(null);
  const id = useId();

  const setText = (v: string) => {
    if (value === undefined) setInternal(v);
    onValueChange?.(v);
  };

  // Autosize: 1–8 lines plus padding, both read from the computed style
  // because they follow the density.
  useEffect(() => {
    const el = areaRef.current;
    if (!el) return;
    const cs = getComputedStyle(el);
    const line = Number.parseFloat(cs.lineHeight) || 22;
    const pad = (Number.parseFloat(cs.paddingTop) || 0) + (Number.parseFloat(cs.paddingBottom) || 0);
    el.style.height = "0px";
    el.style.height = `${Math.min(8 * line + pad, Math.max(line + pad, el.scrollHeight))}px`;
  }, [text]);

  const canSubmit = !disabled && !busy && text.trim().length > 0;

  const submit = async (override?: string) => {
    const t = (override ?? text).trim();
    if (disabled || busy || t.length === 0) return;
    const submission: ComposerSubmission =
      mode === "answer" && question ? { mode: "answer", questionId: question.id, text: t }
        : mode === "steer" ? { mode: "steer", text: t, interrupt }
        : mode === "chat" ? { mode: "chat", text: t }
        : { mode: "prompt", text: t };
    setBusy(true);
    try {
      await onSubmit(submission);
      setText("");
      setInterrupt(false);
    } finally {
      setBusy(false);
    }
  };

  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key !== "Enter" || e.shiftKey || e.nativeEvent.isComposing) return; // Shift+Enter is a new line
    e.preventDefault();
    void submit();
  };

  const isDisabled = disabled === true;
  const answerOptions = mode === "answer" && question?.options ? question.options : [];

  return (
    <form
      className={cx(styles["root"], styles[mode], isDisabled && styles["disabled"], className)}
      data-mode={mode}
      onSubmit={(e) => {
        e.preventDefault();
        void submit();
      }}
      {...rest}
    >
      {mode === "answer" && question ? (
        <div className={styles["question"]} role="note" aria-label="Pending question" title={question.text}>
          <Icon name="hand" size={14} className={styles["questionIcon"]} />
          <span className={styles["questionLabel"]}>Answering {question.askedBy ?? "the agent"}:</span>
          <span className={styles["questionText"]}>{question.text}</span>
        </div>
      ) : null}

      <div className={styles["field"]}>
        <label htmlFor={id} className="ds-sr-only">
          {MODE_LABEL[mode]}
        </label>
        <textarea
          ref={areaRef}
          id={id}
          className={styles["textarea"]}
          value={text}
          rows={1}
          placeholder={isDisabled ? disabledReason ?? "This session is finished." : placeholder ?? MODE_PLACEHOLDER[mode]}
          disabled={isDisabled}
          autoFocus={autoFocus}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={onKey}
          aria-describedby={`${id}-hint`}
        />
      </div>

      <div className={styles["actions"]}>
        {leading}
        {answerOptions.length > 0 ? (
          <div className={styles["options"]} role="group" aria-label="Answer with one of">
            {answerOptions.map((o) => (
              <button key={o} type="button" className={styles["option"]} disabled={isDisabled || busy} onClick={() => void submit(o)} title={o}>
                <span className="ds-cap">{o}</span>
              </button>
            ))}
          </div>
        ) : null}
        {mode === "chat" && to && !isDisabled ? (
          <span className={styles["sentAs"]} data-testid="composer-to">{to}</span>
        ) : sentAs && !isDisabled ? (
          <span className={styles["sentAs"]}>
            Sent as <b>{sentAs}</b>
          </span>
        ) : null}
        {mode === "steer" && canInterrupt && !isDisabled ? (
          <label className={styles["interrupt"]} title="Stop the agent's current turn so it hears this now. Otherwise it reads it at its next step.">
            <input type="checkbox" checked={interrupt} onChange={(e) => setInterrupt(e.target.checked)} />
            interrupt now
          </label>
        ) : null}
        <span className={styles["spacer"]} />
        <span id={`${id}-hint`} className={cx(styles["hint"], answerOptions.length > 0 && "ds-sr-only")}>
          {isDisabled ? null : (
            <>
              {mode === "steer" && landsHint && !interrupt ? <span className={styles["lands"]} data-testid="lands-hint">{landsHint}</span> : null}
              <span className={cx(styles["hint"], Boolean(landsHint) && mode === "steer" && !interrupt && styles["keys"])}>
                <kbd className={styles["kbd"]}>Enter</kbd> {mode === "answer" ? "answer" : "send"} <kbd className={styles["kbd"]}>⇧ Enter</kbd> new line
              </span>
            </>
          )}
        </span>
        <Button
          type="submit"
          size="sm"
          variant={mode === "prompt" ? "secondary" : "primary"}
          leadingIcon={mode === "answer" ? "hand" : mode === "steer" && interrupt ? "zap" : undefined}
          disabled={!canSubmit}
          loading={busy}
          className={cx(styles["submit"], styles[`submit-${mode}`])}
        >
          {MODE_LABEL[mode]}
        </Button>
      </div>
    </form>
  );
}

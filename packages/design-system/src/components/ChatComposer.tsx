import { useEffect, useId, useRef, useState, type HTMLAttributes, type KeyboardEvent, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { Button } from "../primitives/Button.tsx";
import styles from "./ChatComposer.module.css";

/** The two ways a human intervenes, plus the initial prompt. */
export type ComposerMode = "answer" | "steer" | "prompt";

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
}

export type ComposerSubmission =
  | { readonly mode: "answer"; readonly questionId: string; readonly text: string }
  | { readonly mode: "steer"; readonly text: string }
  | { readonly mode: "prompt"; readonly text: string };

const MODE_LABEL: Record<ComposerMode, string> = {
  answer: "Answer",
  steer: "Steer",
  prompt: "Send",
};
const MODE_PLACEHOLDER: Record<ComposerMode, string> = {
  answer: "Type your answer…",
  steer: "Instruction for the running agent — interrupts the current turn",
  prompt: "Describe the task…",
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
 *   steer   the session is running. The frame is accent-tinted, a line
 *           under the field says plainly that this interrupts the current
 *           turn, and the button says "Steer". Submitting is deliberate:
 *           Cmd/Ctrl+Enter, or the button — plain Enter inserts a newline,
 *           because an accidental interrupt costs a turn.
 *
 * In `answer` mode plain Enter submits: the agent is waiting, and speed is
 * the point. Shift+Enter always inserts a newline.
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
  className,
  ...rest
}: ChatComposerProps) {
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

  // Autosize: 1–8 rows of 22px plus 20px padding.
  useEffect(() => {
    const el = areaRef.current;
    if (!el) return;
    el.style.height = "0px";
    el.style.height = `${Math.min(8 * 22 + 20, Math.max(22 + 20, el.scrollHeight))}px`;
  }, [text]);

  const canSubmit = !disabled && !busy && text.trim().length > 0;

  const submit = async (override?: string) => {
    const t = (override ?? text).trim();
    if (disabled || busy || t.length === 0) return;
    const submission: ComposerSubmission =
      mode === "answer" && question ? { mode: "answer", questionId: question.id, text: t } : mode === "steer" ? { mode: "steer", text: t } : { mode: "prompt", text: t };
    setBusy(true);
    try {
      await onSubmit(submission);
      setText("");
    } finally {
      setBusy(false);
    }
  };

  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key !== "Enter") return;
    if (e.shiftKey) return; // newline
    const modifier = e.metaKey || e.ctrlKey;
    if (mode === "steer" && !modifier) return; // steer needs a deliberate submit
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
        <span id={`${id}-hint`} className={cx(styles["hint"], mode === "steer" && styles["hintSteer"], answerOptions.length > 0 && "ds-sr-only")}>
          {isDisabled ? null : mode === "steer" ? (
            <>
              <Icon name="zap" size={11} /> Interrupts the current turn. <kbd className={styles["kbd"]}>⌘</kbd>
              <kbd className={styles["kbd"]}>Enter</kbd> to send
            </>
          ) : mode === "answer" ? (
            <>
              <kbd className={styles["kbd"]}>Enter</kbd> to answer · <kbd className={styles["kbd"]}>Shift</kbd>
              <kbd className={styles["kbd"]}>Enter</kbd> for a new line
            </>
          ) : (
            <>
              <kbd className={styles["kbd"]}>Enter</kbd> to send
            </>
          )}
        </span>
        <span className={styles["spacer"]} />
        <Button
          type="submit"
          size="sm"
          variant={mode === "prompt" ? "secondary" : "primary"}
          leadingIcon={mode === "steer" ? "zap" : mode === "answer" ? "hand" : "send"}
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

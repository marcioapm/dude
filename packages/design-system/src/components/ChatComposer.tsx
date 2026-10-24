import { useEffect, useId, useRef, useState, type ClipboardEvent, type HTMLAttributes, type KeyboardEvent, type MutableRefObject, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { Button, IconButton } from "../primitives/Button.tsx";
import { Tooltip } from "../primitives/Tooltip.tsx";
import { AttachmentChip, attachmentWarning, takePastedFiles, type ComposerAttachment } from "./ImageAttachments.tsx";
import { MentionMenu, mentionOptionId, type MentionItem } from "./SessionAccess.tsx";
import styles from "./ChatComposer.module.css";
import trayStyles from "./ImageAttachments.module.css";

/**
 * The ways a human intervenes, plus the initial prompt, plus `chat`:
 * talking with a task's conductor, which is a conversation, not an
 * intervention in someone's turn. An answer is not one of them: the
 * question's own turn is its form (`QuestionCard`).
 */
export type ComposerMode = "steer" | "prompt" | "chat";

export interface ChatComposerProps extends Omit<HTMLAttributes<HTMLFormElement>, "onSubmit" | "children"> {
  /**
   * Explicit mode. When omitted: `steer` if the session is running,
   * `prompt` otherwise.
   */
  readonly mode?: ComposerMode | undefined;
  /**
   * Who waits for the reader's answer to a question ("Implement"): the
   * composer steps back to one quiet line saying the answer goes in the
   * question above, with "Write to the agent instead", which brings the
   * composer back for a message that does not answer.
   */
  readonly waitingFor?: string | undefined;
  /** The question waited on (its id): "Write to the agent instead" lasts while it stays the same. Unset, the asker's label stands in. */
  readonly waitingKey?: string | undefined;
  /** "Write to the agent instead" was pressed: the composer is back for this question. */
  readonly onWriteInstead?: (() => void) | undefined;
  /** The session is running; a steer will interrupt it. */
  readonly running?: boolean | undefined;
  /** Nothing accepts input (terminal session). */
  readonly disabled?: boolean | undefined;
  readonly disabledReason?: string | undefined;
  readonly placeholder?: string | undefined;
  readonly value?: string | undefined;
  readonly defaultValue?: string | undefined;
  readonly onValueChange?: ((value: string) => void) | undefined;
  /**
   * Sends it. The text is cleared only once the submission is confirmed:
   * resolving `false`, or rejecting, leaves the words in the composer to
   * send again. Showing why is the caller's (a rejection is caught here,
   * never left unhandled).
   */
  readonly onSubmit: (submission: ComposerSubmission) => void | boolean | Promise<void | boolean>;
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
  /**
   * Chat only: a control about who it goes to, right after `to` — the
   * welcome's `ModelPicker` ("To **Brainstorm** · Claude (High) · Claude
   * Code"). Shown only with `to`.
   */
  readonly toAside?: ReactNode;
  /**
   * Images in the tray, in order: the app reads, scales and uploads them
   * and says how each is doing. Send waits while one uploads and refuses
   * while one cannot be sent; a message may be images alone.
   */
  readonly attachments?: ReadonlyArray<ComposerAttachment> | undefined;
  /**
   * Files picked with the paperclip or pasted into the field. Set, the
   * paperclip shows (before `leading`) and pasting an image attaches it.
   */
  readonly onAttachFiles?: ((files: File[]) => void) | undefined;
  readonly onRemoveAttachment?: ((id: string) => void) | undefined;
  /** What the file picker offers ("image/png,image/jpeg,…"). */
  readonly attachAccept?: string | undefined;
  /** The paperclip's tooltip: what may be attached, and the limits. */
  readonly attachHint?: ReactNode;
  /** Set, the paperclip is off and says why ("Image storage isn't set up"). */
  readonly attachDisabledReason?: string | undefined;
  /**
   * `stage`: the page's one thing (the welcome). Raised off the page
   * (`shadow-2`), a 72px field (52 compact) in `text-prose`, and no inset of
   * its own: its host is the frame.
   */
  readonly variant?: "stage" | undefined;
  /**
   * People who can be @-mentioned; unset, "@" is plain text. An `outside`
   * one (a private session's non-member) is offered locked: picking them
   * writes the name but leaves them out of `mentions`.
   */
  readonly mentionables?: ReadonlyArray<MentionItem> | undefined;
  /** Someone was picked from the menu; for an `outside` one the caller asks what to do (`below`). */
  readonly onMention?: ((item: MentionItem) => void) | undefined;
  /** The menu's heading: "Mention someone in Acme · they're notified". */
  readonly mentionHeading?: ReactNode;
  /** Under the field, inside the composer: a pending mention's confirm. */
  readonly below?: ReactNode;
  /**
   * Set by the composer: sends what is written now, as Send does, with
   * `shareWith` (people the owner shares the session with as it is sent,
   * mentioned too while their name is still in the text). For the
   * caller's own buttons in `below`.
   */
  readonly submitRef?: MutableRefObject<((extra?: { readonly shareWith?: ReadonlyArray<string> }) => void) | null> | undefined;
}

export type ComposerSubmission =
  | { readonly mode: "steer"; readonly text: string; readonly interrupt: boolean; readonly attachmentIds: ReadonlyArray<string> }
  | { readonly mode: "prompt"; readonly text: string; readonly attachmentIds: ReadonlyArray<string> }
  | {
    readonly mode: "chat"; readonly text: string; readonly attachmentIds: ReadonlyArray<string>;
    /** The people picked from the menu whose "@Name" is still in the text, by id. */
    readonly mentions?: ReadonlyArray<string>;
    /** People the session is shared with as this is sent (`submitRef`). */
    readonly shareWith?: ReadonlyArray<string>;
  };

/** The open mention menu: where its "@" is in the text, and what follows it up to the caret. */
interface MentionQuery {
  readonly at: number;
  readonly query: string;
}

/** The "@" word the caret ends, if one is being typed: "@" at the start or after a space, then up to two words. */
export function mentionQueryAt(text: string, caret: number): MentionQuery | null {
  const before = text.slice(0, caret);
  const at = before.lastIndexOf("@");
  if (at < 0 || (at > 0 && !/\s/.test(before[at - 1]!))) return null;
  const query = before.slice(at + 1);
  if (query.length > 40 || /\n/.test(query) || /\s\S*\s/.test(query.trimStart())) return null;
  return { at, query };
}

/** Whether every word typed starts some word of the name, ignoring case and accents. */
export function nameMatches(name: string, query: string): boolean {
  const fold = (s: string) => s.normalize("NFD").replace(/\p{M}/gu, "").toLowerCase();
  const words = fold(name).split(/\s+/);
  return fold(query).split(/\s+/).filter(Boolean).every((q) => words.some((w) => w.startsWith(q)));
}

const MODE_LABEL: Record<ComposerMode, string> = {
  steer: "Steer",
  prompt: "Send",
  chat: "Send",
};
const MODE_PLACEHOLDER: Record<ComposerMode, string> = {
  steer: "Steer the agent…",
  prompt: "Describe the task…",
  chat: "Ask about this task…",
};

/**
 * The human's input. The modes are distinct on purpose:
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
 * While the agent waits on the reader's answer (`waitingFor`) the
 * composer steps back to one line: the question's turn above is where
 * the answer goes. "Write to the agent instead" brings it back, for a
 * message that leaves the question open.
 *
 * Enter sends in every mode; Shift+Enter always inserts a newline. The
 * action row says who it is sent as.
 */
export function ChatComposer({
  mode: modeProp,
  waitingFor,
  waitingKey,
  onWriteInstead,
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
  toAside,
  attachments = [],
  onAttachFiles,
  onRemoveAttachment,
  attachAccept,
  attachHint,
  attachDisabledReason,
  variant,
  mentionables,
  onMention,
  mentionHeading,
  below,
  submitRef,
  className,
  ...rest
}: ChatComposerProps) {
  const [interrupt, setInterrupt] = useState(false);
  const mode: ComposerMode = modeProp ?? (running ? "steer" : "prompt");
  // "Write to the agent instead", for the question it was pressed on: the next question steps back again.
  const [writingFor, setWritingFor] = useState<string | null>(null);
  // Without a waitingKey the asker's label stands in, so the wait ending is what lets the next question step back.
  useEffect(() => {
    if (waitingFor === undefined) setWritingFor(null);
  }, [waitingFor]);
  const waitedOn = waitingKey ?? waitingFor;
  const stepsBack = waitingFor !== undefined && writingFor !== waitedOn;
  const [internal, setInternal] = useState(defaultValue ?? "");
  const text = value ?? internal;
  const [busy, setBusy] = useState(false);
  const areaRef = useRef<HTMLTextAreaElement>(null);
  const id = useId();

  const setText = (v: string) => {
    if (value === undefined) setInternal(v);
    onValueChange?.(v);
  };

  // @-mentions: who was picked (by id and name), and the menu while an "@" word is typed.
  const [picks, setPicks] = useState<ReadonlyArray<{ readonly id: string; readonly name: string }>>([]);
  const [mention, setMention] = useState<MentionQuery | null>(null);
  const [active, setActive] = useState(0);
  const menuId = `${id}-mentions`;
  const offered = mention && mentionables ? mentionables.filter((m) => nameMatches(m.person.name, mention.query)) : [];
  // A query that matches nobody after a space has moved on from the mention, and one that is a whole name
  // and a space was picked or typed out: the menu closes.
  const whole = mention !== null && /\s$/.test(mention.query)
    && offered.some((m) => m.person.name.toLowerCase() === mention.query.trim().toLowerCase());
  const menuOpen = mention !== null && mentionables !== undefined && !whole && (offered.length > 0 || !/\s$/.test(mention.query));
  const readMention = (el: HTMLTextAreaElement) => {
    if (!mentionables) return;
    const q = mentionQueryAt(el.value, el.selectionStart ?? el.value.length);
    setMention(q);
    if (q?.query !== mention?.query) setActive(0);
  };
  const pick = (item: MentionItem) => {
    if (!mention) return;
    const el = areaRef.current;
    const caret = el?.selectionStart ?? text.length;
    const written = `@${item.person.name} `;
    const next = text.slice(0, mention.at) + written + text.slice(caret);
    setText(next);
    setMention(null);
    if (!item.outside && item.person.id) setPicks((p) => [...p.filter((x) => x.id !== item.person.id), { id: item.person.id!, name: item.person.name }]);
    onMention?.(item);
    const at = mention.at + written.length;
    requestAnimationFrame(() => {
      areaRef.current?.focus();
      areaRef.current?.setSelectionRange(at, at);
    });
  };
  /** The picks still named in the text, and those shared with as it is sent. */
  const mentionsIn = (t: string, shareWith: ReadonlyArray<string>) => {
    const named = new Set(picks.filter((p) => t.includes(`@${p.name}`)).map((p) => p.id));
    for (const sid of shareWith) {
      const who = mentionables?.find((m) => m.person.id === sid);
      if (!who || t.includes(`@${who.person.name}`)) named.add(sid);
    }
    return [...named];
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

  const uploading = attachments.filter((a) => a.state === "uploading").length;
  const invalid = attachments.some((a) => a.state === "error");
  const ready = attachments.filter((a) => a.state === "ready" && a.attachmentId).map((a) => a.attachmentId!);
  const hasContent = text.trim().length > 0 || ready.length > 0;
  const canSubmit = !disabled && !busy && uploading === 0 && !invalid && hasContent;
  // Enter while images upload: sent as soon as they are up.
  const [sendWhenUploaded, setSendWhenUploaded] = useState(false);

  const submit = async (override?: string, extra?: { readonly shareWith?: ReadonlyArray<string> }) => {
    const t = (override ?? text).trim();
    if (disabled || busy || invalid) return;
    if (uploading > 0) {
      if (t.length > 0 || attachments.length > 0) setSendWhenUploaded(true);
      return;
    }
    if (t.length === 0 && ready.length === 0) return;
    const attachmentIds = ready;
    const shareWith = extra?.shareWith ?? [];
    const mentions = mentionables ? mentionsIn(t, shareWith) : [];
    const submission: ComposerSubmission =
      mode === "steer" ? { mode: "steer", text: t, interrupt, attachmentIds }
        : mode === "chat" ? {
          mode: "chat", text: t, attachmentIds,
          ...(mentions.length > 0 ? { mentions } : {}), ...(shareWith.length > 0 ? { shareWith } : {}),
        }
        : { mode: "prompt", text: t, attachmentIds };
    setBusy(true);
    let sent = false;
    try {
      sent = (await onSubmit(submission)) !== false;
    } catch {
      // Not sent: the words stay; the caller says why.
    } finally {
      setBusy(false);
    }
    if (sent) {
      setText("");
      setInterrupt(false);
      setPicks([]);
      setMention(null);
    }
  };
  useEffect(() => {
    if (!submitRef) return;
    submitRef.current = (extra) => void submit(undefined, extra);
    return () => {
      submitRef.current = null;
    };
  });

  useEffect(() => {
    if (!sendWhenUploaded) return;
    if (invalid || disabled) setSendWhenUploaded(false);
    else if (uploading === 0) {
      setSendWhenUploaded(false);
      void submit();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sendWhenUploaded, uploading, invalid, disabled]);

  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (menuOpen) {
      // While the menu is open the keys are its: Enter and Tab pick, Escape closes.
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        if (offered.length > 0) setActive((a) => (a + (e.key === "ArrowDown" ? 1 : offered.length - 1)) % offered.length);
        return;
      }
      if ((e.key === "Enter" || e.key === "Tab") && !e.shiftKey && !e.nativeEvent.isComposing && offered[active]) {
        e.preventDefault();
        pick(offered[active]!);
        return;
      }
      if (e.key === "Escape") {
        e.preventDefault();
        setMention(null);
        return;
      }
    }
    if (e.key !== "Enter" || e.shiftKey || e.nativeEvent.isComposing) return; // Shift+Enter is a new line
    e.preventDefault();
    void submit();
  };

  const attachOff = disabled === true || attachDisabledReason !== undefined;
  const onPaste = (e: ClipboardEvent<HTMLTextAreaElement>) => {
    if (!onAttachFiles || attachOff) return;
    // Pasted files are attached; pasted text still goes into the field.
    takePastedFiles(e, onAttachFiles);
  };
  const picker = useRef<HTMLInputElement>(null);

  const isDisabled = disabled === true;

  if (stepsBack && !isDisabled) {
    return (
      <div className={cx(styles["root"], className)} data-mode="waiting" data-testid="composer-waiting">
        <div className={styles["waitBar"]}>
          <Icon name="hand" size={14} className={styles["waitIcon"]} />
          <span className={styles["waitText"]}><b>{waitingFor} is waiting for your answer above.</b></span>
          <span className={styles["spacer"]} />
          <button type="button" className={styles["writeInstead"]} data-testid="write-instead"
            title="Sends a message instead of answering; the question stays open"
            onClick={() => {
              setWritingFor(waitedOn ?? null);
              onWriteInstead?.();
              requestAnimationFrame(() => areaRef.current?.focus());
            }}>
            Write to the agent instead
          </button>
        </div>
      </div>
    );
  }

  return (
    <form
      className={cx(styles["root"], styles[mode], variant === "stage" && styles["stage"], isDisabled && styles["disabled"], className)}
      data-mode={mode}
      data-variant={variant}
      onSubmit={(e) => {
        e.preventDefault();
        void submit();
      }}
      {...rest}
    >
      {menuOpen ? (
        <MentionMenu id={menuId} items={offered} active={Math.min(active, Math.max(0, offered.length - 1))} query={mention!.query}
          onPick={pick} onHover={setActive} heading={mentionHeading} />
      ) : null}
      <div className={cx(styles["field"], attachments.length > 0 && styles["withTray"])}>
        {attachments.length > 0 ? (
          <div className={trayStyles["tray"]} role="list" aria-label="Images to send">
            {attachments.map((a) => (
              <div role="listitem" key={a.id}><AttachmentChip attachment={a} onRemove={disabled ? undefined : onRemoveAttachment} /></div>
            ))}
          </div>
        ) : null}
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
          onChange={(e) => {
            setText(e.target.value);
            readMention(e.target);
          }}
          onSelect={(e) => readMention(e.currentTarget)}
          onBlur={() => setMention(null)}
          onKeyDown={onKey}
          onPaste={onPaste}
          aria-describedby={`${id}-hint`}
          {...(mentionables ? {
            role: "combobox", "aria-autocomplete": "list" as const, "aria-expanded": menuOpen, "aria-controls": menuId,
            "aria-activedescendant": menuOpen && offered.length > 0 ? mentionOptionId(menuId, Math.min(active, offered.length - 1)) : undefined,
          } : {})}
        />
      </div>
      {below}

      {invalid ? (
        <div className={trayStyles["warning"]} role="status" data-testid="attachment-warning">
          <Icon name="warning" size={14} />
          <span>{attachmentWarning(attachments)}</span>
        </div>
      ) : null}

      <div className={styles["actions"]}>
        {onAttachFiles ? (
          <>
            <Tooltip content={attachDisabledReason ?? attachHint ?? "Attach images"} keepOnPress={attachOff}>
              <span className={styles["attach"]}>
                <IconButton icon="paperclip" label={attachDisabledReason ?? "Attach images (or paste, or drop)"} size="sm"
                  disabled={attachOff} aria-disabled={attachOff} data-testid="attach-button"
                  onClick={() => picker.current?.click()} />
              </span>
            </Tooltip>
            <input ref={picker} type="file" multiple hidden accept={attachAccept} data-testid="attach-input"
              onChange={(e) => {
                const files = Array.from(e.target.files ?? []);
                e.target.value = "";
                if (files.length > 0) onAttachFiles(files);
              }} />
          </>
        ) : null}
        {leading}
        {mode === "chat" && to && !isDisabled ? (
          <>
            <span className={styles["sentAs"]} data-testid="composer-to">{to}</span>
            {toAside}
          </>
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
        <span id={`${id}-hint`} className={styles["hint"]}>
          {isDisabled ? null : (
            <>
              {uploading > 0 ? (
                <span className={styles["lands"]} data-testid="upload-hint">
                  Uploading {uploading} of {attachments.length}…
                </span>
              ) : mode === "steer" && landsHint && !interrupt ? <span className={styles["lands"]} data-testid="lands-hint">{landsHint}</span> : null}
              <span className={cx(styles["hint"], Boolean(landsHint) && mode === "steer" && !interrupt && styles["keys"])}>
                <kbd className={styles["kbd"]}>Enter</kbd> send <kbd className={styles["kbd"]}>⇧ Enter</kbd> new line
              </span>
            </>
          )}
        </span>
        <Button
          type="submit"
          size="sm"
          variant={mode === "prompt" ? "secondary" : "primary"}
          leadingIcon={mode === "steer" && interrupt ? "zap" : undefined}
          disabled={!canSubmit}
          loading={busy}
          className={styles["submit"]}
        >
          {MODE_LABEL[mode]}
        </Button>
      </div>
    </form>
  );
}

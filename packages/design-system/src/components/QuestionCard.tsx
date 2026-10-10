import { useEffect, useId, useRef, useState, type ClipboardEvent, type HTMLAttributes, type KeyboardEvent, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { formatTimestamp } from "../util/format.ts";
import { toMs } from "../util/useNow.ts";
import { Icon } from "../icons/index.tsx";
import { AgentAvatar, ROLE_LABEL, type AvatarKind } from "./AgentAvatar.tsx";
import { Duration } from "./Numbers.tsx";
import { Markdown } from "./Markdown.tsx";
import { Button, IconButton } from "../primitives/Button.tsx";
import { Tooltip } from "../primitives/Tooltip.tsx";
import { AttachmentChip, attachmentWarning, takePastedFiles, type ComposerAttachment } from "./ImageAttachments.tsx";
import styles from "./QuestionCard.module.css";
import trayStyles from "./ImageAttachments.module.css";

export type QuestionState = "waiting" | "answered" | "dismissed";

/** A question the agent asks, or a request (for a repository) it makes. */
export type QuestionKind = "question" | "request";

/** How each kind is spoken of: what it is, what a person does with it, and what the agent waits for. */
const WORDS: Record<QuestionKind, { noun: string; verb: string; awaited: string }> = {
  question: { noun: "a question", verb: "answer", awaited: "an answer" },
  request: { noun: "for a repository", verb: "decide", awaited: "a decision" },
};

/** One choice offered to a question. */
export interface QuestionChoice {
  readonly label: string;
  /** One line of why, in secondary ink under the label. */
  readonly description?: string | undefined;
  /** The asker's suggestion: said quietly ("agent suggests"), never preselected. */
  readonly recommended?: boolean | undefined;
}

/** One question of an ask. */
export interface QuestionItem {
  /** Its tab's name, a few words; unused for a question alone. */
  readonly header?: string | undefined;
  /** The question, as Markdown. */
  readonly question: string;
  readonly choices?: ReadonlyArray<QuestionChoice> | undefined;
  /** Several choices may be ticked. */
  readonly multiple?: boolean | undefined;
}

/** A person's answer to one question: the choices picked, by index, and their own words ("" for none). */
export interface QuestionAnswer {
  readonly choices: ReadonlyArray<number>;
  readonly text: string;
}

/**
 * The form's state, for the app to keep until it is sent (a reload or
 * another screen must not lose the picks): the tab shown, each question's
 * picks and own words (`own` null while "Something else…" is closed), and
 * the note.
 */
export interface QuestionDraft {
  readonly tab: number;
  readonly answers: ReadonlyArray<{ readonly choices: ReadonlyArray<number>; readonly own: string | null }>;
  readonly note: string;
}

export interface QuestionSubmission {
  readonly answers: ReadonlyArray<QuestionAnswer>;
  /** "" for none; only an ask of several has a note. */
  readonly note: string;
  /** The note's images, uploaded. */
  readonly attachmentIds: ReadonlyArray<string>;
}

export interface QuestionCardProps extends Omit<HTMLAttributes<HTMLElement>, "children" | "title" | "onSubmit"> {
  /** Who is asking — the role that is blocked. */
  readonly role: AvatarKind;
  /** The asking session's name when there are several of that role ("implementer-2"). */
  readonly name?: string | undefined;
  /** The question, as Markdown; with `items`, what a line about it says (unused in the turn). */
  readonly text: string;
  /** Choices offered to `text`, when there are no `items`. */
  readonly options?: ReadonlyArray<string> | undefined;
  /** What it asks: one to four questions. Absent, the one question `text` with `options`. */
  readonly items?: ReadonlyArray<QuestionItem> | undefined;
  readonly askedAt?: string | number | Date | undefined;
  /**
   * When a person answered. `null` or `undefined` means nobody has yet: the
   * turn is loud and its wait clock ticks. Once set, the turn settles into
   * the history; with `answers` it is the record of them.
   */
  readonly answeredAt?: string | number | Date | null | undefined;
  /** What was answered, one per question: the settled turn shows each under its question. */
  readonly answers?: ReadonlyArray<QuestionAnswer> | null | undefined;
  /** Who answered, by the name the record gives them ("by Ana", "in Ana's words"). */
  readonly answeredBy?: string | undefined;
  /**
   * The question was never answered and never will be — the session
   * ended, was aborted, or the run was retried. Settles the turn without
   * claiming an answer, so a dead question cannot ring forever.
   */
  readonly dismissed?: boolean | undefined;
  /**
   * Settled without an answer here: what was asked was decided elsewhere,
   * named in a few words ("Decided on the banner"). Settles the turn as
   * `dismissed` does, saying where instead of "No longer needed".
   */
  readonly settledBy?: string | undefined;
  /**
   * A request's one-click choices (`kind="request"`: Approve, Decline).
   * A question is answered through its form (`onSubmit`).
   */
  readonly onChoose?: ((option: string) => void) | undefined;
  /**
   * Makes the waiting question the answer form: choices to pick, "Something
   * else…" for the person's own words, tabs and a review for several.
   * Resolving `false`, or rejecting, keeps the form as it was to send again.
   */
  readonly onSubmit?: ((submission: QuestionSubmission) => void | boolean | Promise<void | boolean>) | undefined;
  /** The form's state to start from (a kept draft). Read once, when it mounts. */
  readonly draft?: QuestionDraft | undefined;
  /** Every change to the form's state, for the app to keep until it is sent. */
  readonly onDraftChange?: ((draft: QuestionDraft) => void) | undefined;
  /**
   * The note's images, as the composer's tray takes them: the app reads,
   * scales and uploads, and says how each is doing. Several questions only.
   */
  readonly attachments?: ReadonlyArray<ComposerAttachment> | undefined;
  readonly onAttachFiles?: ((files: File[]) => void) | undefined;
  readonly onRemoveAttachment?: ((id: string) => void) | undefined;
  readonly attachAccept?: string | undefined;
  readonly attachHint?: ReactNode;
  readonly attachDisabledReason?: string | undefined;
  /**
   * The person it waits on, when that is someone other than the reader:
   * the turn says so, and lists the choices without offering them — only
   * they may answer.
   */
  readonly waitingOn?: string | undefined;
  /**
   * Put to `waitingOn` alone, with nothing to take over (a brainstorm
   * session's question to one member): no "Take over this task" hint.
   */
  readonly onlyThey?: boolean | undefined;
  /** A question to answer (the default), or a request to decide: every word the turn says follows. */
  readonly kind?: QuestionKind | undefined;
  /** Flash once on mount (a question that just arrived). */
  readonly isNew?: boolean | undefined;
}

/** A Date for a timestamp prop, or null when it is missing or does not parse. */
function toDate(v: string | number | Date | null | undefined): Date | null {
  const ms = toMs(v);
  return ms === null ? null : new Date(ms);
}

type Draft = { tab: number; answers: Array<{ choices: number[]; own: string | null }>; note: string };

/** Answered: a pick, or own words that say something. */
function answered(a: Draft["answers"][number] | undefined): boolean {
  return a !== undefined && (a.choices.length > 0 || (a.own !== null && a.own.trim() !== ""));
}

function startDraft(items: ReadonlyArray<QuestionItem>, draft: QuestionDraft | undefined): Draft {
  const n = items.length;
  return {
    tab: Math.min(Math.max(0, draft?.tab ?? 0), n > 1 ? n : 0),
    // A question with no choices is its own words alone: the field is open.
    answers: items.map((it, i) => {
      const d = draft?.answers[i];
      const count = it.choices?.length ?? 0;
      const choices = (d?.choices ?? []).filter((c) => c >= 0 && c < count);
      return { choices: it.multiple ? choices : choices.slice(0, 1), own: count === 0 ? (d?.own ?? "") : (d?.own ?? null) };
    }),
    note: draft?.note ?? "",
  };
}

/** The answer as the record and the review read it: labels picked, then the person's words in quotes, and whose they are. */
function AnswerWords({ item, answer, whose }: { readonly item: QuestionItem; readonly answer: QuestionAnswer; readonly whose: string }) {
  const picked = answer.choices.map((c) => item.choices?.[c]?.label ?? "").filter(Boolean).join("; ");
  const own = answer.text.trim();
  return <>{picked}{picked && own ? "; " : null}{own ? <>“{own}”<small className={styles["ownWords"]}>{whose}</small></> : null}</>;
}

const isField = (el: EventTarget | null) => el instanceof HTMLElement && (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.isContentEditable);
// A focused control the browser activates itself (Enter on Change, Back, a tab, the paperclip).
const isControl = (el: EventTarget | null) => el instanceof HTMLElement && el.closest('button, a[href], [role="tab"]') !== null;

/**
 * An agent's question to a person, as a turn in the transcript — and, while
 * it waits on the reader, the form that answers it. Two states that must
 * not be confused:
 *
 *   waiting   the run is blocked on someone. This is the one loud turn a
 *             transcript is allowed, and it is loud once: the attention
 *             highlight (tint and bar). With `onSubmit` the turn is the
 *             answer form: one question's choices answer in one click; a
 *             question with no choices, or "Something else…", is a field in
 *             place; several questions are tabs, each answered then Next,
 *             and a last tab that reviews them all, with a note, and sends
 *             them together. In grayscale it is still the only barred,
 *             tinted turn.
 *
 *   answered  history. The wash and bar go; "✓ Answered · by Ana, after
 *             4m 12s", and each question with its answer right under it.
 *             Nothing in the transcript says it twice.
 */
export function QuestionCard({
  role, name, text, options, items: itemsProp, askedAt, answeredAt, answers, answeredBy, dismissed, settledBy, onChoose, onSubmit,
  draft, onDraftChange, attachments = [], onAttachFiles, onRemoveAttachment, attachAccept, attachHint, attachDisabledReason,
  waitingOn, onlyThey, kind = "question", isNew, className, ...rest
}: QuestionCardProps) {
  const words = WORDS[kind];
  const takeOver = onlyThey ? `Only ${waitingOn} can ${words.verb} this one` : `Take over this task to ${words.verb}`;
  const answeredDate = toDate(answeredAt);
  const state: QuestionState = answeredDate ? "answered" : dismissed || settledBy ? "dismissed" : "waiting";
  const waiting = state === "waiting";
  const asked = toDate(askedAt);
  const who = name ?? ROLE_LABEL[role];
  const someoneElse = waiting && waitingOn !== undefined;
  const items: ReadonlyArray<QuestionItem> = itemsProp && itemsProp.length > 0 ? itemsProp
    : [{ question: text, choices: (options ?? []).map((label) => ({ label })) }];
  const n = items.length;
  const form = waiting && !someoneElse && kind === "question" && onSubmit !== undefined;
  const record = state === "answered" && answers != null && answers.length === n && kind === "question";
  const verb = n > 1 ? `${waiting ? "asks" : "asked"} ${n} questions` : waiting ? "asks" : "asked";

  return (
    <article
      className={cx(styles["root"], styles[state], isNew && styles["new"], className)}
      data-state={state}
      data-role={role}
      data-questions={n}
      aria-label={someoneElse ? `${who} asks ${n > 1 ? `${n} questions` : words.noun} and is waiting for ${waitingOn} to ${words.verb}`
        : waiting ? `${who} asks ${n > 1 ? `${n} questions` : words.noun} and is waiting for ${words.awaited}`
        : state === "dismissed" ? (settledBy ? `${who} asked a question, settled elsewhere: ${settledBy}`
          : `${who} asked a question that is no longer needed: its run ended`) : `${who} asked ${n > 1 ? `${n} questions` : "a question"}`}
      {...rest}
    >
      <div className={styles["gutter"]}>
        <AgentAvatar role={role} size="chat" live={waiting} />
      </div>
      <div className={styles["main"]}>
        <header className={styles["header"]}>
          <span className={styles["name"]}>{who}</span>
          {name ? <span className={styles["roleName"]}>{ROLE_LABEL[role]}</span> : null}
          <span className={styles["verb"]}>{verb}</span>
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
            ) : answeredDate ? (
              <span className={styles["waited"]} title={`Answered at ${answeredDate.toISOString()}`}>
                {answeredBy ? `by ${answeredBy}, ` : ""}after <Duration since={asked} until={answeredDate} />
              </span>
            ) : null
          ) : null}
          {asked ? (
            <time className={styles["time"]} dateTime={asked.toISOString()} title={`Asked at ${asked.toISOString()}`}>
              {formatTimestamp(asked, "time")}
            </time>
          ) : null}
        </header>
        {form ? (
          <AnswerForm items={items} onSubmit={onSubmit} draft={draft} onDraftChange={onDraftChange} attachments={attachments}
            onAttachFiles={onAttachFiles} onRemoveAttachment={onRemoveAttachment} attachAccept={attachAccept} attachHint={attachHint}
            attachDisabledReason={attachDisabledReason} />
        ) : record ? (
          <Record items={items} answers={answers} answeredBy={answeredBy} />
        ) : (
          <Offered items={items} kind={kind} someoneElse={someoneElse} onChoose={state === "waiting" && !someoneElse ? onChoose : undefined}
            waitingOn={waitingOn} takeOver={takeOver} verb={words.verb} />
        )}
      </div>
    </article>
  );
}

/** Each question with its answer right under it: the turn once answered. */
function Record({ items, answers, answeredBy }: { readonly items: ReadonlyArray<QuestionItem>; readonly answers: ReadonlyArray<QuestionAnswer>; readonly answeredBy: string | undefined }) {
  return (
    <ol className={styles["record"]} data-testid="question-record">
      {items.map((item, i) => (
        <li key={i} className={styles["recordItem"]}>
          <Markdown source={item.question} unmeasured className={styles["recordQuestion"]} />
          <p className={styles["recordAnswer"]} data-testid="record-answer">
            <AnswerWords item={item} answer={answers[i]!} whose={answeredBy ? `in ${answeredBy}'s words` : "in their own words"} />
          </p>
        </li>
      ))}
    </ol>
  );
}

/** A turn with no form: what was asked, and the choices as the record of what was offered (or a request's buttons). */
function Offered({ items, kind, someoneElse, onChoose, waitingOn, takeOver, verb }: {
  readonly items: ReadonlyArray<QuestionItem>; readonly kind: QuestionKind; readonly someoneElse: boolean;
  readonly onChoose: ((option: string) => void) | undefined; readonly waitingOn: string | undefined; readonly takeOver: string; readonly verb: string;
}) {
  const several = items.length > 1;
  return (
    <>
      {items.map((item, i) => {
        const labels = (item.choices ?? []).map((c) => c.label);
        const clickable = onChoose !== undefined && kind === "request";
        const list = labels.length > 0 ? (
          <ul className={cx(styles["options"], someoneElse && styles["optionsElse"])}
            aria-label={clickable ? "Reply with one of" : "Choices offered"} data-testid={someoneElse ? "choices-someone-else" : undefined}>
            {labels.map((o, k) => (
              <li key={k} className={styles["optionItem"]}>
                {clickable ? (
                  <button type="button" className={cx(styles["option"], styles["optionButton"])} onClick={() => onChoose(o)}>
                    <span className={styles["optionIndex"]}>{k + 1}</span>
                    {o}
                  </button>
                ) : (
                  <span className={styles["option"]}>
                    <span className={styles["optionIndex"]}>{k + 1}</span>
                    {o}
                  </span>
                )}
              </li>
            ))}
          </ul>
        ) : null;
        return (
          <div key={i} className={cx(several && styles["offeredItem"])}>
            {several && item.header ? <div className={styles["offeredHeader"]}>{item.header}</div> : null}
            <Markdown source={item.question} unmeasured className={styles["body"]} />
            {/* Someone else's: shown but not offered. With a mouse, hovering them says it
                again, and a press (which does nothing) leaves those words up. */}
            {list && someoneElse ? <Tooltip content={takeOver} side="bottom" keepOnPress>{list}</Tooltip> : list}
          </div>
        );
      })}
      {someoneElse ? (
        <p className={styles["waitingOn"]} data-testid="waiting-on">
          <Icon name="hand" size={12} />
          Waiting for {waitingOn} to {verb}
          {/* How to make it yours, in words everyone sees: mouse, keyboard, touch, screen reader. */}
          {" "}<span className={styles["takeOver"]} data-testid="take-over">· {takeOver}</span>
        </p>
      ) : null}
    </>
  );
}

interface AnswerFormProps {
  readonly items: ReadonlyArray<QuestionItem>;
  readonly onSubmit: NonNullable<QuestionCardProps["onSubmit"]>;
  readonly draft: QuestionDraft | undefined;
  readonly onDraftChange: QuestionCardProps["onDraftChange"];
  readonly attachments: ReadonlyArray<ComposerAttachment>;
  readonly onAttachFiles: QuestionCardProps["onAttachFiles"];
  readonly onRemoveAttachment: QuestionCardProps["onRemoveAttachment"];
  readonly attachAccept: string | undefined;
  readonly attachHint: ReactNode;
  readonly attachDisabledReason: string | undefined;
}

/**
 * The form inside the waiting turn. One question: a choice answers in one
 * click; own words (or a `multiple` question) answer with Answer. Several:
 * a tab each and a last one to review and Send, which is off until every
 * question is answered. Keys, outside a field and off the form's buttons:
 * 1–9 pick, ←/→ move between questions, Enter is Next / Send; ↑/↓ move
 * within a radio group, which is one tab stop.
 */
function AnswerForm({ items, onSubmit, draft, onDraftChange, attachments, onAttachFiles, onRemoveAttachment, attachAccept, attachHint, attachDisabledReason }: AnswerFormProps) {
  const n = items.length;
  const single = n === 1;
  const [st, setSt] = useState<Draft>(() => startDraft(items, draft));
  const [busy, setBusy] = useState(false);
  const id = useId();
  const root = useRef<HTMLFormElement>(null);
  // Where focus goes after a render that moved it: the field just opened, or the new tab's first choice.
  const focusNext = useRef<"own" | "tab" | "tabButton" | null>(null);
  const picker = useRef<HTMLInputElement>(null);

  const update = (next: Draft) => {
    setSt(next);
    onDraftChange?.({ tab: next.tab, answers: next.answers.map((a) => ({ choices: [...a.choices], own: a.own })), note: next.note });
  };

  useEffect(() => {
    const el = root.current;
    if (!el || !focusNext.current) return;
    const target = focusNext.current === "own" ? el.querySelector<HTMLElement>("[data-own-field]")
      : focusNext.current === "tabButton" ? el.querySelector<HTMLElement>('[role="tab"][aria-selected="true"]')
      : (el.querySelector<HTMLElement>('[role="radio"][aria-checked="true"], [role="checkbox"]') ?? el.querySelector<HTMLElement>('[role="radio"], [data-own-field], textarea')
        ?? el.querySelector<HTMLElement>("[data-panel]"));
    focusNext.current = null;
    target?.focus();
  });

  const allDone = st.answers.every(answered);
  const uploading = attachments.some((a) => a.state === "uploading");
  const invalid = attachments.some((a) => a.state === "error");
  const ready = attachments.filter((a) => a.state === "ready" && a.attachmentId).map((a) => a.attachmentId!);

  const send = async (draftNow: Draft) => {
    if (busy || !draftNow.answers.every(answered) || uploading || invalid) return;
    const out: QuestionAnswer[] = draftNow.answers.map((a) => ({ choices: [...a.choices], text: a.own?.trim() ?? "" }));
    setBusy(true);
    try {
      await onSubmit({ answers: out, note: single ? "" : draftNow.note.trim(), attachmentIds: single ? [] : ready });
    } catch {
      // Not sent: the form stays as it was; the app says why.
    } finally {
      setBusy(false);
    }
  };

  const goTo = (tab: number) => {
    focusNext.current = "tab";
    update({ ...st, tab: Math.max(0, Math.min(n, tab)) });
  };

  const pick = (k: number) => {
    const i = st.tab;
    const item = items[i]!;
    const count = item.choices?.length ?? 0;
    const a = st.answers[i]!;
    let next: Draft["answers"][number];
    if (k === count) {
      // "Something else…": for one answer it replaces the pick; for several it adds to them.
      if (a.own !== null && item.multiple) next = { ...a, own: null };
      else {
        next = { choices: item.multiple ? a.choices : [], own: a.own ?? "" };
        focusNext.current = "own";
      }
    } else if (item.multiple) {
      next = { ...a, choices: a.choices.includes(k) ? a.choices.filter((c) => c !== k) : [...a.choices, k].sort((x, y) => x - y) };
    } else {
      next = { choices: [k], own: count === 0 ? "" : null };
    }
    const answers = st.answers.map((x, j) => (j === i ? next : x));
    const updated = { ...st, answers };
    update(updated);
    // One question, one answer: a click on a choice is the answer.
    if (single && !item.multiple && k < count) void send(updated);
  };

  const setOwn = (value: string) => update({ ...st, answers: st.answers.map((x, j) => (j === st.tab ? { ...x, own: value } : x)) });

  const next = () => {
    if (single) void send(st);
    else if (st.tab < n) goTo(st.tab + 1);
    else void send(st);
  };

  const onKey = (e: KeyboardEvent<HTMLFormElement>) => {
    if (e.defaultPrevented || e.altKey || e.ctrlKey || e.metaKey || e.nativeEvent.isComposing) return;
    if (isField(e.target)) return;
    if (e.target instanceof HTMLElement && e.target.getAttribute("role") === "tab" && (e.key === "ArrowRight" || e.key === "ArrowLeft")) {
      // On the tab list, the arrows move between tabs and focus stays on the tabs.
      e.preventDefault();
      const to = Math.max(0, Math.min(n, st.tab + (e.key === "ArrowRight" ? 1 : -1)));
      update({ ...st, tab: to });
      focusNext.current = "tabButton";
      return;
    }
    if (isControl(e.target)) return;
    if (/^[1-9]$/.test(e.key) && st.tab < n) {
      const k = Number(e.key) - 1;
      if (k <= (items[st.tab]!.choices?.length ?? 0)) {
        e.preventDefault();
        pick(k);
      }
    } else if (!single && e.key === "ArrowRight" && st.tab < n) {
      e.preventDefault();
      goTo(st.tab + 1);
    } else if (!single && e.key === "ArrowLeft" && st.tab > 0) {
      e.preventDefault();
      goTo(st.tab - 1);
    } else if (e.key === "Enter") {
      e.preventDefault();
      if (st.tab === n || answered(st.answers[st.tab]) || !single) next();
    }
  };

  const done = st.answers.filter(answered).length;
  const tabId = (i: number) => `${id}-tab-${i}`;
  const panelId = `${id}-panel`;
  const questionId = `${id}-q`;

  return (
    <form ref={root} className={styles["form"]} data-testid="question-form" onKeyDown={onKey}
      aria-labelledby={st.tab < n ? questionId : tabId(n)}
      onSubmit={(e) => {
        e.preventDefault();
        next();
      }}>
      {single ? null : (
        <div role="tablist" aria-label="Questions" className={styles["tabs"]} data-testid="question-tabs">
          {items.map((item, i) => (
            <button key={i} type="button" role="tab" id={tabId(i)} aria-selected={st.tab === i} aria-controls={panelId}
              tabIndex={st.tab === i ? 0 : -1} className={cx(styles["tab"], st.tab === i && styles["tabOn"], answered(st.answers[i]) && styles["tabDone"])}
              data-done={answered(st.answers[i]) || undefined} onClick={() => goTo(i)}>
              <span className={styles["tabMark"]} aria-hidden>
                {answered(st.answers[i]) ? <Icon name="check" size={9} strokeWidth={2.5} /> : null}
              </span>
              <span className={styles["tabName"]}>{item.header}</span>
              {answered(st.answers[i]) ? <span className="ds-sr-only"> (answered)</span> : null}
            </button>
          ))}
          <button type="button" role="tab" id={tabId(n)} aria-selected={st.tab === n} aria-controls={panelId} tabIndex={st.tab === n ? 0 : -1}
            className={cx(styles["tab"], styles["tabSend"], st.tab === n && styles["tabOn"])} data-testid="send-tab" onClick={() => goTo(n)}>
            Send · {done}/{n}
          </button>
        </div>
      )}
      <div id={panelId} role={single ? undefined : "tabpanel"} aria-labelledby={single ? undefined : tabId(st.tab)} data-panel tabIndex={-1}
        className={styles["panel"]}>
        {st.tab < n ? (
          <ItemPanel item={items[st.tab]!} answer={st.answers[st.tab]!} questionId={questionId} single={single} busy={busy}
            onPick={pick} onOwn={setOwn}
            onOwnEnter={() => {
              if (answered(st.answers[st.tab])) next();
            }} />
        ) : (
          <Review items={items} answers={st.answers} onChange={goTo} />
        )}
        {!single && st.tab === n ? (
          <NoteField note={st.note} onNote={(note) => update({ ...st, note })} onSend={() => void send(st)} attachments={attachments}
            onAttachFiles={onAttachFiles} onRemoveAttachment={onRemoveAttachment} picker={picker} attachAccept={attachAccept}
            attachHint={attachHint} attachDisabledReason={attachDisabledReason} />
        ) : null}
        <div className={styles["foot"]}>
          <span className={styles["hint"]} data-testid="question-hint">
            {single
              ? items[0]!.multiple ? "Pick any, then Answer" : (items[0]!.choices?.length ?? 0) > 0 && st.answers[0]!.own === null ? "One click answers." : null
              : st.tab === n
                ? (allDone ? "They go to the agent together, as one message" : `${n - done} still to answer`)
                : items[st.tab]!.multiple ? "Pick any that apply" : null}
          </span>
          <span className={styles["spacer"]} />
          {!single && st.tab > 0 ? (
            <Button type="button" size="sm" variant="quiet" onClick={() => goTo(st.tab - 1)} data-testid="question-back">Back</Button>
          ) : null}
          {single ? (
            items[0]!.multiple || st.answers[0]!.own !== null ? (
              <Button type="submit" size="sm" variant="primary" className={styles["answerButton"]} leadingIcon="hand"
                disabled={!answered(st.answers[0])} loading={busy} data-testid="question-answer">Answer</Button>
            ) : null
          ) : st.tab < n ? (
            <Button type="submit" size="sm" variant={answered(st.answers[st.tab]) ? "primary" : "secondary"}
              className={cx(answered(st.answers[st.tab]) && styles["answerButton"])} data-testid="question-next">
              {st.tab === n - 1 ? "Review" : "Next"}
            </Button>
          ) : (
            <Button type="submit" size="sm" variant="primary" className={styles["answerButton"]} leadingIcon="hand"
              disabled={!allDone || uploading || invalid} loading={busy} data-testid="question-send">Send answers</Button>
          )}
        </div>
      </div>
    </form>
  );
}

/** One question's choices, each a row with its mark; "Something else…" last, opening a field in place. */
function ItemPanel({ item, answer, questionId, single, busy, onPick, onOwn, onOwnEnter }: {
  readonly item: QuestionItem; readonly answer: Draft["answers"][number]; readonly questionId: string; readonly single: boolean; readonly busy: boolean;
  readonly onPick: (k: number) => void; readonly onOwn: (value: string) => void; readonly onOwnEnter: () => void;
}) {
  const choices = item.choices ?? [];
  const multiple = item.multiple === true;
  const role = multiple ? "checkbox" : "radio";
  const ownOpen = answer.own !== null;
  // A radio group is one tab stop: the picked row, or the first. Checkboxes are a stop each.
  const stop = ownOpen ? choices.length : (answer.choices[0] ?? 0);
  const tabIndexOf = (k: number) => (multiple || k === stop ? 0 : -1);
  // ↑/↓ move focus between the rows without picking: a pick on one question sends it.
  const onArrows = (e: KeyboardEvent<HTMLDivElement>) => {
    if ((e.key !== "ArrowDown" && e.key !== "ArrowUp") || isField(e.target)) return;
    const rows = [...e.currentTarget.querySelectorAll<HTMLElement>("[data-choice]")];
    const at = rows.findIndex((r) => r.contains(e.target as Node));
    if (at < 0) return;
    e.preventDefault();
    rows[(at + (e.key === "ArrowDown" ? 1 : rows.length - 1)) % rows.length]!.focus();
  };
  const field = (
    <input data-own-field className={styles["ownField"]} value={answer.own ?? ""} maxLength={4000} disabled={busy}
      aria-label={choices.length > 0 ? "Something else, in your own words" : "Your answer"}
      placeholder={choices.length > 0 ? `Say it your way — Enter ${single ? "to send" : "for the next question"}` : `Your answer — Enter ${single ? "to send" : "for the next question"}`}
      onChange={(e) => onOwn(e.target.value)}
      onKeyDown={(e) => {
        if (e.key !== "Enter" || e.nativeEvent.isComposing) return;
        e.preventDefault();
        e.stopPropagation();
        onOwnEnter();
      }} />
  );
  return (
    <>
      <Markdown source={item.question} unmeasured className={styles["qtext"]} id={questionId} />
      {multiple ? <div className={styles["multiNote"]}>Pick any that apply</div> : null}
      {choices.length === 0 ? (
        <div className={styles["ownAlone"]}>{field}</div>
      ) : (
        <div role={multiple ? "group" : "radiogroup"} aria-labelledby={questionId} className={styles["choices"]} onKeyDown={onArrows}>
          {choices.map((c, k) => {
            const on = answer.choices.includes(k);
            return (
              <div key={k} role={role} aria-checked={on} tabIndex={tabIndexOf(k)} aria-disabled={busy || undefined}
                className={cx(styles["choice"], on && styles["choiceOn"])} data-choice={k}
                onClick={() => !busy && onPick(k)}
                onKeyDown={(e) => {
                  if (e.key === " ") {
                    e.preventDefault();
                    if (!busy) onPick(k);
                  }
                }}>
                <span className={cx(styles["mark"], multiple ? styles["markBox"] : styles["markDot"])} aria-hidden />
                <span className={styles["choiceText"]}>
                  <span className={styles["choiceLabel"]}>{c.label}</span>
                  {c.description ? <span className={styles["choiceWhy"]}>{c.description}</span> : null}
                </span>
                {c.recommended ? <span className={styles["suggests"]}>agent suggests</span> : <span className={styles["key"]} aria-hidden>{k + 1}</span>}
              </div>
            );
          })}
          <div role={role} aria-checked={ownOpen} tabIndex={tabIndexOf(choices.length)} className={cx(styles["choice"], styles["choiceOwn"], ownOpen && styles["choiceOn"])}
            data-choice="own" aria-disabled={busy || undefined}
            onClick={(e) => {
              if (isField(e.target) || busy) return;
              onPick(choices.length);
            }}
            onKeyDown={(e) => {
              if (e.key === " " && !isField(e.target)) {
                e.preventDefault();
                if (!busy) onPick(choices.length);
              }
            }}>
            <span className={cx(styles["mark"], multiple ? styles["markBox"] : styles["markDot"])} aria-hidden />
            <span className={styles["choiceText"]}>
              <span className={styles["choiceLabel"]}>Something else…</span>
              {ownOpen ? field : null}
            </span>
            <span className={styles["key"]} aria-hidden>{choices.length + 1}</span>
          </div>
        </div>
      )}
    </>
  );
}

/** The last tab: every answer, each with Change; one not answered yet says so. */
function Review({ items, answers, onChange }: { readonly items: ReadonlyArray<QuestionItem>; readonly answers: Draft["answers"]; readonly onChange: (tab: number) => void }) {
  return (
    <ul className={styles["review"]} data-testid="question-review">
      {items.map((item, i) => {
        const a = answers[i]!;
        const ok = answered(a);
        return (
          <li key={i} className={styles["reviewItem"]}>
            <div>
              <Markdown source={item.question} unmeasured className={styles["reviewQuestion"]} />
              <div className={cx(styles["reviewAnswer"], !ok && styles["reviewMissing"])} data-testid="review-answer">
                {ok ? <AnswerWords item={item} answer={{ choices: a.choices, text: a.own ?? "" }} whose="your words" /> : "Not answered yet"}
              </div>
            </div>
            <Button type="button" size="sm" variant="quiet" onClick={() => onChange(i)} aria-label={`${ok ? "Change" : "Answer"}: ${item.header ?? item.question}`}>
              {ok ? "Change" : "Answer"}
            </Button>
          </li>
        );
      })}
    </ul>
  );
}

/** The optional note sent with several answers: words and images, as the composer takes them. */
function NoteField({ note, onNote, onSend, attachments, onAttachFiles, onRemoveAttachment, picker, attachAccept, attachHint, attachDisabledReason }: {
  readonly note: string; readonly onNote: (v: string) => void; readonly onSend: () => void;
  readonly attachments: ReadonlyArray<ComposerAttachment>; readonly onAttachFiles: QuestionCardProps["onAttachFiles"];
  readonly onRemoveAttachment: QuestionCardProps["onRemoveAttachment"]; readonly picker: React.RefObject<HTMLInputElement | null>;
  readonly attachAccept: string | undefined; readonly attachHint: ReactNode; readonly attachDisabledReason: string | undefined;
}) {
  const attachOff = attachDisabledReason !== undefined;
  const onPaste = (e: ClipboardEvent<HTMLTextAreaElement>) => {
    if (onAttachFiles && !attachOff) takePastedFiles(e, onAttachFiles);
  };
  const invalid = attachments.some((a) => a.state === "error");
  return (
    <div className={styles["note"]}>
      <div className={cx(styles["noteField"], attachments.length > 0 && styles["noteWithTray"])}>
        {attachments.length > 0 ? (
          <div className={trayStyles["tray"]} role="list" aria-label="Images to send">
            {attachments.map((a) => <div role="listitem" key={a.id}><AttachmentChip attachment={a} onRemove={onRemoveAttachment} /></div>)}
          </div>
        ) : null}
        <textarea className={styles["noteText"]} rows={1} value={note} maxLength={4000} aria-label="A note for the agent (optional)"
          placeholder="Anything else for the agent? (optional)" data-testid="question-note"
          onChange={(e) => onNote(e.target.value)} onPaste={onPaste}
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
              e.preventDefault();
              onSend();
            }
          }} />
        {onAttachFiles ? (
          <>
            <Tooltip content={attachDisabledReason ?? attachHint ?? "Attach images"} keepOnPress={attachOff}>
              <span className={styles["attach"]}>
                <IconButton icon="paperclip" label={attachDisabledReason ?? "Attach images (or paste)"} size="sm" disabled={attachOff}
                  aria-disabled={attachOff} data-testid="attach-button" onClick={() => picker.current?.click()} />
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
      </div>
      {invalid ? (
        <div className={trayStyles["warning"]} role="status" data-testid="attachment-warning">
          <Icon name="warning" size={14} />
          <span>{attachmentWarning(attachments)}</span>
        </div>
      ) : null}
    </div>
  );
}

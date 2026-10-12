/*
 * Agents on an epic (docs/design/agent-coordination.md): a task named
 * inline (TaskChip), a message between two tasks' agents (PeerMessage) and
 * their thread (PeerThread), where a Run stopped to wait on a peer
 * (PeerWaitLine), the epic's notes (EpicNote, EpicNoteCount,
 * EpicNoteHistory, EpicNoteRow) and the notice an agent is told of a change
 * (EpicNoteNotice), a conductor's digest (SinceDigest), agents waiting on
 * each other (PeerWaitMark), and what `list_tasks --peers` returns
 * (PeerList).
 *
 * A peer is told by who: its role colour on a rail and its role face.
 * Nothing here takes the accent (a person's steer) or the attention tone
 * (needs you), and no peer wears a person's face.
 */

import { useId, useState, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { formatTimestamp } from "../util/format.ts";
import { Icon } from "../icons/index.tsx";
import type { AgentRoleName } from "../tokens/palette.ts";
import type { TaskStatus } from "../tokens/status.ts";
import { Button } from "../primitives/Button.tsx";
import { AgentAvatar, ROLE_LABEL } from "./AgentAvatar.tsx";
import { Markdown } from "./Markdown.tsx";
import { PersonAvatar, type Person } from "./PersonAvatar.tsx";
import { StatusMark } from "./StatusMark.tsx";
import styles from "./Peer.module.css";

type When = Date | string | number;

const at = (d: When) => (d instanceof Date ? d : new Date(d));
const clock = (d: When) => formatTimestamp(at(d), "time-short");
const iso = (d: When) => at(d).toISOString();

/** A task as a peer names it: its key, and what is known of it at the moment shown. */
export interface PeerTask {
  readonly key: string;
  readonly title?: string | undefined;
  readonly status?: TaskStatus | undefined;
  /** The agent speaking for it. */
  readonly role?: AgentRoleName | undefined;
  /** That agent's Run is live. */
  readonly live?: boolean | undefined;
}

// --------------------------------------------------------------------------- TaskChip

export interface TaskChipProps {
  readonly task: PeerTask;
  /** The role's word after the face (thread heads); inline mentions leave it to the face. */
  readonly showRole?: boolean | undefined;
  /** Opens the task: the chip is a button. Without it, an image with the same name. */
  readonly onOpen?: (() => void) | undefined;
  readonly className?: string | undefined;
}

/** A task named inline: its status mark, its key in mono, its live agent's face. */
export function TaskChip({ task, showRole, onOpen, className }: TaskChipProps) {
  const role = task.role;
  const inner = (
    <>
      {task.status ? <StatusMark status={task.status} iconOnly size="sm" /> : null}
      <span className={styles["taskKey"]}>{task.key}</span>
      {role ? <AgentAvatar role={role} size="xs" live={task.live} /> : null}
      {role && showRole ? <span className={styles["taskChipRole"]}>{ROLE_LABEL[role]}</span> : null}
    </>
  );
  const label = [task.key, task.title].filter(Boolean).join(", ") + (role ? `: its ${ROLE_LABEL[role].toLowerCase()}` : "");
  return onOpen ? (
    <button type="button" className={cx(styles["taskChip"], className)} onClick={onOpen} aria-label={`Open ${label}`} title={task.title}>{inner}</button>
  ) : (
    <span className={cx(styles["taskChip"], className)} role="img" aria-label={label} title={task.title}>{inner}</span>
  );
}

// --------------------------------------------------------------------------- PeerMessage

/** Where a message is: on its way to the recipient's next step, read there, or held for its next Run (with the orchestrator's notice). */
export type PeerDelivery =
  | { readonly kind: "queued" }
  | { readonly kind: "read"; readonly at: When; readonly after?: string | undefined }
  | { readonly kind: "inbox"; readonly notice?: string | undefined };

export interface PeerMessageData {
  readonly id: string;
  readonly from: PeerTask;
  readonly to: PeerTask;
  readonly text: string;
  readonly sentAt: When;
  readonly delivery: PeerDelivery;
  /** The message it answers: a reply. */
  readonly replyTo?: string | null | undefined;
}

export interface PeerMessageProps {
  readonly msg: PeerMessageData;
  /** The transcript's own task: "You" for its messages, "you" for those to it. */
  readonly viewer?: string | undefined;
  /** In a thread on the Coordination view: a 24px face, the UI body size. */
  readonly compact?: boolean | undefined;
  /** What it answers, quoted on one line. */
  readonly replyToText?: string | undefined;
  /** The sender waits on its reply, in a thread: since when, and a muted line after. */
  readonly waiting?: { readonly since: When; readonly note?: ReactNode } | undefined;
  /** In the sender's transcript: sending returned at once and the agent carried on. */
  readonly carryingOn?: boolean | undefined;
  /** Refused: the reason, in danger ink, where the delivery line would be. */
  readonly refused?: string | undefined;
}

const RAIL: Record<AgentRoleName, string> = {
  conductor: "conductor",
  brainstorm: "conductor",
  investigator: "investigator",
  implementer: "implementer",
  reviewer: "reviewer",
  simplifier: "simplifier",
  qa_browser: "qaBrowser",
};

/**
 * A message from one task's agent to another's, as a turn: the sender's
 * role face and a 2px rail in its role colour (dashed while it is not
 * read), "ABS-4 Implementer → ABS-2", a neutral Peer or Reply tag, and its
 * delivery in a steer's words.
 */
export function PeerMessage({ msg, viewer, compact, replyToText, waiting, carryingOn, refused }: PeerMessageProps) {
  const role = msg.from.role ?? "implementer";
  const outgoing = viewer !== undefined && viewer === msg.from.key;
  const toYou = viewer !== undefined && viewer === msg.to.key;
  const d = msg.delivery;
  const unread = d.kind !== "read" && !refused;
  const titleId = useId();
  const reply = Boolean(msg.replyTo);
  return (
    <article className={cx(styles["peer"], styles[RAIL[role]], compact && styles["compact"])} data-unread={unread ? "true" : undefined}
      data-kind="peer" data-message={msg.id} aria-labelledby={titleId}>
      <div className={styles["gutter"]}>
        <AgentAvatar role={role} size={compact ? "md" : "chat"} />
      </div>
      <div className={styles["main"]}>
        <header className={styles["head"]} id={titleId}>
          <span className={styles["name"]}>{outgoing ? "You" : msg.from.key}</span>
          <span className={styles["arrow"]}>
            {outgoing ? null : <span>{ROLE_LABEL[role]}</span>}
            <Icon name="arrow-right" size={12} title="to" />
            {toYou ? <span>you</span> : <TaskChip task={msg.to} />}
          </span>
          <span className={styles["tag"]}><Icon name="message" size={10} /><span className="ds-cap">{reply ? "Reply" : "Peer"}</span></span>
          {d.kind === "read" ? (
            <span className={styles["time"]}>
              sent <time dateTime={iso(msg.sentAt)}>{clock(msg.sentAt)}</time> · read{" "}
              <time dateTime={iso(d.at)}>{formatTimestamp(at(d.at), "time")}</time>{d.after ? `, after ${d.after}` : null}
            </span>
          ) : (
            <time className={styles["time"]} dateTime={iso(msg.sentAt)}>{formatTimestamp(at(msg.sentAt), "time")}</time>
          )}
        </header>
        {replyToText ? (
          <div className={styles["replyTo"]}>
            <span className={styles["replyToLabel"]}>Replying to</span>
            <span className={styles["replyToText"]}>{replyToText.replace(/`/g, "")}</span>
          </div>
        ) : null}
        <Markdown source={msg.text} unmeasured className={styles["body"]} />
        {refused ? (
          <div className={styles["dangerNote"]} role="status"><Icon name="alert" size={12} /><span><b>Not sent:</b> {refused}</span></div>
        ) : d.kind === "queued" ? (
          <div className={styles["note"]} data-testid="peer-lands"><Icon name="clock" size={12} />
            <span>Lands at <b>{msg.to.key}</b>'s next step. Nothing is interrupted.</span></div>
        ) : d.kind === "inbox" ? (
          <div className={styles["note"]} data-testid="peer-lands"><Icon name="clock" size={12} />
            <span>{d.notice ?? <>Held for <b>{msg.to.key}</b>'s next Run.</>}</span></div>
        ) : null}
        {carryingOn && !refused ? (
          <div className={styles["note"]} data-testid="peer-carrying-on">
            <Icon name="arrow-right" size={12} />
            <span><b>Sent · carrying on.</b></span>
            <span className={styles["noteMuted"]}>The reply lands at your next step; nothing waits on it unless you run <code>dude wait {msg.id}</code>.</span>
          </div>
        ) : null}
        {waiting ? (
          <div className={styles["note"]} data-testid="peer-waiting">
            <Icon name="clock" size={12} />
            <span>{outgoing ? "Your turn ended" : `${msg.from.key}'s turn ended`}: <b>waiting for {toYou ? "your" : `${msg.to.key}'s`} reply</b> since {clock(waiting.since)}.</span>
            {waiting.note ? <span className={styles["noteMuted"]}>{waiting.note}</span> : null}
          </div>
        ) : null}
      </div>
    </article>
  );
}

// --------------------------------------------------------------------------- PeerWaitLine

/** How a wait on a peer ended: its reply, the waiter's own answer, its Run, or the peer's task ending. */
export type PeerWaitEnd =
  | { readonly at: When; readonly by: "reply" | "self" | "run_ended" }
  | { readonly at: When; readonly by: "peer_ended"; readonly status?: string | undefined; readonly line?: string | undefined };

export interface PeerWaitLineProps {
  /** The peer waited on, by key. */
  readonly on: string;
  /** The message waited on. */
  readonly msg: string;
  readonly how: "wait" | "turn-ended";
  readonly since: When;
  /** When the Run parks if no reply comes; unknown, the line does not say. */
  readonly parksAt?: When | null | undefined;
  readonly ended?: PeerWaitEnd | undefined;
  /** The clock "parks in N min" counts down from: the time left, as the Run header says it. */
  readonly now?: When | undefined;
}

function endedWords(on: string, e: PeerWaitEnd): string {
  switch (e.by) {
    case "reply": return "answered; the reply resumed the turn";
    case "self": return "it went ahead on its own assumption";
    case "run_ended": return "its Run ended";
    // The orchestrator's line (PeerEndedLine), as the waiting agent was told it.
    case "peer_ended": return e.line ?? `${on} ended`;
  }
}

/**
 * Where a Run stopped to wait on a peer, in its own transcript: one quiet
 * line on the text column, a clock, neutral ink. It waits on an agent, not
 * on you.
 */
export function PeerWaitLine({ on, msg, how, since, parksAt, ended, now }: PeerWaitLineProps) {
  const why = how === "wait" ? <>ran <code className={styles["noticeCode"]}>dude wait {msg}</code></> : <>turn ended with its question to {on} unanswered</>;
  const parks = parksAt ? at(parksAt) : null;
  const label = ended
    ? `Waited on ${on} from ${clock(since)} to ${clock(ended.at)}: ${endedWords(on, ended)}`
    : `Waiting on ${on} since ${clock(since)}${parks ? `; parks at ${clock(parks)} if no reply` : ""}`;
  return (
    <div className={styles["notice"]} role="status" aria-label={label} data-testid="peer-wait-line" data-state={ended ? ended.by : "waiting"}>
      <span className={styles["noticeGlyph"]}><Icon name="clock" size={12} /></span>
      <div className={styles["noticeBody"]}>
        <span className={styles["noticeLead"]}>{ended ? `Waited on ${on}` : `Waiting on ${on}`}</span>
        <span className={styles["noticeBy"]}> · {why} · </span>
        <span className={styles["noticeText"]}>
          {ended
            ? `${clock(since)} → ${clock(ended.at)}, ${endedWords(on, ended)}`
            : parks
              ? `parks in ${Math.max(0, Math.round((parks.getTime() - (now === undefined ? Date.now() : at(now).getTime())) / 60_000))} min, at ${clock(parks)}, if no reply; the reply resumes it`
              : "the reply resumes it"}
        </span>
      </div>
      <time className={styles["time"]} dateTime={iso(since)}>{formatTimestamp(at(since), "time")}</time>
    </div>
  );
}

// --------------------------------------------------------------------------- EpicNote

/** Who pinned a note: an agent (its task, role, and the person it worked for), or a person. */
export type EpicNoteAuthor =
  | { readonly kind: "agent"; readonly task: string; readonly role: AgentRoleName; readonly for?: Person | undefined }
  | { readonly kind: "person"; readonly person: Person };

export interface EpicNoteData {
  readonly id: string;
  /** Its number on the epic: what "replaces N" names. */
  readonly n: number;
  readonly text: string;
  readonly by: EpicNoteAuthor;
  readonly at: When;
  /** Live agents on the epic that have read it. */
  readonly readBy?: number | undefined;
  readonly replaces?: number | null | undefined;
  /** Set once a newer note replaced it: history. */
  readonly replacedBy?: { readonly n: number; readonly at: When } | null | undefined;
  /** Set once a person retired it: history. */
  readonly retired?: { readonly by: string; readonly at: When } | null | undefined;
  /** A person filed it from a brainstorm session's proposal. */
  readonly fromSession?: boolean | undefined;
}

const first = (name: string) => name.split(" ")[0] ?? name;

function NoteBy({ note }: { readonly note: EpicNoteData }) {
  const b = note.by;
  if (note.fromSession && b.kind === "person") {
    return (
      <span className={styles["noteByWho"]}>
        <Icon name="brainstorm" size={12} />From a brainstorm session, filed by {first(b.person.name)}
      </span>
    );
  }
  if (b.kind === "person") return <span className={styles["noteByWho"]}><PersonAvatar person={b.person} size={16} />{b.person.name}</span>;
  return (
    <span className={styles["noteByWho"]}>
      {b.for ? <PersonAvatar person={b.for} size={16} agent={b.role} /> : <AgentAvatar role={b.role} size="xs" />}
      {ROLE_LABEL[b.role]} on <span className={styles["taskKey"]}>{b.task}</span>
    </span>
  );
}

export interface EpicNoteProps {
  readonly note: EpicNoteData;
  /** The epic's live agents: the "of m" of "read by n of m". */
  readonly live: number;
  readonly isNew?: boolean | undefined;
  /** A person's Replace: pin a new note in its place. */
  readonly onReplace?: (() => void) | undefined;
  /** A person's Retire: take it off with no replacement. Never offered to agents. */
  readonly onRetire?: (() => void) | undefined;
}

/**
 * A fact pinned for every agent on the epic: the pin, the words, its number,
 * who pinned it and when, how many agents have it. Never edited: replaced or
 * retired, it reads as history with no actions.
 */
export function EpicNote({ note, live, isNew, onReplace, onRetire }: EpicNoteProps) {
  const replaced = note.replacedBy;
  const retired = note.retired;
  const gone = Boolean(replaced || retired);
  return (
    <li className={cx(styles["epicNote"], gone && styles["epicNoteGone"])} data-note={note.n} data-testid="epic-note"
      aria-label={`Note ${note.n}`}>
      <span className={styles["pin"]}><Icon name="pin" size={14} /></span>
      <div className={styles["noteMain"]}>
        <Markdown source={note.text} unmeasured className={styles["noteText"]} />
        <div className={styles["noteBy"]}>
          <span className={styles["noteNum"]}>Note {note.n}</span>
          <NoteBy note={note} />
          <time dateTime={iso(note.at)}>{clock(note.at)}</time>
          {replaced ? (
            <span className={styles["noteGoneWords"]}>Replaced by note {replaced.n} at {clock(replaced.at)}</span>
          ) : retired ? (
            <span className={styles["noteGoneWords"]}>Retired by {first(retired.by)} at {clock(retired.at)}</span>
          ) : (
            <>
              {note.replaces ? <span>replaces note {note.replaces}</span> : null}
              {note.readBy !== undefined ? <span>read by {note.readBy} of {live} {live === 1 ? "agent" : "agents"}</span> : null}
            </>
          )}
          {isNew && !gone ? <span className={styles["tag"]}><span className="ds-cap">New</span></span> : null}
          {!gone && (onReplace || onRetire) ? (
            <span className={styles["noteActions"]}>
              {onReplace ? <Button size="sm" variant="quiet" onClick={onReplace} aria-label={`Replace note ${note.n}`}>Replace</Button> : null}
              {onRetire ? <Button size="sm" variant="quiet" onClick={onRetire} aria-label={`Retire note ${note.n}`}>Retire</Button> : null}
            </span>
          ) : null}
        </div>
      </div>
    </li>
  );
}

/** The current notes against the epic's bound, "3 of 10", as Section's muted count. */
export function EpicNoteCount({ count, limit }: { readonly count: number; readonly limit: number }) {
  return (
    <span className={styles["noteCount"]} aria-label={`${count} of at most ${limit} notes`}>
      {count} of {limit}
    </span>
  );
}

const plural = (n: number, one: string, many: string) => (n === 1 ? `1 ${one}` : `${n} ${many}`);

/** The epic's replaced and retired notes, folded under the current ones to one line. */
export function EpicNoteHistory({ notes, live, defaultOpen }: {
  readonly notes: ReadonlyArray<EpicNoteData>;
  readonly live: number;
  readonly defaultOpen?: boolean | undefined;
}) {
  const [open, setOpen] = useState(defaultOpen ?? false);
  const body = useId();
  if (notes.length === 0) return null;
  const retired = notes.filter((n) => n.retired).length;
  const replaced = notes.length - retired;
  const words = [
    replaced ? plural(replaced, "replaced note", "replaced notes") : null,
    retired ? plural(retired, "retired note", "retired notes") : null,
  ].filter(Boolean).join(", ");
  return (
    <div className={styles["history"]}>
      <button type="button" className={styles["foldLine"]} aria-expanded={open} aria-controls={body} onClick={() => setOpen((o) => !o)}
        data-testid="note-history">
        <Icon name={open ? "chevron-down" : "chevron-right"} size={12} />
        <span>{words}</span>
        <span className={styles["foldHint"]}>· no agent is briefed with these any more</span>
      </button>
      {open ? (
        <ul id={body} className={styles["noteList"]}>
          {notes.map((n) => <EpicNote key={n.id} note={n} live={live} />)}
        </ul>
      ) : null}
    </div>
  );
}

/** The agent's own `note` call, in its aside: what it pinned, and the note it replaced. */
export function EpicNoteRow({ note }: { readonly note: Pick<EpicNoteData, "n" | "text" | "at" | "replaces"> }) {
  const head = `Note ${note.n} pinned for every agent on the epic${note.replaces ? ` · replaces note ${note.replaces}` : ""}`;
  return (
    <div className={styles["noteRow"]} role="note" aria-label={note.replaces ? `Epic note ${note.n} pinned, replacing note ${note.replaces}` : `Epic note ${note.n} pinned`}>
      <span className={styles["pin"]}><Icon name="pin" size={14} /></span>
      <div className={styles["noteMain"]}>
        <div className={styles["noteRowHead"]}>{head}</div>
        <Markdown source={note.text} unmeasured className={styles["noteRowText"]} />
      </div>
      <time className={styles["time"]} dateTime={iso(note.at)}>{formatTimestamp(at(note.at), "time")}</time>
    </div>
  );
}

// --------------------------------------------------------------------------- EpicNoteNotice

/** A note's text on one line: no emphasis marks, no code ticks. */
export function noteLine(text: string): string {
  return text.replace(/\*\*|`/g, "").replace(/\s+/g, " ").trim();
}

/** A note's leading bold phrase, if it has one: "Preview API (ABS-2)". */
export function noteTitle(text: string): string | null {
  const m = /^\*\*(.+?)\.?\*\*/.exec(text);
  return m ? m[1]! : null;
}

/**
 * The words that changed between two texts of a note: the run of words
 * between their common start and common end, trimmed of code ticks and
 * punctuation. Null when nothing changed, or the change is too long to
 * name (a rewrite reads in full).
 */
export function noteWordChange(was: string, now: string, most = 8): { readonly now: string; readonly was: string } | null {
  const a = was.split(/\s+/).filter(Boolean);
  const b = now.split(/\s+/).filter(Boolean);
  let head = 0;
  while (head < a.length && head < b.length && a[head] === b[head]) head++;
  let tail = 0;
  while (tail < a.length - head && tail < b.length - head && a[a.length - 1 - tail] === b[b.length - 1 - tail]) tail++;
  const clean = (words: string[]) => words.join(" ").replace(/^[`(]+|[`),.;:]+$/g, "");
  const w = clean(a.slice(head, a.length - tail));
  const n = clean(b.slice(head, b.length - tail));
  if (!w || !n || head + tail === 0) return null;
  if (a.length - head - tail > most || b.length - head - tail > most) return null;
  return { now: n, was: w };
}

export interface EpicNoteNoticeProps {
  readonly change: "added" | "replaced" | "retired";
  /** The new note; for `retired`, the note taken off. */
  readonly note: Pick<EpicNoteData, "n" | "text" | "by" | "replaces" | "retired">;
  /** The note it replaced. */
  readonly was?: Pick<EpicNoteData, "n" | "text"> | undefined;
  /** The words that changed (`noteWordChange`); absent, the text in full. */
  readonly changed?: { readonly now: string; readonly was: string } | null | undefined;
  /** When the agent read it, and after which tool. */
  readonly read: { readonly at: When; readonly after?: string | undefined };
}

function authorWords(by: EpicNoteAuthor): string {
  return by.kind === "person" ? by.person.name : `${by.task}'s ${ROLE_LABEL[by.role].toLowerCase()}`;
}

/**
 * What every agent on the epic is told at its next step when a note is
 * added, replaced or retired: one quiet line on the transcript's text
 * column with the pin. No face, rail, tag or fill: the epic's record
 * changed, nobody spoke.
 */
export function EpicNoteNotice({ change, note, was, changed, read }: EpicNoteNoticeProps) {
  const by = change === "retired" && note.retired ? note.retired.by : authorWords(note.by);
  const wasN = was?.n ?? note.replaces ?? undefined;
  const label = change === "added" ? `Epic note ${note.n} added by ${by}`
    : change === "retired" ? `Epic note ${note.n} retired by ${by}`
    : `Epic note ${note.n} replaced note ${wasN}, by ${by}`;
  const title = noteTitle(note.text);
  return (
    <div className={styles["notice"]} role="note" aria-label={label} data-change={change} data-testid="epic-note-notice">
      <span className={styles["noticeGlyph"]}><Icon name="pin" size={12} /></span>
      <div className={cx(styles["noticeBody"], change !== "replaced" && styles["noticeClamp"])}>
        <span className={styles["noticeLead"]}>
          {change === "added" ? `Epic note added: ${note.n}` : change === "retired" ? `Epic note retired: ${note.n}` : `Epic note replaced: ${wasN} → ${note.n}`}
        </span>
        <span className={styles["noticeBy"]}> · {by} · </span>
        {change === "retired" ? (
          <span className={styles["noticeWas"]}>no longer in force: {noteLine(note.text)}</span>
        ) : change === "added" || !changed ? (
          <span className={styles["noticeText"]}>{noteLine(note.text)}</span>
        ) : (
          <span className={styles["noticeText"]}>
            {title ? `${title} … ` : null}<code className={styles["noticeCode"]}>{changed.now}</code>
            <span className={styles["noticeWas"]}> (was: <code className={styles["noticeCode"]}>{changed.was}</code>)</span>
          </span>
        )}
      </div>
      <span className={styles["time"]}>
        read <time dateTime={iso(read.at)}>{formatTimestamp(at(read.at), "time")}</time>{read.after ? `, after ${read.after}` : null}
      </span>
    </div>
  );
}

// --------------------------------------------------------------------------- PeerThread

export type PeerThreadState =
  | { readonly kind: "waiting"; readonly on: string; readonly since: When }
  /** Asked, and nobody waits on it. */
  | { readonly kind: "open"; readonly on: string }
  | { readonly kind: "answered"; readonly at: When };

export interface PeerThreadProps {
  readonly a: PeerTask;
  readonly b: PeerTask;
  readonly subject: string;
  readonly state: PeerThreadState;
  readonly children: ReactNode;
  /** One muted line under the messages: who reads it, who writes in it. */
  readonly foot?: ReactNode;
}

/**
 * Two tasks' agents talking: subject, the pair, state, the messages, and
 * who reads and who writes. Read only: no composer, no Reply.
 */
export function PeerThread({ a, b, subject, state, children, foot }: PeerThreadProps) {
  return (
    <section className={styles["thread"]} aria-label={`${a.key} and ${b.key}: ${subject}`} data-testid="peer-thread" data-state={state.kind}>
      <header className={styles["threadHead"]}>
        <span className={styles["threadSubject"]}>{subject}</span>
        <span className={styles["threadPair"]}>
          <TaskChip task={a} showRole /><Icon name="comments" size={12} title="with" /><TaskChip task={b} showRole />
        </span>
        <span className={styles["threadState"]}>
          {state.kind === "waiting" ? (
            <><Icon name="clock" size={12} />Waiting for {state.on}'s reply since {clock(state.since)}</>
          ) : state.kind === "open" ? (
            <><Icon name="message" size={12} />No reply from {state.on} yet · nobody waits</>
          ) : (
            <><Icon name="check" size={12} />Answered {clock(state.at)}</>
          )}
        </span>
      </header>
      {children}
      {foot ? <div className={styles["threadFoot"]}>{foot}</div> : null}
    </section>
  );
}

// --------------------------------------------------------------------------- SinceDigest

export interface DigestItem {
  readonly id: string;
  readonly lead: ReactNode;
  readonly text: ReactNode;
  readonly at: When;
}

export interface SinceDigestProps {
  readonly since: When;
  readonly woke: When;
  /** "2 threads · 2 note changes · ABS-1 in review". */
  readonly counts: string;
  readonly groups: ReadonlyArray<{ readonly title: string; readonly items: ReadonlyArray<DigestItem> }>;
  readonly defaultOpen?: boolean | undefined;
  /** Where it was told: "in its briefing", "with its wake note". */
  readonly where?: string | undefined;
}

/**
 * What a conductor was told of its epic when it woke for something else:
 * one muted line just before the briefing that carried it, opening to the
 * items. Never a wake, never a tone.
 */
export function SinceDigest({ since, woke, counts, groups, defaultOpen, where = "in its briefing" }: SinceDigestProps) {
  const [open, setOpen] = useState(defaultOpen ?? false);
  const body = useId();
  return (
    <div className={styles["digest"]} data-testid="since-digest">
      <button type="button" className={styles["digestLine"]} aria-expanded={open} aria-controls={body} onClick={() => setOpen((o) => !o)}>
        <Icon name={open ? "chevron-down" : "chevron-right"} size={12} />
        <Icon name="layers" size={14} />
        <span className={styles["digestTitle"]}>Since the conductor last looked, on this epic</span>
        <span className={styles["digestCounts"]}>· {counts}</span>
        <span className={styles["digestWhen"]}>{clock(since)} → {clock(woke)} · {where}</span>
      </button>
      {open ? (
        <div id={body} className={styles["digestBody"]}>
          {groups.map((g) => (
            <div key={g.title}>
              <div className={cx("ds-label", styles["digestGroup"])}>{g.title}</div>
              {g.items.map((i) => (
                <div key={i.id} className={styles["digestItem"]}>
                  {i.lead}
                  <span>{i.text}</span>
                  <time className={styles["time"]} dateTime={iso(i.at)}>{clock(i.at)}</time>
                </div>
              ))}
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );
}

// --------------------------------------------------------------------------- PeerWaitMark

/** Agents on an epic waiting on each other: a clock and the words, neutral. */
export function PeerWaitMark({ pairs }: { readonly pairs: ReadonlyArray<readonly [string, string]> }) {
  if (pairs.length === 0) return null;
  const words = pairs.map(([w, on]) => `${w} waits on ${on}`).join(", ");
  return (
    <span className={styles["chip"]} role="img" aria-label={`Agents waiting on each other: ${words}`} title={words} data-testid="peer-wait-mark">
      <Icon name="clock" size={12} />
      {pairs.length === 1 ? `${pairs[0]![0]} waits on ${pairs[0]![1]}` : `${pairs.length} agents waiting on peers`}
    </span>
  );
}

// --------------------------------------------------------------------------- PeerList

/** A task as `list_tasks --peers` describes it. */
export interface PeerRow extends PeerTask {
  readonly title: string;
  /** Its newest phase Run's phase. */
  readonly phase?: string | null | undefined;
  readonly run?: "live" | "parked" | "none" | undefined;
  readonly branch?: string | null | undefined;
  readonly head?: string | null | undefined;
  readonly files?: ReadonlyArray<{ readonly path: string; readonly add: number; readonly del: number }> | undefined;
  /** What its agent last said, one paragraph. */
  readonly lastSaid?: string | null | undefined;
  /** The peer whose reply its Run waits on, by key. */
  readonly waitingOn?: string | null | undefined;
  /** Its unread messages to the caller, and whether it waits on the caller's reply. */
  readonly forYou?: { readonly count: number; readonly waits: boolean } | undefined;
  /** A peer by a "depends on" link, not by the epic. */
  readonly link?: { readonly dependsOn: string; readonly project?: string | undefined } | undefined;
}

export interface PeerListProps {
  readonly peers: ReadonlyArray<PeerRow>;
  /**
   * `page`: rows on the raised shade with what each last said. `tool`: a
   * tool call's result, no fill, branch and files. `rail`: a session rail's
   * block, the chip, title and phase.
   */
  readonly variant?: "page" | "tool" | "rail" | undefined;
  /** The caller's own task: its row says "you" and nothing else. */
  readonly self?: string | undefined;
  /** The list's accessible name: "Peers of ABS-2", "Tasks on this epic". */
  readonly label: string;
  readonly onOpen?: ((key: string) => void) | undefined;
}

function runWords(p: PeerRow): string {
  const state = p.run === "live" ? "live" : p.run === "parked" ? "parked" : "no Run going";
  return [p.phase, p.role ? ROLE_LABEL[p.role].toLowerCase() : null, state].filter(Boolean).join(" · ");
}

const fileName = (path: string) => path.split("/").at(-1) ?? path;

/** What `list_tasks --peers` returns, as rows. */
export function PeerList({ peers, variant = "page", self, label, onOpen }: PeerListProps) {
  return (
    <ul className={cx(styles["peers"], variant !== "page" && styles["dense"])} aria-label={label} data-variant={variant}>
      {peers.map((p) => {
        const task: PeerTask = { ...p, live: p.live ?? p.run === "live" };
        if (p.key === self) {
          return (
            <li key={p.key} className={styles["peerRow"]} data-self="true">
              <span className={styles["peerLine"]}>
                <TaskChip task={task} />
                <span className={styles["peerTitle"]}>{p.title}</span>
                <span className={styles["taskChipRole"]}>you</span>
              </span>
            </li>
          );
        }
        const files = p.files ?? [];
        return (
          <li key={p.key} className={styles["peerRow"]} data-peer={p.key}>
            <span className={styles["peerLine"]}>
              <TaskChip task={task} onOpen={onOpen ? () => onOpen(p.key) : undefined} />
              <span className={styles["peerTitle"]}>{p.title}</span>
              {p.link && variant !== "rail" ? (
                <span className={styles["chip"]} title='Linked by "depends on", not on this epic: no shared epic notes'>
                  <Icon name="link" size={12} />depends on {p.link.dependsOn}{p.link.project ? ` · project ${p.link.project}` : ""}
                </span>
              ) : null}
              {p.waitingOn ? <PeerWaitMark pairs={[[p.key, p.waitingOn]]} /> : null}
              {variant === "tool" && p.forYou && p.forYou.count > 0 ? (
                <span className={styles["chip"]}><Icon name="message" size={12} />
                  {p.forYou.count === 1 ? "1 message for you" : `${p.forYou.count} messages for you`}{p.forYou.waits ? ", waits on your reply" : ""}</span>
              ) : null}
            </span>
            <span className={styles["peerFacts"]}>
              {p.link && variant === "rail" ? (
                <span className={styles["peerLink"]}><Icon name="link" size={12} />depends on {p.link.dependsOn}{p.link.project ? ` · ${p.link.project}` : ""}</span>
              ) : null}
              <span>{runWords(p)}</span>
              {variant !== "rail" && p.branch ? (
                <span className={styles["mono"]}>{p.branch}{p.head ? ` @ ${p.head.slice(0, 7)}` : " · nothing pushed"}</span>
              ) : null}
              {variant !== "rail" && p.files !== undefined ? (
                <span>{files.length === 0 ? "no files changed" : files.map((f) => `${fileName(f.path)} +${f.add}${f.del ? ` −${f.del}` : ""}`).join(", ")}</span>
              ) : null}
            </span>
            {variant === "page" && p.lastSaid ? <span className={styles["peerSaid"]}>{p.lastSaid.replace(/`/g, "")}</span> : null}
            {variant === "tool" && p.lastSaid ? <span className={styles["peerSaid"]}>“{p.lastSaid.replace(/`/g, "")}”</span> : null}
          </li>
        );
      })}
    </ul>
  );
}

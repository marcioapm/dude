/*
 * Proposed design-system components for agents coordinating on an epic:
 * TaskChip, PeerMessage, EpicNote (and its transcript row), PeerThread,
 * SinceDigest, PeerWaitMark and PeerList. Each composes existing parts
 * (AgentAvatar, StatusMark, PersonAvatar, Markdown, Icon) and styles itself
 * from tokens only. The proposed `pin` glyph is drawn here until it joins
 * the icon set.
 */

import { useId, useState, type ReactNode } from "react";
import type { AgentRole, TaskStatus } from "@dude/domain";
import { AgentAvatar, Markdown, PersonAvatar, ROLE_LABEL, StatusMark } from "@dude/design-system/components";
import { Icon, cx, formatTimestamp } from "@dude/design-system";
import { PEERS, type Delivery, type Note, type Peer, type PeerMsg, type TaskKey } from "./data.ts";
import s from "./peer.module.css";

/** The proposed `pin` glyph: a push pin, for what is pinned for every agent on an epic. */
export function PinGlyph({ size = 14 }: { readonly size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth={1.5} strokeLinecap="round" strokeLinejoin="round" aria-hidden style={{ flexShrink: 0 }}>
      <path d="M9.5 2.5 13.5 6.5M10.5 3.5 7 7l-3 .5L8.5 12 9 9l3.5-3.5M6.25 9.75 2.5 13.5" />
    </svg>
  );
}

const clock = (d: Date) => formatTimestamp(d, "time-short");

// --------------------------------------------------------------------------- TaskChip

/** A task by its key, with its status and the role of its live agent: a peer, wherever one is named. */
export function TaskChip({ task, role, status, onOpen, showRole }: {
  readonly task: TaskKey;
  /** The agent speaking for it; defaults to its live Run's. */
  readonly role?: AgentRole | undefined;
  /** Its status at the moment shown; defaults to now. */
  readonly status?: TaskStatus | undefined;
  readonly onOpen?: (() => void) | undefined;
  /** Say the role in words after the key (the thread heads do; inline mentions do not). */
  readonly showRole?: boolean | undefined;
}) {
  const p = PEERS[task];
  const r = role ?? p.role;
  const inner = (
    <>
      <StatusMark status={status ?? p.status} iconOnly size="sm" />
      <span className={s.taskKey}>{task}</span>
      <AgentAvatar role={r} size="xs" live={p.live} />
      {showRole ? <span className={s.taskChipRole}>{ROLE_LABEL[r]}</span> : null}
    </>
  );
  const label = `${task}, ${p.title}: its ${ROLE_LABEL[r].toLowerCase()}`;
  return onOpen ? (
    <button type="button" className={s.taskChip} onClick={onOpen} aria-label={`Open ${label}`} title={p.title}>{inner}</button>
  ) : (
    <span className={s.taskChip} aria-label={label} title={p.title} role="img">{inner}</span>
  );
}

// --------------------------------------------------------------------------- PeerMessage

// Where an unread message lands, in a steer's words; a read one says it in its header.
function deliveryNote(d: Delivery, to: TaskKey | "epic"): ReactNode {
  if (d.kind === "queued") return <span>Lands at <b>{to === "epic" ? "each peer" : to}</b>'s next step. Nothing is interrupted.</span>;
  if (d.kind === "inbox") return <span><b>{to}</b> has no live agent: it is in {to}'s next Run briefing.</span>;
  return null;
}

export interface PeerMessageProps {
  readonly msg: PeerMsg;
  /** The transcript it sits in is this task's: "→ you" for messages to it. */
  readonly viewer?: TaskKey | undefined;
  /** In a thread on the Coordination view: a 24px face and the UI body size. */
  readonly compact?: boolean | undefined;
  /** What it answers, quoted in one line. */
  readonly replyToText?: string | undefined;
  /** The sender waits for the reply (`wait`): one muted line with the wait's clock. */
  readonly waiting?: { readonly since: Date; readonly parksIn?: string | undefined } | undefined;
  /** Refused: the reason, in danger ink, where the delivery line would be. */
  readonly refused?: string | undefined;
}

/**
 * A message from one task's agent to another's. A turn on the transcript's
 * grid, framed by a 2px rail in the sender's role colour (the thread rule),
 * never by the accent (a person's steer) or attention (needs you), and never
 * with a person's face. Header: sender's key and role → recipient, a neutral
 * "Peer" tag (or "Reply"), and its delivery in words, as a steer's.
 */
export function PeerMessage({ msg, viewer, compact, replyToText, waiting, refused }: PeerMessageProps) {
  const outgoing = viewer === msg.from;
  const d = msg.delivery;
  const to = msg.to === "epic" ? "every peer" : msg.to === viewer ? "you" : msg.to;
  const queued = d.kind === "queued" && !refused;
  const titleId = useId();
  return (
    <article className={cx(s.peer, s[msg.fromRole], compact && s.compact)} data-queued={queued || d.kind === "inbox" ? "true" : undefined}
      data-kind="peer" aria-labelledby={titleId}>
      <div className={s.gutter}>
        <AgentAvatar role={msg.fromRole} size={compact ? "md" : "chat"} />
      </div>
      <div className={s.main}>
        <header className={s.head} id={titleId}>
          <span className={s.name}>{outgoing ? "You" : msg.from}</span>
          <span className={s.arrow}>
            {outgoing ? null : <span>{ROLE_LABEL[msg.fromRole]}</span>}
            <Icon name="arrow-right" size={12} title="to" />
            {msg.to === "epic" ? <span>every peer on the epic</span> : to === "you" ? <span>you</span> : <TaskChip task={msg.to} />}
          </span>
          <span className={s.tag}><Icon name="message" size={10} /><span className="ds-cap">{msg.replyTo ? "Reply" : "Peer"}</span></span>
          {d.kind === "read" ? (
            <span className={s.time}>
              sent <time dateTime={msg.sentAt.toISOString()}>{clock(msg.sentAt)}</time> · read{" "}
              <time dateTime={d.at.toISOString()}>{formatTimestamp(d.at, "time")}</time>{d.after ? `, after ${d.after}` : null}
            </span>
          ) : (
            <time className={s.time} dateTime={msg.sentAt.toISOString()}>{formatTimestamp(msg.sentAt, "time")}</time>
          )}
        </header>
        {replyToText ? (
          <div className={s.replyTo}><span className={s.replyToLabel}>Replying to</span><span className={s.replyToText}>{replyToText.replace(/`/g, "")}</span></div>
        ) : null}
        <Markdown source={msg.text} unmeasured className={s.body} />
        {refused ? (
          <div className={s.dangerNote} role="status"><Icon name="alert" size={12} /><span><b>Not sent:</b> {refused}</span></div>
        ) : d.kind !== "read" ? (
          <div className={s.note}><Icon name="clock" size={12} />{deliveryNote(d, msg.to)}</div>
        ) : null}
        {waiting ? (
          <div className={s.note}>
            <Icon name="clock" size={12} />
            <span>{outgoing ? "Your turn ended" : `${msg.from}'s turn ended`}: <b>waiting for {msg.to === viewer ? "your" : `${msg.to}'s`} reply</b> since {clock(waiting.since)}.</span>
            {waiting.parksIn ? <span className={s.noteMuted}>{waiting.parksIn}</span> : null}
          </div>
        ) : null}
      </div>
    </article>
  );
}

// --------------------------------------------------------------------------- EpicNote

function NoteBy({ note }: { readonly note: Note }) {
  const b = note.by;
  return b.kind === "person" ? (
    <span className={s.noteByWho}><PersonAvatar person={b.person} size={16} />{b.person.name}</span>
  ) : (
    <span className={s.noteByWho}><PersonAvatar person={b.for} size={16} agent={b.role} />{ROLE_LABEL[b.role]} on <span className={s.taskKey}>{b.task}</span></span>
  );
}

/** A fact pinned for every agent on the epic: the pin, the words, who pinned it and when, how many agents have it. */
export function EpicNote({ note, live, isNew }: { readonly note: Note; readonly live: number; readonly isNew?: boolean | undefined }) {
  return (
    <li className={s.epicNote} data-note={note.id}>
      <span className={s.pin}><PinGlyph /></span>
      <div>
        <Markdown source={note.text} unmeasured className={s.noteText} />
        <div className={s.noteBy}>
          <NoteBy note={note} />
          <time dateTime={note.at.toISOString()}>{clock(note.at)}</time>
          <span>read by {note.readBy} of {live} agents</span>
          {isNew ? <span className={s.tag}><span className="ds-cap">New</span></span> : null}
        </div>
      </div>
    </li>
  );
}

/**
 * An epic note in a transcript: a row of the agent's aside, as ChatEvent
 * is. `pinned` is the agent's own `note` call; otherwise it arrived at
 * the agent's step.
 */
export function EpicNoteRow({ note, pinned }: { readonly note: Note; readonly pinned?: boolean | undefined }) {
  return (
    <div className={s.noteInline} role="note" aria-label={pinned ? "Epic note pinned" : "New epic note"}>
      <span className={s.pin}><PinGlyph size={14} /></span>
      <div>
        <div className={s.noteInlineHead}>
          {pinned ? "Pinned for every agent on the epic" : <>Epic note · <NoteBy note={note} /></>}
        </div>
        <Markdown source={note.text} unmeasured className={s.noteInlineText} />
      </div>
      <time className={s.time} dateTime={note.at.toISOString()}>{formatTimestamp(note.at, "time")}</time>
    </div>
  );
}

// --------------------------------------------------------------------------- PeerThread

export type ThreadState =
  | { readonly kind: "waiting"; readonly on: TaskKey; readonly since: Date }
  /** Asked without `wait`: the sender carries on. */
  | { readonly kind: "open"; readonly on: TaskKey }
  | { readonly kind: "answered"; readonly at: Date };

/** A conversation between two tasks on the Coordination view: who with whom, about what, its state, then its messages. */
export function PeerThread({ a, b, subject, state, children, foot }: {
  readonly a: TaskKey;
  readonly b: TaskKey;
  readonly subject: string;
  readonly state: ThreadState;
  readonly children: ReactNode;
  /** A person's way in: Reply. */
  readonly foot?: ReactNode;
}) {
  return (
    <section className={s.thread} aria-label={`${a} and ${b}: ${subject}`}>
      <header className={s.threadHead}>
        <span className={s.threadSubject}>{subject}</span>
        <span className={s.threadPair}>
          <TaskChip task={a} showRole /><Icon name="comments" size={12} title="with" /><TaskChip task={b} showRole />
        </span>
        <span className={s.threadState}>
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
      {foot ? <div className={s.threadFoot}>{foot}</div> : null}
    </section>
  );
}

// --------------------------------------------------------------------------- SinceDigest

export interface DigestItem {
  readonly id: string;
  readonly lead: ReactNode;
  readonly text: ReactNode;
  readonly at: Date;
}

/**
 * What a conductor was told about its epic when it woke for something
 * else: one quiet line in its task's Chat, where the briefing put it, that
 * opens to the items. Never a wake of its own, never a tone.
 */
export function SinceDigest({ since, woke, counts, groups, defaultOpen }: {
  readonly since: Date;
  readonly woke: Date;
  readonly counts: string;
  readonly groups: ReadonlyArray<{ readonly title: string; readonly items: ReadonlyArray<DigestItem> }>;
  readonly defaultOpen?: boolean | undefined;
}) {
  const [open, setOpen] = useState(defaultOpen ?? false);
  const body = useId();
  return (
    <div className={s.digest} data-testid="since-digest">
      <button type="button" className={s.digestLine} aria-expanded={open} aria-controls={body} onClick={() => setOpen((o) => !o)}>
        <Icon name={open ? "chevron-down" : "chevron-right"} size={12} />
        <Icon name="layers" size={14} />
        <span className={s.digestTitle}>Since the conductor last looked, on this epic</span>
        <span className={s.digestCounts}>· {counts}</span>
        <span className={s.digestWhen}>{clock(since)} → {clock(woke)} · in its briefing</span>
      </button>
      {open ? (
        <div id={body} className={s.digestBody}>
          {groups.map((g) => (
            <div key={g.title}>
              <div className={cx("ds-label", s.digestGroup)}>{g.title}</div>
              {g.items.map((i) => (
                <div key={i.id} className={s.digestItem}>
                  {i.lead}
                  <span>{i.text}</span>
                  <time className={s.time} dateTime={i.at.toISOString()}>{clock(i.at)}</time>
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

/** Agents on an epic waiting on each other: a clock and the words, neutral; the chips say who. */
export function PeerWaitMark({ pairs }: { readonly pairs: ReadonlyArray<readonly [TaskKey, TaskKey]> }) {
  const words = pairs.map(([a, b]) => `${a} waits on ${b}`).join(", ");
  return (
    <span className={s.waitMark} role="img" aria-label={`Agents waiting on each other: ${words}`} title={words}>
      <Icon name="clock" size={12} />
      {pairs.length === 1 ? `${pairs[0]![0]} waits on ${pairs[0]![1]}` : `${pairs.length} agents waiting on peers`}
    </span>
  );
}

// --------------------------------------------------------------------------- PeerList

/** The epic's other tasks, as `peers` returns them: status, phase, live or parked, branch and head, files by churn, what it last said. */
export function PeerList({ peers, variant = "page", onOpen, overrides }: {
  readonly peers: ReadonlyArray<Peer>;
  /**
   * `page`: rows on the raised shade with what each last said. `tool`: a
   * tool call's result, no fill, branch and files. `rail`: a session rail's
   * block, the chip, title and phase only.
   */
  readonly variant?: "page" | "tool" | "rail" | undefined;
  readonly onOpen?: ((k: TaskKey) => void) | undefined;
  /** What a moment changes about a peer: its files so far, its waiting. */
  readonly overrides?: Partial<Record<TaskKey, Partial<Peer>>> | undefined;
}) {
  return (
    <ul className={cx(s.peers, variant !== "page" && s.dense)} aria-label="Peers on this epic">
      {peers.map((base) => {
        const p = { ...base, ...overrides?.[base.key] };
        return (
          <li key={p.key} className={s.peerRow}>
            <span className={s.peerLine}>
              <TaskChip task={p.key} onOpen={onOpen ? () => onOpen(p.key) : undefined} />
              <span className={s.peerTitle}>{p.title}</span>
              {p.waitingOn ? <PeerWaitMark pairs={[[p.key, p.waitingOn]]} /> : null}
            </span>
            <span className={s.peerFacts}>
              <span>{p.phase} · {ROLE_LABEL[p.role].toLowerCase()} · {p.live ? "live" : "parked"}</span>
              {variant !== "rail" ? (
                <>
                  <span className={s.mono}>{p.branch}{p.head !== "—" ? ` @ ${p.head}` : " · nothing pushed"}</span>
                  <span>{p.files.length === 0 ? "no files changed" : p.files.map((f) => `${f.path.split("/").at(-1)} +${f.add}${f.del ? ` −${f.del}` : ""}`).join(", ")}</span>
                </>
              ) : null}
            </span>
            {variant === "page" ? <span className={s.peerSaid}>{p.lastSaid.replace(/`/g, "")}</span> : null}
            {variant === "tool" ? <span className={s.peerSaid}>“{p.lastSaid.replace(/`/g, "")}”</span> : null}
          </li>
        );
      })}
    </ul>
  );
}

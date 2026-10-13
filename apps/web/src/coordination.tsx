/**
 * Agents on an epic, as the web app reads them: the epic's notes, threads
 * and tasks (`GET /v1/epics/:id/notes`, `/threads`, `/peers`), mapped onto
 * the design system's peer pieces, and the transcript's peer turns drawn
 * with them. One read of the three serves the Coordination view, a Run's
 * rail and its transcript; it is read again when the caller says something
 * changed.
 */

import { useEffect, useMemo, useState, type ReactNode } from "react";
import {
  EpicNoteNotice, PeerList, PeerMessage, PeerWaitLine, SessionFacts, SessionRailBlock, SinceDigest, TaskChip, noteWordChange,
  type EpicNoteData, type PeerMessageData, type PeerRow, type PeerTask, type Person,
} from "@dude/design-system/components";
import { Icon, plural } from "@dude/design-system";
import {
  ALL_AGENT_ROLES, ALL_TASK_STATUSES, type AgentRole, type EpicNote, type EpicNotes, type EpicPeers, type EpicThreads,
  type PeerMessageView, type PeerState, type TaskStatus,
} from "@dude/domain";
import type { ApiClient } from "./api/client.ts";
import {
  dudeToolName, toolLabel, type DigestTurn, type NoteToldTurn, type PeerTurn, type PeerWaitTurn, type ToolTurn, type Turn,
} from "./api/conversation.ts";
import type { People } from "./people.tsx";

/** An epic's notes, threads and tasks, read together. */
export interface Coordination {
  notes: EpicNotes;
  threads: EpicThreads;
  peers: EpicPeers;
}

/**
 * The epic's coordination, read when `epicId` is known and again each time
 * `version` moves; null until the first read, and kept through a failed
 * re-read. `problem` says why a read failed.
 */
export function useCoordination(client: ApiClient, epicId: string | null | undefined, version: unknown) {
  const [data, setData] = useState<{ epicId: string; value: Coordination } | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [asked, setAsked] = useState(0);
  useEffect(() => {
    if (!epicId) return;
    let current = true;
    Promise.all([client.epicNotes(epicId), client.epicThreads(epicId), client.epicPeers(epicId)]).then(
      ([notes, threads, peers]) => {
        if (!current) return;
        setData({ epicId, value: { notes, threads, peers } });
        setProblem(null);
      },
      (err: unknown) => current && setProblem(err instanceof Error ? err.message : String(err)),
    );
    return () => {
      current = false;
    };
  }, [client, epicId, version, asked]);
  const value = data && data.epicId === epicId ? data.value : null;
  return { value, problem, reload: () => setAsked((n) => n + 1) };
}

const ROLES = new Set<string>(ALL_AGENT_ROLES);
const STATUSES = new Set<string>(ALL_TASK_STATUSES);
const asRole = (r: string | null | undefined): AgentRole | undefined => (r && ROLES.has(r) ? (r as AgentRole) : undefined);
const asStatus = (s: string | null | undefined): TaskStatus | undefined => (s && STATUSES.has(s) ? (s as TaskStatus) : undefined);

/** The role a phase speaks with, as the API's peer state names it. */
const PHASE_ROLE: Record<string, AgentRole> = {
  implement: "implementer", fix: "implementer", review: "reviewer", simplify: "simplifier", test: "qa_browser", investigate: "investigator",
};

/** A task as the peer pieces name it, from the epic's peers. */
export function peerTask(key: string, peers: readonly PeerState[] | undefined, title?: string): PeerTask {
  const p = peers?.find((x) => x.key === key);
  if (!p) return { key, ...(title ? { title } : {}) };
  return { key, title: p.title, status: asStatus(p.status), role: asRole(p.role), live: p.run === "live" };
}

/** Each task on the epic as `PeerList` draws it. */
export function peerRows(peers: readonly PeerState[]): PeerRow[] {
  return peers.map((p) => ({
    key: p.key, title: p.title, status: asStatus(p.status), role: asRole(p.role), live: p.run === "live", phase: p.phase, run: p.run,
    waitingOn: p.waitingOn?.task.key ?? null,
    branch: p.branch, head: p.head, files: p.files?.map((f) => ({ path: f.path, add: f.additions, del: f.deletions })), lastSaid: p.lastSaid,
  }));
}

/** Who pinned a note, as `EpicNote` says it: a person by the organisation's records, an agent on its task's owner's face. */
export function noteData(n: EpicNote, people: People, owners?: ReadonlyMap<string, Person>): EpicNoteData {
  const by: EpicNoteData["by"] = n.by.kind === "agent" && n.by.task
    ? { kind: "agent", task: n.by.task.key, role: agentOf(n.by.label), ...(owners?.get(n.by.task.taskId) ? { for: owners.get(n.by.task.taskId)! } : {}) }
    : { kind: "person", person: personOf(n.by.personId, n.by.label, people) };
  return {
    id: n.id, n: n.n, text: n.text, by, at: n.createdAt, readBy: n.readBy, replaces: n.replaces,
    replacedBy: n.replacedBy, fromSession: n.by.fromSessionId !== null,
    retired: n.retired ? { by: n.retired.by.name ?? "someone", at: n.retired.at } : null,
  };
}

function personOf(id: string | null, label: string, people: People) {
  const known = id ? people.byId.get(id) : undefined;
  return known ? { id: known.id, name: known.name, photoUrl: known.photoUrl, online: known.online } : { ...(id ? { id } : {}), name: label };
}

/** "ABS-2's implementer" → implementer; "ABS-2's fixer" speaks as one. */
function agentOf(label: string): AgentRole {
  const word = label.split("'s ").at(-1)?.trim().replace(/ /g, "_") ?? "";
  if (word === "fixer") return "implementer";
  return asRole(word) ?? "implementer";
}

/** Every message on the epic, by id: what a transcript's peer turn needs that its event does not carry. */
export function messagesById(threads: EpicThreads | undefined): ReadonlyMap<string, PeerMessageView> {
  const out = new Map<string, PeerMessageView>();
  for (const t of threads?.threads ?? []) for (const m of t.messages) out.set(m.id, m);
  return out;
}

/** A message as `PeerMessage` draws it. */
export function messageData(m: PeerMessageView, peers: readonly PeerState[] | undefined): PeerMessageData {
  const from = { ...peerTask(m.from.key, peers, m.from.title), role: asRole(m.fromRole) ?? (m.fromPhase ? PHASE_ROLE[m.fromPhase] : undefined) };
  return {
    id: m.id, from, to: peerTask(m.to.key, peers, m.to.title), text: m.text, sentAt: m.sentAt, replyTo: m.replyTo,
    delivery: m.readAt ? { kind: "read", at: m.readAt } : m.briefedRunId ? { kind: "read", at: m.sentAt, after: "in its briefing" }
      : m.lands === "next_run" ? { kind: "inbox" } : { kind: "queued" },
  };
}

/** A thread's subject: its first message's first line, clipped. The API has no subject of its own. */
export function threadSubject(first: string): string {
  const line = first.replace(/[`*_]/g, "").split("\n")[0]!.trim();
  return line.length > 80 ? `${line.slice(0, 79)}…` : line;
}

/** What a transcript's peer turns read beyond their own events. */
export interface PeerContext {
  /** The Run's own task, by key: "You" and "you". */
  self: string | undefined;
  /** The Run's own role. */
  role: AgentRole;
  coordination: Coordination | null;
  messages: ReadonlyMap<string, PeerMessageView>;
  people: People;
}

/** A peer turn of the transcript: where a message was sent, or where it was read. */
export function PeerTurnView({ turn, ctx }: { turn: PeerTurn; ctx: PeerContext }) {
  const peers = ctx.coordination?.peers.tasks;
  const known = ctx.messages.get(turn.message);
  const self = ctx.self ?? "";
  const sent = turn.direction === "sent";
  const msg: PeerMessageData = known ? messageData(known, peers) : {
    id: turn.message,
    from: sent ? { key: self, role: ctx.role } : peerTask(turn.peer, peers),
    to: sent ? peerTask(turn.peer, peers) : { key: self },
    text: turn.text ?? "(the message is on the epic's Coordination view)",
    sentAt: turn.at, replyTo: turn.replyTo,
    delivery: sent ? (turn.lands === "next_run" ? { kind: "inbox" } : { kind: "queued" }) : { kind: "read", at: turn.at },
  };
  if (sent && turn.text) Object.assign(msg, { text: turn.text });
  // A held message says what the orchestrator told its sender.
  if (sent && msg.delivery.kind === "inbox" && turn.notice) Object.assign(msg, { delivery: { kind: "inbox", notice: turn.notice } });
  if (!sent) {
    // Read here: when, and after what, as this Run read it.
    Object.assign(msg, { delivery: { kind: "read", at: turn.at, after: turn.via === "briefing" ? "in its briefing" : turn.after ? toolLabel(turn.after) : undefined } });
  }
  const replyTo = msg.replyTo ? ctx.messages.get(msg.replyTo)?.text : undefined;
  return <PeerMessage msg={msg} viewer={self} replyToText={replyTo} carryingOn={sent} />;
}

/** Where this Run stopped to wait on a peer's reply, and how the wait ended. */
export function PeerWaitView({ turn, now }: { turn: PeerWaitTurn; now?: number | undefined }) {
  return (
    <PeerWaitLine on={turn.on} msg={turn.message} how={turn.how === "turn_ended" ? "turn-ended" : "wait"} since={turn.since} parksAt={turn.parksAt} now={now}
      ended={turn.ended ? (turn.ended.by === "peer_ended" ? { at: turn.ended.at, by: "peer_ended", status: turn.ended.status ?? undefined, line: turn.ended.line ?? undefined }
        : { at: turn.ended.at, by: turn.ended.by }) : undefined} />
  );
}

/** A note change this Run was told, with the note's words from the epic's notes. */
export function NoteToldView({ turn, ctx }: { turn: NoteToldTurn; ctx: PeerContext }) {
  const all = [...(ctx.coordination?.notes.current ?? []), ...(ctx.coordination?.notes.history ?? [])];
  const note = all.find((n) => n.id === turn.note) ?? all.find((n) => n.n === turn.n);
  const was = turn.was !== null ? all.find((n) => n.n === turn.was) : undefined;
  const data: EpicNoteData = note ? noteData(note, ctx.people) : {
    id: turn.note, n: turn.n, text: "", by: { kind: "person", person: { name: "someone" } }, at: turn.at, replaces: turn.was,
  };
  return (
    <EpicNoteNotice change={turn.change} note={data} was={was ? { n: was.n, text: was.text } : undefined}
      changed={was && note ? noteWordChange(was.text, note.text) : undefined}
      read={{ at: turn.at, after: turn.after ? toolLabel(turn.after) : undefined }} />
  );
}

/** The conductor's digest, folded, as it was told. */
export function DigestView({ turn, ctx }: { turn: DigestTurn; ctx: PeerContext }) {
  const peers = ctx.coordination?.peers.tasks;
  const counts = [
    turn.messages.length ? plural(turn.messages.length + turn.more, "message") : null,
    turn.notes.length ? plural(turn.notes.length, "note change") : null,
    turn.peers.length ? turn.peers.length === 1 ? `${turn.peers[0]!.task} ${turn.peers[0]!.status.replace(/_/g, " ")}` : plural(turn.peers.length, "peer moved", "peers moved") : null,
  ].filter(Boolean).join(" · ");
  const other = (m: DigestTurn["messages"][number]) => (m.from === turn.task ? m.to : m.from);
  const groups: Array<{ title: string; items: Array<{ id: string; lead: ReactNode; text: ReactNode; at: string }> }> = [];
  if (turn.messages.length) {
    groups.push({ title: `Messages with ${turn.task}`, items: [
      ...turn.messages.map((m) => ({
        id: m.message, at: m.at, lead: <TaskChip task={peerTask(other(m), peers)} />,
        text: <>{m.from === turn.task ? `${turn.task} → ${m.to}` : `→ ${turn.task}`}: “{m.text}”
          {m.replyTo ? ` · replies to ${m.replyTo}` : m.answeredAt ? <> · <b>answered</b></> : " · no reply yet"}</>,
      })),
      ...(turn.more ? [{ id: "more", at: turn.through, lead: <Icon name="more" size={14} />, text: `… and ${turn.more} more` }] : []),
    ] });
  }
  if (turn.notes.length) {
    groups.push({ title: "Epic notes added, replaced or retired", items: turn.notes.map((n, i) => ({
      id: `n${i}`, at: n.at, lead: <Icon name="pin" size={14} />, text: n.line.charAt(0).toUpperCase() + n.line.slice(1),
    })) });
  }
  if (turn.peers.length) {
    groups.push({ title: "Peers that changed", items: turn.peers.map((p) => ({
      id: p.task, at: p.at, lead: <TaskChip task={{ ...peerTask(p.task, peers), status: asStatus(p.status) }} />,
      text: `is ${p.status.replace(/_/g, " ")}`,
    })) });
  }
  return <SinceDigest since={turn.since} woke={turn.through} counts={counts} groups={groups}
    where={turn.where === "wake" ? "with its wake note" : "in its briefing"} />;
}

/** A transcript with no epic read (yet): its peer turns say what their events carry. */
export function noPeers(role: AgentRole, people: People): PeerContext {
  return { self: undefined, role, coordination: null, messages: new Map(), people };
}

/** What a dude tool for peers looks like as a tool call: a compact summary, its glyph, and a `list_tasks --peers` result as a PeerList. */
export interface PeerToolView {
  summary: ReactNode;
  icon: "layers" | "file-diff" | "message" | "clock" | "pin";
  result?: ReactNode;
}

/** The tool's output, as JSON when it parses. */
function outputJson(turn: ToolTurn): Record<string, unknown> | null {
  const head = turn.result?.output?.head ?? turn.result?.stdout?.head;
  if (!head) return null;
  try {
    const v = JSON.parse(head) as unknown;
    return typeof v === "object" && v !== null ? (v as Record<string, unknown>) : null;
  } catch {
    return null;
  }
}

/** A `list_tasks --peers` result's rows, as `PeerList` draws them. */
export function listedPeers(out: Record<string, unknown>): PeerRow[] {
  const tasks = Array.isArray(out.tasks) ? (out.tasks as Array<Record<string, unknown>>) : [];
  return tasks.map((t) => {
    const heads = Array.isArray(t.heads) ? (t.heads as Array<Record<string, unknown>>) : [];
    const files = Array.isArray(t.files) ? (t.files as Array<Record<string, unknown>>) : [];
    const run = t.run === "live" || t.run === "parked" ? t.run : "none";
    return {
      key: String(t.key ?? ""), title: String(t.title ?? ""), status: asStatus(String(t.status ?? "")), role: asRole(String(t.role ?? "")),
      live: run === "live", phase: typeof t.phase === "string" ? t.phase : null, run,
      branch: typeof heads[0]?.branch === "string" ? (heads[0].branch as string) : null,
      head: typeof heads[0]?.head === "string" ? (heads[0].head as string) : null,
      files: files.map((f) => ({ path: String(f.path ?? ""), add: Number(f.additions ?? 0), del: Number(f.deletions ?? 0) })),
      lastSaid: typeof t.lastSaid === "string" ? t.lastSaid : null,
      waitingOn: typeof t.waitingOn === "string" && t.waitingOn ? t.waitingOn : null,
      forYou: typeof t.messagesForYou === "number" ? { count: t.messagesForYou, waits: t.waitsOnYourReply === true } : undefined,
    } satisfies PeerRow;
  });
}

/**
 * A dude tool an agent uses with its peers, drawn compactly as the other
 * dude tools are: `list_tasks --peers` (its result a PeerList), `run_diff
 * --task`, `message`, `wait_for_reply`, `note`. Null for any other call.
 */
export function peerTool(turn: ToolTurn, ctx: PeerContext | undefined): PeerToolView | null {
  const name = dudeToolName(turn.tool);
  const args = (turn.args && typeof turn.args === "object" ? turn.args : {}) as Record<string, unknown>;
  const out = turn.status === "completed" ? outputJson(turn) : null;
  const code = (s: string) => <code>{s}</code>;
  switch (name) {
    case "list_tasks": {
      if (args.peers !== true) return null;
      const rows = out ? listedPeers(out) : [];
      const others = rows.filter((r) => r.key !== ctx?.self);
      const forYou = others.reduce((n, r) => n + (r.forYou?.count ?? 0), 0);
      const facts = out ? [plural(others.length, "peer"), ...(forYou ? [`${plural(forYou, "message")} for you`] : [])] : [];
      return {
        icon: "layers",
        summary: <>{code("dude task list --peers")}{facts.length ? ` · ${facts.join(" · ")}` : null}</>,
        ...(rows.length ? { result: <PeerList variant="tool" label={ctx?.self ? `Peers of ${ctx.self}` : "Peers"} self={ctx?.self} peers={rows} /> } : {}),
      };
    }
    case "run_diff": {
      if (typeof args.task !== "string") return null;
      const files = out && Array.isArray(out.files) ? out.files.length : null;
      return { icon: "file-diff", summary: <>{code(`dude diff --task ${args.task}${typeof args.path === "string" ? ` ${args.path}` : ""}`)}
        {files !== null ? ` · ${plural(files, "file")}` : null}</> };
    }
    case "message": {
      const to = typeof args.to === "string" ? args.to : "";
      const reply = typeof args.replyTo === "string" ? ` --reply-to ${args.replyTo}` : "";
      const held = out?.lands === "next_run";
      const words = turn.refusal !== undefined ? null : turn.status !== "completed" ? null
        : held ? `sent · ${to} has no Run going` : "sent · carrying on";
      return { icon: "message", summary: <>{code(`dude message ${to}${reply}`)}{words ? ` · ${words}` : null}</> };
    }
    case "wait_for_reply": {
      const message = typeof args.message === "string" ? args.message : "";
      const waiting = out?.waiting as Record<string, unknown> | undefined;
      const words = out?.reply ? "the reply had landed" : waiting ? `ends this turn: waiting on ${String(waiting.on ?? "")}` : null;
      return { icon: "clock", summary: <>{code(`dude wait ${message}`)}{words ? ` · ${words}` : null}</> };
    }
    case "note": {
      const replaces = typeof args.replaces === "number" && args.replaces > 0 ? ` --replaces ${args.replaces}` : "";
      const words = out && typeof out.n === "number"
        ? `note ${out.n}, told to ${plural(Number(out.told ?? 0), "agent")} at their next step` : null;
      return { icon: "pin", summary: <>{code(`dude note${replaces}`)}{words ? ` · ${words}` : null}</> };
    }
    default:
      return null;
  }
}

/** The peer context of a Run's transcript, built once per read. */
export function usePeerContext(coordination: Coordination | null, self: string | undefined, role: AgentRole, people: People): PeerContext {
  const messages = useMemo(() => messagesById(coordination?.threads), [coordination]);
  return useMemo(() => ({ self, role, coordination, messages, people }), [self, role, coordination, messages, people]);
}

/**
 * A Run waiting on a peer, as its header and the tree say it: "Waiting on
 * ABS-2 · parks in 10 min", or "· parked" once it is.
 */
export function waitWords(on: string, parksAt: string | null, parked: boolean, now = Date.now()): string {
  if (parked) return `Waiting on ${on} · parked`;
  if (!parksAt) return `Waiting on ${on}`;
  const min = Math.max(0, Math.round((new Date(parksAt).getTime() - now) / 60_000));
  return min > 0 ? `Waiting on ${on} · parks in ${min} min` : `Waiting on ${on} · parks now`;
}

/** Roles that write to their peers; the rest read them. */
const WRITERS = new Set<AgentRole>(["implementer", "investigator", "conductor"]);

/**
 * A Run's rail on an epic: its peers (the epic's other tasks), the epic's
 * notes against their bound and whether this Run has read them all, and
 * which peer tools its role has. A reviewer, the simplifier and QA read;
 * they have the notes in their briefing.
 */
export function PeerRail({ coordination, self, role, phase, startedAt, turns }: {
  coordination: Coordination;
  self: string | undefined;
  role: AgentRole;
  phase: string | null;
  startedAt: string;
  turns: readonly Turn[];
}) {
  const writes = WRITERS.has(role) || phase === "fix";
  const notes = coordination.notes.current;
  // Notes pinned after it started reach a Run at a step: read once it was told.
  const told = new Set(turns.flatMap((t) => (t.kind === "noteTold" ? [t.note] : [])));
  const unread = notes.filter((n) => n.createdAt > startedAt && !told.has(n.id)).length;
  const others = peerRows(coordination.peers.tasks.filter((p) => p.key !== self));
  return (
    <>
      {others.length > 0 ? (
        <SessionRailBlock label="Peers" data-testid="rail-peers">
          <PeerList variant="rail" label={self ? `Peers of ${self}` : "Peers"} peers={others} />
        </SessionRailBlock>
      ) : null}
      <SessionRailBlock label="Epic notes" data-testid="rail-notes">
        <span className="railLine"><Icon name="pin" size={14} />
          {writes
            ? `${notes.length} of ${coordination.notes.limit} pinned · ${unread === 0 ? "all read" : `${unread} not read yet`}`
            : `Epic notes: ${notes.length}, in briefing`}
        </span>
      </SessionRailBlock>
      <SessionRailBlock label="Role" data-testid="rail-role">
        <SessionFacts facts={[{ label: "With peers", value: writes ? "list, diff, message, note" : "list, diff · read only" }]} />
      </SessionRailBlock>
    </>
  );
}

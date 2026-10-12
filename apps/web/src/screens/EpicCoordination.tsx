/**
 * An epic's Coordination view, beside its board: the notes every agent on
 * it follows (pinned, replaced and retired by people here), the threads
 * between its tasks' agents (read only: only the two agents write in one),
 * and each task as its peers see it.
 *
 * Read with the epic's page: again whenever the shell's tree reloads (the
 * organisation's stream says something happened), as the epic's metrics
 * are. A pin or retire reads it again at once.
 */

import { useMemo, useRef, useState } from "react";
import { EpicNote, EpicNoteHistory, PeerList, PeerMessage, PeerThread, type EpicNoteData } from "@dude/design-system/components";
import { Button, Callout, Dialog, Section, Spinner, Textarea, useToast } from "@dude/design-system/primitives";
import { firstName } from "@dude/design-system";
import { EPIC_NOTE_TEXT_MAX, type EpicNote as EpicNoteRecord, type PeerThread as PeerThreadRecord } from "@dude/domain";
import type { NavEpic } from "@dude/design-system";
import { ApiError, type ApiClient } from "../api/client.ts";
import { messageData, noteData, peerRows, peerTask, threadSubject, useCoordination } from "../coordination.tsx";
import { usePeople } from "../people.tsx";

export interface EpicCoordinationProps {
  client: ApiClient;
  epicId: string;
  /** The epic as the tree has it: its tasks' owners give an agent's note its person's face. */
  epic: NavEpic | null;
  /** Bumped when the shell's tree reloads. */
  version: number;
  onOpenTask: (taskId: string) => void;
}

/** Why a pin or retire was refused, in the words the orchestrator gave. */
function refusal(err: unknown): string {
  return err instanceof ApiError ? err.message : "It could not be saved. Try again.";
}

export function EpicCoordination({ client, epicId, epic, version, onOpenTask }: EpicCoordinationProps) {
  const people = usePeople();
  const { value, problem, reload } = useCoordination(client, epicId, version);
  const { toast } = useToast();
  const [text, setText] = useState("");
  const [replacing, setReplacing] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [retiring, setRetiring] = useState<EpicNoteRecord | null>(null);
  const field = useRef<HTMLTextAreaElement>(null);

  // An agent's note wears its task's owner's face.
  const owners = useMemo(() => new Map((epic?.tasks ?? []).flatMap((t) => (t.people?.[0] ? [[t.id, t.people[0]] as const] : []))), [epic]);
  const you = people.you ? people.names.get(people.you) : undefined;

  if (!value) {
    return problem
      ? <Callout tone="danger" data-testid="coordination-problem">Could not read this epic's coordination: {problem}</Callout>
      : <div className="centered"><Spinner label="Loading…" /></div>;
  }
  const { notes, threads, peers } = value;
  const live = notes.live;
  const asData = (n: EpicNoteRecord): EpicNoteData => noteData(n, people, owners);
  // Newest first, as the doc lays them: the latest settled thing on top.
  const current = [...notes.current].sort((a, b) => b.n - a.n);
  const newest = current[0]?.n;

  const pin = async () => {
    const words = text.trim();
    if (!words) return;
    setBusy(true);
    setError(null);
    try {
      const pinned = await client.pinEpicNote(epicId, { text: words, ...(replacing ? { replaces: replacing } : {}) });
      setText("");
      setReplacing(null);
      toast({ title: replacing ? `Note ${pinned.n} replaces note ${replacing}` : `Note ${pinned.n} pinned for the epic`, tone: "success" });
      reload();
    } catch (err) {
      setError(refusal(err));
    } finally {
      setBusy(false);
    }
  };
  const retire = async (n: EpicNoteRecord) => {
    setRetiring(null);
    try {
      await client.retireEpicNote(epicId, n.n);
      toast({ title: `Note ${n.n} retired`, tone: "success" });
      reload();
    } catch (err) {
      toast({ title: refusal(err), tone: "danger" });
    }
  };

  return (
    <div className="coord" data-testid="coordination" id="epic-coordination-panel" role="tabpanel" aria-labelledby="epic-coordination-tab">
      <div className="coordGrid">
        <div className="coordMain">
          <Section title="Epic notes" count={notes.current.length} limit={notes.limit} countNoun="notes" data-testid="epic-notes">
            <p className="coordLead">
              Working facts every agent on this epic must follow, at most {notes.limit} at a time. A note is never edited: a new one
              replaces it, or a person retires it. Every agent is told at its next step when one is added, replaced or retired, and
              every Run that starts here has the current ones in its briefing.
            </p>
            {current.length > 0 ? (
              <ul className="coordNotes">
                {current.map((n) => (
                  <EpicNote key={n.id} note={asData(n)} live={live} isNew={n.n === newest && n.replaces !== null}
                    onReplace={() => {
                      setReplacing(n.n);
                      setText(n.text);
                      setError(null);
                      requestAnimationFrame(() => field.current?.focus());
                    }}
                    onRetire={() => setRetiring(n)} />
                ))}
              </ul>
            ) : (
              <p className="coordEmpty">No notes pinned yet.</p>
            )}
            <EpicNoteHistory notes={notes.history.map(asData)} live={live} />
            <div className="coordNoteForm">
              <Textarea ref={field} label={replacing ? `Replace note ${replacing}` : "New note"} rows={2} maxRows={8} value={text}
                maxLength={EPIC_NOTE_TEXT_MAX} placeholder="A contract, a decision, where the docs are…" error={error ?? undefined}
                onChange={(e) => setText(e.target.value)} data-testid="note-text" />
              <div className="coordNoteActions">
                <span>
                  {you ? `As ${firstName(you)} · ` : ""}every agent on the epic is told at its next step. Text only: it can't steer or start anything.
                </span>
                {replacing ? (
                  <Button size="sm" variant="quiet" onClick={() => {
                    setReplacing(null);
                    setText("");
                    setError(null);
                  }}>Cancel</Button>
                ) : null}
                <Button size="sm" variant="secondary" disabled={busy || !text.trim()} onClick={() => void pin()} data-testid="pin-note">
                  {replacing ? `Replace note ${replacing}` : "Pin for the epic"}
                </Button>
              </div>
            </div>
          </Section>
          <Section title="Between tasks" count={threads.threads.length} data-testid="epic-threads">
            {threads.threads.length === 0 ? (
              <p className="coordEmpty">No agent on this epic has messaged another yet.</p>
            ) : (
              <div className="coordThreads">
                {threads.threads.map((t) => <Thread key={t.id} thread={t} peers={peers.tasks} />)}
              </div>
            )}
          </Section>
        </div>
        <Section title="Tasks on this epic" count={peers.tasks.length} data-testid="epic-peers">
          <PeerList label="Tasks on this epic" peers={peerRows(peers.tasks)}
            onOpen={(key) => {
              const t = peers.tasks.find((p) => p.key === key);
              if (t) onOpenTask(t.taskId);
            }} />
        </Section>
      </div>
      {retiring ? (
        <Dialog open onOpenChange={(o) => !o && setRetiring(null)} tone="danger" size="sm" title={`Retire note ${retiring.n}?`}
          description="It leaves the epic's notes with no replacement. Every agent on the epic is told at its next step; Runs that start later are not briefed with it."
          footer={<>
            <Button variant="quiet" onClick={() => setRetiring(null)}>Keep it</Button>
            <Button variant="danger" solid onClick={() => void retire(retiring)} data-testid="retire-confirm">Retire note {retiring.n}</Button>
          </>} />
      ) : null}
    </div>
  );
}

/** One thread between two tasks' agents: read only. */
function Thread({ thread, peers }: { thread: PeerThreadRecord; peers: ReadonlyArray<import("@dude/domain").PeerState> }) {
  const [asker, asked] = thread.pair;
  const first = thread.messages[0]!;
  const byId = new Map(thread.messages.map((m) => [m.id, m]));
  const replyAt = thread.messages.find((m) => m.replyTo !== null && m.from.taskId === asked.taskId)?.sentAt;
  const state = thread.state === "waiting" && thread.wait
    ? { kind: "waiting" as const, on: asked.key, since: thread.wait.since }
    : thread.state === "answered" && replyAt ? { kind: "answered" as const, at: replyAt }
    : { kind: "open" as const, on: asked.key };
  const waitOn = thread.state === "waiting" ? thread.wait : null;
  return (
    <PeerThread a={peerTask(asker.key, peers, asker.title)} b={peerTask(asked.key, peers, asked.title)} subject={threadSubject(first.text)}
      state={state} foot={<span>Between {asker.key}'s and {asked.key}'s agents. Everyone on the epic can read it; only the two agents write in it.</span>}>
      {thread.messages.map((m) => (
        <PeerMessage key={m.id} compact msg={messageData(m, peers)} replyToText={m.replyTo ? byId.get(m.replyTo)?.text : undefined}
          waiting={waitOn && waitOn.messageId === m.id ? {
            since: waitOn.since,
            note: waitOn.parksAt ? `Parks at ${new Date(waitOn.parksAt).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false })} if no reply.` : undefined,
          } : undefined} />
      ))}
    </PeerThread>
  );
}

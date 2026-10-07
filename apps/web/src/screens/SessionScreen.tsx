/**
 * A brainstorm session: its members' conversation with one agent that
 * reads their linked projects and proposes work, which a member files with
 * a click, as themselves.
 *
 * Its Chat is the session's ledger folded as a conductor's is (every
 * agent the session had, in order), with its proposal cards and what
 * happened to its membership merged in by time. The rail says who is in
 * it (and has it open), what it reads, what it can do, and what it cost.
 * A reader sees all of it and writes nothing; a question put to one
 * member is answered only by them, while anyone else's message waits.
 */

import { Fragment, useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { firstName, type NavProject } from "@dude/design-system";
import {
  Capabilities, ChatAside, ChatComposer, ChatMessage, ChatNotice, ChatTranscript, CostDisplay, LinkedProjects, ProposalCard,
  ScreenHeader, SessionFacts, SessionPeople, SessionRail, SessionRailBlock, SharedMark, type ProposalCardItem,
} from "@dude/design-system/components";
import { Button, Callout, Spinner } from "@dude/design-system/primitives";
import { EventTypes, type PersistedEvent, type Proposal, type ProposalItem, type RunStatus, type SessionDetail, type SessionMemberView } from "@dude/domain";
import { ApiError, type ApiClient } from "../api/client.ts";
import { apply, emptyProjection, snapshot, steerWait, type Turn } from "../api/conversation.ts";
import { dudeName } from "../DudeMark.tsx";
import { useEventStream, useReloadOnEvents } from "../hooks/useEventStream.ts";
import { errorText } from "../hooks/useSave.tsx";
import { usePeople, type People } from "../people.tsx";
import { NotFound } from "./NotFound.tsx";
import { asides, interleaved, renderTurn, type SteerActions } from "./RunScreen.tsx";
import { LinkDialog, MakeOwnerDialog, ShareDialog } from "./SessionDialogs.tsx";

/** How often the page says it is open: the API counts it open for 90 seconds. */
const OPEN_EVERY_MS = 60_000;

/** The session's own events that change what the page reads (people, links, the card). */
const SESSION_EVENTS: ReadonlySet<string> = new Set([
  EventTypes.BrainstormShared, EventTypes.BrainstormJoined, EventTypes.BrainstormDeclined, EventTypes.BrainstormRoleChanged,
  EventTypes.BrainstormMemberRemoved, EventTypes.BrainstormOwnerChanged, EventTypes.BrainstormLinked, EventTypes.BrainstormProposed,
  EventTypes.BrainstormFiled, EventTypes.BrainstormOpen, EventTypes.RunCreated, EventTypes.RunStarted, EventTypes.RunCompleted,
  EventTypes.RunFailed, EventTypes.RunAborted, EventTypes.RunPaused, EventTypes.RunResumed, EventTypes.QuestionAsked,
  EventTypes.QuestionAnswered, "run.parked", "run.unparked",
]);

export function SessionScreen({ client, sessionId, projects, onBack, onChanged }: {
  client: ApiClient;
  sessionId: string;
  projects: readonly NavProject[];
  onBack: () => void;
  /** Its people or title changed for you (handed over, left): the sidebar's list re-reads. */
  onChanged: () => void;
}) {
  const people = usePeople();
  const [detail, setDetail] = useState<SessionDetail | null>(null);
  const [missing, setMissing] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const [dialog, setDialog] = useState<"share" | "link" | null>(null);
  const [handing, setHanding] = useState<SessionMemberView | null>(null);

  const load = useCallback(async () => {
    try {
      setDetail(await client.getSession(sessionId));
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) setMissing(true);
      else setProblem(errorText(err));
    }
  }, [client, sessionId]);
  useEffect(() => {
    setDetail(null);
    setMissing(false);
    void load();
  }, [load]);
  useReloadOnEvents({ client, sessionId }, () => void load(), 300, (e) => !SESSION_EVENTS.has(e.eventType));

  // Open, for its members' rail: said now, every minute, and no longer on leaving.
  useEffect(() => {
    const say = (open: boolean) => void client.sessionOpen(sessionId, open).catch(() => undefined);
    say(true);
    const t = setInterval(() => say(true), OPEN_EVERY_MS);
    return () => {
      clearInterval(t);
      say(false);
    };
  }, [client, sessionId]);

  const { events } = useEventStream({ client, sessionId });
  const status = (detail?.session.run?.status ?? undefined) as RunStatus | undefined;
  const projection = useRef(emptyProjection());
  const projected = useRef(sessionId);
  const conversation = useMemo(() => {
    if (projected.current !== sessionId) {
      projection.current = emptyProjection();
      projected.current = sessionId;
    }
    return snapshot(apply(projection.current, events), status);
  }, [events, sessionId, status]);
  const grouped = useMemo(() => asides(conversation.turns), [conversation]);

  const send = useCallback(async (text: string) => {
    setProblem(null);
    try {
      await client.sessionChat(sessionId, text);
      return true;
    } catch (err) {
      setProblem(`Could not send the message: ${errorText(err)}`);
      return false;
    }
  }, [client, sessionId]);

  const linkedKeys = useMemo(() => new Map((detail?.session.projects ?? []).map((p) => [p.key.toUpperCase(), p])), [detail]);
  const lines = useMemo(() => {
    if (!detail) return [];
    const out: Array<{ id: string; at: string; node: ReactNode }> = [];
    for (const proposal of detail.proposals ?? []) {
      out.push({ id: proposal.id, at: proposal.createdAt, node: (
        <ProposalBlock key={proposal.id} client={client} sessionId={sessionId} proposal={proposal} linked={linkedKeys}
          readOnly={detail.you.role === "read"} filingAs={firstName(people.names.get(detail.you.id) ?? "you")} onFiled={() => void load()} />
      ) });
    }
    for (const e of events) {
      const text = sessionNotice(e, people);
      if (text) out.push({ id: e.eventId, at: e.occurredAt, node: <ChatNotice key={e.eventId} kind="notice" by={dudeName(sessionId)} text={text} at={e.occurredAt} data-testid="session-notice" /> });
    }
    return out.sort((a, b) => a.at.localeCompare(b.at));
  }, [detail, events, people, client, sessionId, linkedKeys, load]);

  if (missing) return <NotFound what="session" onBack={onBack} />;
  if (!detail) return <div className="centered">{problem ?? <Spinner label="Loading the session…" />}</div>;

  const { session, you, question } = detail;
  const owner = session.people.find((m) => m.role === "owner");
  const isOwner = you.role === "owner";
  const reader = you.role === "read";
  const live = status !== undefined && !["completed", "failed", "aborted"].includes(status);
  const dude = dudeName(sessionId);
  const steer: SteerActions | undefined = live ? {
    wait: (turn) => steerWait(turn, status!, conversation.activeTool?.name ?? null, conversation.lands),
    resend: () => undefined,
  } : undefined;
  const render = (turn: Turn) => renderTurn(turn, "brainstorm", conversation.contextWindow, !live, people, dude, undefined, undefined, steer);
  // A question for you: its chips are yours. One for someone else: anyone may still write, after their answer.
  const yours = question?.yours ? question : null;
  const others = question && !question.yours ? question : null;
  const shared = session.people.filter((m) => m.accepted).length > 1;
  const members = session.people.map((m) => ({
    person: people.byId.get(m.person.id) ?? m.person, role: m.role, open: m.open, invited: !m.accepted, you: m.person.id === you.id,
  }));

  return (
    <div className="screen sessionScreen" data-testid="session-screen" data-role={you.role}>
      <ScreenHeader
        title={session.title}
        meta={<>
          {shared ? <SharedMark owner={owner && owner.person.id !== you.id ? owner.person : undefined}
            label={`Shared with ${session.people.filter((m) => m.accepted).length - 1}`} /> : null}
          <span>Brainstorm{session.run?.model ? ` · ${session.run.model}` : ""}</span>
        </>}
        actions={isOwner ? <Button size="sm" variant="secondary" onClick={() => setDialog("share")} data-testid="share-open">Share</Button> : undefined}
      />
      <div className="runScreen" data-view="chat">
        <div className="runView">
          <div className="runChat">
            <ChatTranscript
              fill
              live={live}
              revision={events.length + (detail.proposals?.length ?? 0)}
              turns={conversation.turns.length}
              emptyMessage={reader ? "Nobody has written here yet." : "Write to start: the agent reads the linked projects, asks what it needs, and proposes work you file yourself. It changes nothing."}
              footer={
                <ChatComposer
                  mode={yours ? "answer" : "chat"}
                  question={yours ? { id: yours.id, text: yours.prompt, askedBy: "the brainstorm", askedAt: yours.askedAt, options: yours.options } : undefined}
                  disabled={reader}
                  disabledReason={reader ? "You can read this session: writing is for its owner and members who can chat." : undefined}
                  placeholder={others ? `Waiting for ${firstName(others.to?.name ?? "someone")} to answer: what you write goes after it.` : undefined}
                  onSubmit={({ text }) => send(text)}
                  sentAs={people.names.get(you.id) ? `${firstName(people.names.get(you.id)!)} · everyone in the session sees it` : undefined}
                  to={<>To <b>Brainstorm</b></>}
                  data-testid="session-composer"
                />
              }
            >
              {interleaved(grouped, lines).map((item) => "node" in item
                ? <Fragment key={item.id}>{item.node}</Fragment>
                : Array.isArray(item.group)
                  ? <ChatAside key={item.group[0]!.id}>{item.group.map(render)}</ChatAside>
                  : render(item.group))}
              {conversation.activity ? (
                <ChatMessage role="brainstorm" activity={conversation.activity}
                  activityProps={conversation.activeTool ? { label: conversation.activeTool.name, since: conversation.activeTool.since } : undefined} />
              ) : null}
            </ChatTranscript>
            <SessionRail className="runRail" aria-label="The session" data-testid="session-rail">
              <SessionRailBlock label={<span className="sessionRailHead">People{isOwner ? (
                <Button size="sm" variant="quiet" onClick={() => setDialog("share")}>Share</Button>) : null}</span>}>
                <SessionPeople members={members} />
              </SessionRailBlock>
              <SessionRailBlock label={<span className="sessionRailHead">Linked{isOwner ? (
                <Button size="sm" variant="quiet" onClick={() => setDialog("link")} data-testid="link-open">Link</Button>) : null}</span>}>
                {session.projects.length > 0 ? <LinkedProjects projects={session.projects} />
                  : <span className="muted">Nothing linked: it reads only what the organisation remembers.</span>}
              </SessionRailBlock>
              <SessionRailBlock label="It can">
                <Capabilities can={["Read the linked projects' code, tasks, pull requests and findings", "Propose epics, tasks, edits and comments · members file them"]}
                  cannot={["Change code, push, start or steer work", "Read projects nobody linked"]} />
              </SessionRailBlock>
              <SessionRailBlock label="Cost">
                <SessionFacts facts={[{ label: "Session", value: <CostDisplay usd={session.costUsd} /> }]} />
              </SessionRailBlock>
            </SessionRail>
          </div>
        </div>
        {problem ? <Callout tone="danger" data-testid="session-problem">{problem}</Callout> : null}
      </div>
      {isOwner ? (
        <>
          <ShareDialog client={client} detail={detail} open={dialog === "share"} onClose={() => setDialog(null)}
            onChanged={() => void load()} onMakeOwner={(m) => {
              setDialog(null);
              setHanding(m);
            }} />
          <LinkDialog client={client} projects={projects} linked={session.projects} open={dialog === "link"} onClose={() => setDialog(null)}
            onSave={async (links) => {
              await client.linkSession(sessionId, links);
              await load();
            }} />
          <MakeOwnerDialog client={client} detail={detail} member={handing} onClose={() => setHanding(null)}
            onChanged={() => {
              void load();
              onChanged();
            }} />
        </>
      ) : null}
    </div>
  );
}

/** What the session's ledger says of its people and links, as a line in its Chat. */
export function sessionNotice(e: PersistedEvent, people: People): string | null {
  const p = e.payload ?? {};
  const name = (id: unknown) => (typeof id === "string" ? people.names.get(id) ?? "someone" : "someone");
  const by = name(e.actor?.id);
  switch (e.eventType) {
    case EventTypes.BrainstormShared: {
      const who = Array.isArray(p.people) ? p.people.map(name) : [];
      if (p.role === "owner") return `${by} asked ${who.join(", ")} to take the session over.`;
      return `${by} shared this with ${who.join(", ")} (${p.role === "read" ? "can read" : "can chat"}).`;
    }
    case EventTypes.BrainstormJoined:
      return `${name(p.person)} joined.`;
    case EventTypes.BrainstormDeclined:
      return `${name(p.person)} declined.`;
    case EventTypes.BrainstormMemberRemoved:
      return `${by} removed ${name(p.person)}.`;
    case EventTypes.BrainstormRoleChanged:
      return `${name(p.person)} can ${p.role === "read" ? "read" : "chat"} now.`;
    case EventTypes.BrainstormOwnerChanged: {
      const to = typeof p.toName === "string" ? p.toName : name(p.to);
      const from = typeof p.fromName === "string" ? firstName(p.fromName) : name(p.from);
      const keep = p.keep === "leave" ? "and left" : p.keep === "read" ? "and stays as can read" : "and stays as can chat";
      return `${from} made ${to} the owner, ${keep}.`;
    }
    case EventTypes.BrainstormLinked:
      return `${by} changed what the session reads.`;
    case EventTypes.BrainstormFiled: {
      const filed = Array.isArray(p.filed) ? p.filed as Array<{ key?: string }> : [];
      return `${typeof p.by === "string" ? p.by : by} filed ${filed.map((f) => f.key).filter(Boolean).join(", ")}.`;
    }
    default:
      return null;
  }
}

/** One proposal's card: what the person looking can file, ticked, and filing it. */
function ProposalBlock({ client, sessionId, proposal, linked, readOnly, filingAs, onFiled }: {
  client: ApiClient;
  sessionId: string;
  proposal: Proposal;
  linked: ReadonlyMap<string, { key: string; name: string }>;
  readOnly: boolean;
  filingAs: string;
  onFiled: () => void;
}) {
  const fileable = proposal.items.flatMap((_, i) => (proposal.status[i]?.canFile && !proposal.status[i]?.filed ? [i] : []));
  const [selected, setSelected] = useState<Set<number>>(() => new Set(fileable));
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const epics = new Set(proposal.items.filter((i) => i.kind === "epic").map((i) => i.title));
  const items = proposal.items.map((item, i) => cardItem(item, proposal.status[i] ?? {}, linked, epics));
  const file = async () => {
    setBusy(true);
    setProblem(null);
    try {
      // Only what is still yours to file: another member may have filed a ticked item since.
      const { results } = await client.fileProposal(sessionId, proposal.id, [...selected].filter((i) => fileable.includes(i)).sort((a, b) => a - b));
      const refused = results.filter((r) => r.status === "refused");
      if (refused.length > 0) setProblem(refused.map((r) => `${items[r.item]?.title ?? "an item"}: ${r.why ?? "refused"}`).join(" · "));
      setSelected(new Set());
      onFiled();
    } catch (err) {
      setProblem(`Could not file: ${errorText(err)}`);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div data-testid="proposal" data-proposal={proposal.id}>
      <ProposalCard items={items} selected={selected} filingAs={filingAs} busy={busy} readOnly={readOnly} onFile={() => void file()}
        onToggle={(i) => setSelected((s) => {
          const n = new Set(s);
          if (n.has(i)) n.delete(i);
          else n.add(i);
          return n;
        })} />
      {problem ? <Callout tone="danger" data-testid="proposal-problem">{problem}</Callout> : null}
    </div>
  );
}

function cardItem(item: ProposalItem, status: Proposal["status"][number], linked: ReadonlyMap<string, { key: string; name: string }>,
  epics: ReadonlySet<string | undefined>): ProposalCardItem {
  const project = item.project ? linked.get(item.project.toUpperCase()) : undefined;
  const common = {
    canFile: Boolean(status.canFile),
    ...(status.why ? { why: status.why } : {}),
    ...(status.filed ? { filed: { by: status.filedBy ?? "Someone", key: status.key ?? "" } } : {}),
  };
  switch (item.kind) {
    case "epic":
      return { kind: "epic", title: item.title ?? "", detail: item.description ?? item.goal, project, ...common };
    case "task":
      return { kind: "task", title: item.title ?? "", detail: item.goal, project, child: epics.has(item.epic), ...common };
    case "edit":
      return { kind: "edit", title: `Edit ${item.task ?? ""}`, taskKey: item.task,
        before: textOf(item.before), after: textOf(item.after), ...common };
    case "comment":
      return { kind: "comment", title: `Comment on ${item.task ?? ""}`, taskKey: item.task, detail: item.text ? `“${item.text}”` : undefined, ...common };
  }
}

function textOf(t: { goal?: string; acceptanceCriteria?: string[] } | undefined): ReactNode {
  if (!t) return null;
  return (
    <>
      {t.goal ? <div>{t.goal}</div> : null}
      {t.acceptanceCriteria?.length ? <ul>{t.acceptanceCriteria.map((c, i) => <li key={i}>{c}</li>)}</ul> : null}
    </>
  );
}

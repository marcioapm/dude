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
  Capabilities, ChatAside, ChatComposer, ChatMessage, ChatNotice, ChatTranscript, CostDisplay, LinkedProjects, ModelPicker, ProposalCard, PublishedFiles,
  ScreenHeader, Segmented, SessionFacts, SessionPeople, SessionRail, SessionRailBlock, SessionTitle, SharedMark, type ModelChoice, type ProposalCardItem,
} from "@dude/design-system/components";
import { Button, Callout, Spinner } from "@dude/design-system/primitives";
import { EventTypes, HARNESS_LABEL, UNTITLED_SESSION, harnessSchema, type PersistedEvent, type Proposal, type ProposalItem, type RunStatus, type SessionDetail, type SessionMemberView } from "@dude/domain";
import { ApiError, type ApiClient, type Artifact } from "../api/client.ts";
import { modelChangeWords, organizationOf, pickerTier, useModelOptions } from "../sessionModel.ts";
import { ArtifactViewer, filesOf, save } from "./FilesSection.tsx";
import { apply, emptyProjection, snapshot, steerWait, type QuestionTurn, type Turn } from "../api/conversation.ts";
import type { QuestionSubmission } from "@dude/design-system/components";
import { dudeName } from "../DudeMark.tsx";
import { useEventStream, useReloadOnEvents } from "../hooks/useEventStream.ts";
import { useVisibleInterval } from "../hooks/useVisibleInterval.ts";
import { errorText } from "../hooks/useSave.tsx";
import { usePeople, type People } from "../people.tsx";
import { NotFound } from "./NotFound.tsx";
import { EventLog, asides, conversationOption, eventsOption, interleaved, renderTurn, type SteerActions } from "./RunScreen.tsx";
import { LinkDialog, MakeOwnerDialog, ShareDialog } from "./SessionDialogs.tsx";

/** How often the page says it is open: the API counts it open for 90 seconds. */
const OPEN_EVERY_MS = 60_000;

/** What the session shows: its conversation, or its ledger. There are no Changes: a session changes no code. */
type SessionView = "chat" | "events";

/** The session's own events that change what the page reads (people, links, the card). */
const SESSION_EVENTS: ReadonlySet<string> = new Set([
  EventTypes.BrainstormShared, EventTypes.BrainstormJoined, EventTypes.BrainstormDeclined, EventTypes.BrainstormRoleChanged,
  EventTypes.BrainstormMemberRemoved, EventTypes.BrainstormOwnerChanged, EventTypes.BrainstormLinked, EventTypes.BrainstormProposed,
  EventTypes.BrainstormFiled, EventTypes.BrainstormRenamed, EventTypes.BrainstormModelChanged, EventTypes.BrainstormModelFallback,
  EventTypes.RunCreated, EventTypes.RunStarted, EventTypes.RunCompleted,
  EventTypes.RunFailed, EventTypes.RunAborted, EventTypes.RunPaused, EventTypes.RunResumed, EventTypes.QuestionAsked,
  EventTypes.QuestionAnswered, EventTypes.QuestionClosed, "run.parked", "run.unparked",
]);

/** The detail with one member's open state as a session.open says it: the rest as it was. */
export function withOpen(detail: SessionDetail, personId: string, open: boolean): SessionDetail {
  const people = detail.session.people;
  if (!people.some((m) => m.person.id === personId && m.open !== open)) return detail;
  return { ...detail, session: { ...detail.session, people: people.map((m) => (m.person.id === personId ? { ...m, open } : m)) } };
}

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
  const [view, setView] = useState<SessionView>("chat");

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
  // Someone opening or leaving it changes their dot, nothing the page reads:
  // it is applied where it is, and only what changes the page re-reads it.
  useReloadOnEvents({ client, sessionId }, () => void load(), 300, (e) => {
    if (e.eventType === EventTypes.BrainstormOpen) {
      const { personId, open } = e.payload as { personId?: unknown; open?: unknown };
      if (typeof personId === "string") setDetail((d) => (d ? withOpen(d, personId, open === true) : d));
      return true;
    }
    return !SESSION_EVENTS.has(e.eventType);
  });

  // Open, for its members' rail: said now and every minute while the page is
  // shown (a background tab says nothing), and no longer on leaving.
  useVisibleInterval(() => void client.sessionOpen(sessionId, true).catch(() => undefined), OPEN_EVERY_MS);
  useEffect(() => () => void client.sessionOpen(sessionId, false).catch(() => undefined), [client, sessionId]);

  const { events } = useEventStream({ client, sessionId });
  const status = (detail?.session.run?.status ?? undefined) as RunStatus | undefined;

  // What its agent published: read on opening, and again as each new one is recorded.
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [viewing, setViewing] = useState<string | null>(null);
  const published = useMemo(() => events.filter((e) => e.eventType === EventTypes.ArtifactCreated).length, [events]);
  useEffect(() => {
    let current = true;
    void client.listSessionArtifacts(sessionId).then((r) => current && setArtifacts(r.artifacts), () => undefined);
    return () => {
      current = false;
    };
  }, [client, sessionId, published]);
  const files = useMemo(() => filesOf(artifacts), [artifacts]);
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

  const send = useCallback(async (text: string, aside = false) => {
    setProblem(null);
    try {
      await client.sessionChat(sessionId, text, { aside });
      return true;
    } catch (err) {
      setProblem(`Could not send the message: ${errorText(err)}`);
      return false;
    }
  }, [client, sessionId]);

  // The agent's question answered through its form: the whole ask at once.
  const answer = useCallback(async (turn: QuestionTurn, s: QuestionSubmission) => {
    setProblem(null);
    try {
      await client.sessionAnswer(sessionId, turn.questionId, s.answers, s.note);
      return true;
    } catch (err) {
      setProblem(`Could not answer: ${errorText(err)}`);
      return false;
    }
  }, [client, sessionId]);

  const rename = useCallback(async (title: string) => {
    setProblem(null);
    try {
      await client.renameSession(sessionId, title);
      setDetail((d) => (d ? { ...d, session: { ...d.session, title, titledBy: "person" } } : d));
      onChanged();
    } catch (err) {
      setProblem(`Could not rename it: ${errorText(err)}`);
      throw err;
    }
  }, [client, sessionId, onChanged]);

  const linkedKeys = useMemo(() => new Map((detail?.session.projects ?? []).map((p) => [p.key.toUpperCase(), p])), [detail]);
  // The owner's picker lists the organisation's tiers; anyone else reads the detail's words alone.
  const tierOptions = useModelOptions(client, detail?.you.role === "owner" && detail.model !== undefined, false);
  // What the owner picked, shown at once: a second pick before the first is answered builds on it,
  // and the posts go one after another so the last pick is the one kept.
  const [picked, setPicked] = useState<ModelChoice | null>(null);
  const posting = useRef<Promise<unknown>>(Promise.resolve());
  useEffect(() => setPicked(null), [sessionId]);
  const chooseModel = useCallback((choice: ModelChoice) => {
    setProblem(null);
    setPicked(choice);
    posting.current = posting.current.then(async () => {
      try {
        const { model } = await client.setSessionModel(sessionId, choice);
        setDetail((d) => (d ? { ...d, model } : d));
      } catch (err) {
        setPicked(null);
        setProblem(`Could not change the model: ${errorText(err)}`);
      }
    });
  }, [client, sessionId]);
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
      // A rename is signed by whoever named it: the agent, or the person its words name. A person's model change names them too.
      const renamed = e.eventType === EventTypes.BrainstormRenamed;
      const byPerson = e.eventType === EventTypes.BrainstormModelChanged;
      const by = renamed ? (e.payload.by === "agent" ? "Brainstorm" : undefined) : byPerson ? undefined : dudeName(sessionId);
      if (text) out.push({ id: e.eventId, at: e.occurredAt, node: <ChatNotice key={e.eventId}
        kind={e.eventType === EventTypes.BrainstormTurnStopped ? "stopped" : renamed ? "renamed" : "notice"}
        by={by} text={text} at={e.occurredAt} data-testid="session-notice" /> });
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
  const render = (turn: Turn) => renderTurn(turn, "brainstorm", conversation.contextWindow, !live, people, dude, undefined, undefined, steer,
    undefined, undefined, live && !reader ? { answer } : undefined);
  // A question for you: answered in its turn, the composer steps back. One for someone else: anyone may still write, after their answer.
  const yours = question?.yours && !reader ? question : null;
  const others = question && !question.yours ? question : null;
  const shared = session.people.filter((m) => m.accepted).length > 1;
  const members = session.people.map((m) => ({
    person: people.byId.get(m.person.id) ?? m.person, role: m.role, open: m.open, invited: !m.accepted, you: m.person.id === you.id,
  }));

  return (
    <div className="screen sessionScreen" data-testid="session-screen" data-role={you.role}>
      <ScreenHeader
        fillTitle
        title={<SessionTitle title={session.title} untitled={UNTITLED_SESSION} maxLength={200}
          onRename={reader ? undefined : rename} />}
        meta={<>
          {shared ? <SharedMark owner={owner && owner.person.id !== you.id ? owner.person : undefined}
            label={`Shared with ${session.people.filter((m) => m.accepted).length - 1}`} /> : null}
          <span data-testid="session-header-model">Brainstorm{headerModel(detail)}</span>
        </>}
        actions={isOwner ? <Button size="sm" variant="secondary" onClick={() => setDialog("share")} data-testid="share-open">Share</Button> : undefined}
      />
      <div className="runScreen" data-view={view}>
        {/* The Run screen's switch, without Changes: a session changes no code. */}
        <div className="runBar">
          <Segmented<SessionView> label="Show" value={view} onChange={setView} data-testid="session-view"
            options={[conversationOption, eventsOption(events.length)]} />
        </div>
        <div className="runView">
          {view === "events" ? <EventLog events={events} people={people} /> : (
          <div className="runChat">
            <ChatTranscript
              fill
              live={live}
              revision={events.length + (detail.proposals?.length ?? 0)}
              turns={conversation.turns.length}
              emptyMessage={reader ? "Nobody has written here yet." : "Write to start: the agent reads the linked projects, asks what it needs, and proposes work you file yourself. It changes nothing."}
              footer={
                <ChatComposer
                  mode="chat"
                  waitingFor={yours ? "The brainstorm" : undefined}
                  waitingKey={yours?.id}
                  disabled={reader}
                  // An empty session (made through the API without a message) is for writing in.
                  autoFocus={!reader && session.messages === 0}
                  disabledReason={reader ? "You can read this session: writing is for its owner and members who can chat." : undefined}
                  // A session has no task: the chat composer's own words would say "this task".
                  placeholder={others ? `Waiting for ${firstName(others.to?.name ?? "someone")} to answer: what you write goes after it.`
                    : "Message the brainstorm…"}
                  // Written beside your own open question ("Write to the agent instead"): aside, never the answer.
                  onSubmit={({ text }) => send(text, yours !== null)}
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
              {detail.model ? (
                <SessionRailBlock label="Model" data-testid="session-model">
                  <ModelPicker
                    tiers={tierOptions?.tiers ?? (detail.model.tier ? [pickerTier(detail.model.tier)] : [])}
                    organization={organizationOf(detail.model)}
                    value={picked ?? { tier: detail.model.tier?.id ?? null, harness: detail.model.harness }}
                    readOnly={!isOwner || !tierOptions}
                    onChange={chooseModel} />
                  <span className="muted sessionModelNote">Applies the next time the agent starts.</span>
                </SessionRailBlock>
              ) : null}
              <SessionRailBlock data-testid="session-files" label={<span className="sessionRailHead">
                <span>Files{files.length > 0 ? <> <span className="ds-tnum runCount" data-testid="session-files-count">{files.length}</span></> : null}</span>
                {files.length > 1 ? (
                  <Button size="sm" variant="quiet" data-testid="session-files-zip" onClick={() => void client.sessionArtifactsZip(sessionId)
                    .then((b) => save(b, "session-files.zip"), (err: unknown) => setProblem(`Could not download them: ${errorText(err)}`))}>
                    Download all
                  </Button>
                ) : null}
              </span>}>
                {files.length > 0 ? (
                  <PublishedFiles files={files.map((f) => ({ name: f.name, contentType: f.versions[0]!.contentType, versions: f.versions[0]!.version,
                    description: f.versions[0]!.description }))}
                    onOpen={setViewing} />
                ) : <span className="muted">Nothing published yet: documents it writes for you appear here.</span>}
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
          )}
        </div>
        {problem ? <Callout tone="danger" data-testid="session-problem">{problem}</Callout> : null}
      </div>
      <ArtifactViewer client={client} files={files} open={viewing} onOpenChange={setViewing} />
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

/**
 * The header's model: what the agent runs on now while one is live (a
 * change waits for its next start), else what its next start would use;
 * "(organisation default)" when the session follows the organisation.
 */
export function headerModel(detail: SessionDetail): string {
  const run = detail.session.run;
  const live = run && !["completed", "failed", "aborted"].includes(run.status);
  if (live && run.model) return ` · ${run.model}`;
  const m = detail.model;
  if (!m) return run?.model ? ` · ${run.model}` : "";
  const words = `${m.effective.tierName ?? "no tier"} · ${HARNESS_LABEL[m.effective.harness]}`;
  return ` · ${words}${m.tier === null && m.harness === null ? " (organisation default)" : ""}`;
}

/** What the session's ledger says of its people and links, as a line in its Chat. */
export function sessionNotice(e: PersistedEvent, people: People): string | null {
  const p = e.payload ?? {};
  const name = (id: unknown) => (typeof id === "string" ? people.names.get(id) ?? "someone" : "someone");
  const by = name(e.actor?.id);
  switch (e.eventType) {
    case EventTypes.BrainstormTurnStopped:
      return `Stopped Brainstorm's turn: ${typeof p.tool === "string" ? p.tool : "tool"} was open for 10\u00a0min.`;
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
    case EventTypes.BrainstormRenamed: {
      const title = typeof p.title === "string" ? p.title : "";
      return p.by === "agent" ? `Named it “${title}”` : `${firstName(name(p.by))} renamed it “${title}”`;
    }
    case EventTypes.BrainstormFiled: {
      const filed = Array.isArray(p.filed) ? p.filed as Array<{ key?: string }> : [];
      return `${typeof p.by === "string" ? p.by : by} filed ${filed.map((f) => f.key).filter(Boolean).join(", ")}.`;
    }
    case EventTypes.BrainstormModelChanged: {
      const tier = p.tier && typeof p.tier === "object" && typeof (p.tier as { name?: unknown }).name === "string" ? p.tier as { name: string } : null;
      const harness = harnessSchema.safeParse(p.harness);
      const who = firstName(name(p.by ?? e.actor?.id));
      if (!tier && !harness.success) return `${who} set the model back to the organisation's default; it applies the next time the agent starts.`;
      return `${who} set the model to ${modelChangeWords(tier, harness.success ? harness.data : null)}; it applies the next time the agent starts.`;
    }
    case EventTypes.BrainstormModelFallback: {
      const tier = p.tier && typeof p.tier === "object" ? (p.tier as { name?: unknown }).name : null;
      return `The tier ${typeof tier === "string" ? tier : "this session chose"} was removed: the session follows the organisation's from the agent's next start.`;
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

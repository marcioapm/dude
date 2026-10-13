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
  Capabilities, ChatAside, ChatMessage, ChatNotice, ChatTranscript, CostDisplay, LinkedProjects, ModelPicker, ProposalCard, PublishedFiles,
  ScreenHeader, Segmented, SessionFacts, SessionPeople, SessionRail, SessionRailBlock, SessionTitle, SharedMark, type ModelChoice, type ProposalCardItem, type ProposalRelation,
} from "@dude/design-system/components";
import { Badge, Button, Callout, Spinner } from "@dude/design-system/primitives";
import { EventTypes, HARNESS_LABEL, UNTITLED_SESSION, harnessSchema, type Harness, type PersistedEvent, type Proposal, type ProposalItem, type RunStatus, type SessionDetail, type SessionMemberView, type TaskStatus } from "@dude/domain";
import { ApiError, type ApiClient, type Artifact } from "../api/client.ts";
import { modelChangeWords, organizationOf, pickerTier, useTiers } from "../sessionModel.ts";
import { ArtifactViewer, filesOf, save } from "./FilesSection.tsx";
import { apply, emptyProjection, readWhen, snapshot, steerWait, type QuestionTurn, type Turn } from "../api/conversation.ts";
import { StartHeaderChip, StartRow, retryRefusal } from "../startCue.tsx";
import type { QuestionSubmission } from "@dude/design-system/components";
import { dudeName } from "../DudeMark.tsx";
import { DraftedComposer } from "./DraftedComposer.tsx";
import { useEventStream, useReloadOnEvents } from "../hooks/useEventStream.ts";
import { useVisibleInterval } from "../hooks/useVisibleInterval.ts";
import { errorText } from "../hooks/useSave.tsx";
import { usePeople, type People } from "../people.tsx";
import { byInstant } from "../instant.ts";
import { NotFound } from "./NotFound.tsx";
import { EventLog, asides, conversationOption, eventsOption, interleaved, renderTurn, type SteerActions } from "./RunScreen.tsx";
import { SessionMachineBlock } from "./actions.tsx";
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
  EventTypes.MachineRequested, EventTypes.MachineRaised, EventTypes.MachineRefused, EventTypes.MachineReset,
  EventTypes.BrainstormModelApplied,
  EventTypes.RunCreated, EventTypes.RunStarted, EventTypes.RunCompleted,
  EventTypes.RunFailed, EventTypes.RunAborted, EventTypes.RunPaused, EventTypes.RunResumed, EventTypes.QuestionAsked,
  EventTypes.QuestionAnswered, EventTypes.QuestionClosed, "run.parked", "run.unparked",
  // lux dropped a linked repository: it is no longer linked.
  EventTypes.RepositoryCloneFailed,
]);

/** The detail with one member's open state as a session.open says it: the rest as it was. */
export function withOpen(detail: SessionDetail, personId: string, open: boolean): SessionDetail {
  const people = detail.session.people;
  if (!people.some((m) => m.person.id === personId && m.open !== open)) return detail;
  return { ...detail, session: { ...detail.session, people: people.map((m) => (m.person.id === personId ? { ...m, open } : m)) } };
}

export function SessionScreen({ client, sessionId, projects, onBack, onChanged, onArchived }: {
  client: ApiClient;
  sessionId: string;
  projects: readonly NavProject[];
  onBack: () => void;
  /** Its people or title changed for you (handed over, left): the sidebar's list re-reads. */
  onChanged: () => void;
  /** You archived it (it leaves your list and sidebar) or unarchived it (it comes back). */
  onArchived: (id: string, archived: boolean) => void;
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
  // An existing task's status by key, as the tree has it: what a proposed relation to it shows.
  const statusOf = useMemo(() => {
    const m = new Map<string, TaskStatus>();
    for (const p of projects) for (const t of [...(p.epics ?? []).flatMap((e) => e.tasks), ...(p.tasks ?? [])]) if (t.key) m.set(t.key, t.status);
    return m;
  }, [projects]);

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

  // Try again on an agent that failed to start or to resume: a fresh one takes what it never read.
  const [retrying, setRetrying] = useState(false);
  // The failed turn whose Try again was refused because a newer agent already took its messages: not offered again.
  const [retryTaken, setRetryTaken] = useState<string | null>(null);
  const retryStart = useCallback(async (turnId: string) => {
    setProblem(null);
    setRetrying(true);
    try {
      await client.retrySessionAgent(sessionId);
      void load();
    } catch (err) {
      const refused = retryRefusal(err, errorText);
      if (refused.taken) {
        setRetryTaken(turnId);
        void load();
      } else setProblem(refused.text);
    } finally {
      setRetrying(false);
    }
  }, [client, sessionId, load]);

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

  // Yours alone: nobody else's list, and nothing of the session, changes.
  const [archiving, setArchiving] = useState(false);
  const archive = useCallback(async (archived: boolean) => {
    setProblem(null);
    setArchiving(true);
    try {
      await client.archiveSession(sessionId, archived);
      setDetail((d) => (d ? { ...d, you: { ...d.you, archived } } : d));
      onArchived(sessionId, archived);
    } catch (err) {
      setProblem(`Could not ${archived ? "archive" : "unarchive"} it: ${errorText(err)}`);
    } finally {
      setArchiving(false);
    }
  }, [client, sessionId, onArchived]);

  const linkedKeys = useMemo(() => new Map((detail?.session.projects ?? []).map((p) => [p.key.toUpperCase(), p])), [detail]);
  // The owner's picker lists the organisation's tiers, read when its menu opens (again on each
  // opening after a failed read); until then (and for anyone else) the chip reads the detail's words alone.
  const [menuOpenings, setMenuOpenings] = useState(0);
  const tierOptions = useTiers(client, menuOpenings);
  // What the owner picked, shown at once while its posts are out: a second pick before the first
  // is answered builds on it, and the posts go one after another so the last pick is the one kept.
  // Once the last one settles the detail drives the chip again. A failure cancels the picks queued
  // behind it, since each carries the failed one's half; a result for another session is dropped.
  const [picked, setPicked] = useState<ModelChoice | null>(null);
  const posting = useRef<Promise<unknown>>(Promise.resolve());
  const pending = useRef(0);
  const failures = useRef(0);
  const shownSession = useRef(sessionId);
  useEffect(() => {
    shownSession.current = sessionId;
    pending.current = 0;
    failures.current += 1;
    setPicked(null);
  }, [sessionId]);
  const chooseModel = useCallback((choice: ModelChoice) => {
    setProblem(null);
    setPicked(choice);
    pending.current += 1;
    const failuresBefore = failures.current;
    const mine = () => shownSession.current === sessionId;
    posting.current = posting.current.then(async () => {
      try {
        if (!mine() || failures.current !== failuresBefore) return;
        const { model } = await client.setSessionModel(sessionId, choice);
        if (mine()) setDetail((d) => (d ? { ...d, model } : d));
      } catch (err) {
        if (!mine()) return;
        failures.current += 1;
        setProblem(`Could not change the model: ${errorText(err)}`);
      } finally {
        if (mine()) {
          pending.current -= 1;
          if (pending.current === 0) setPicked(null);
        }
      }
    });
  }, [client, sessionId]);
  const lines = useMemo(() => {
    if (!detail) return [];
    const out: Array<{ id: string; at: string; node: ReactNode }> = [];
    for (const proposal of detail.proposals ?? []) {
      out.push({ id: proposal.id, at: proposal.createdAt, node: (
        <ProposalBlock key={proposal.id} client={client} sessionId={sessionId} proposal={proposal} linked={linkedKeys} statusOf={statusOf}
          readOnly={detail.you.role === "read"} filingAs={firstName(people.names.get(detail.you.id) ?? "you")} you={detail.you.id}
          onFiled={() => void load()} />
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
    return out.sort(byInstant((l) => l.at));
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
    wait: (turn) => steerWait(turn, status!, conversation.activeTool?.name ?? null, conversation.lands, conversation.start),
    resend: () => undefined,
  } : undefined;
  const render = (turn: Turn) => turn.kind === "ended" && turn.start
    ? <StartRow key={turn.id} start={turn.start} role="brainstorm" taken={turn.taken || retryTaken === turn.id}
        onRetry={reader || turn.retried || retrying ? undefined : () => void retryStart(turn.id)} />
    : renderTurn(turn, "brainstorm", conversation.contextWindow, !live, people, dude, undefined, undefined, steer,
      undefined, undefined, live && !reader ? { answer } : undefined);
  const starting = conversation.start;
  // The header: where the start is, or where it failed until a next agent is created.
  const headerStart = starting ?? conversation.startFailed;
  const startHint = readWhen(starting);
  // A session's one raise (role brainstorm).
  const raised = detail.agentMachines?.[0] ?? null;
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
          {headerStart ? <StartHeaderChip start={headerStart} /> : null}
          {you.archived ? <Badge size="sm" icon="archive" data-testid="session-archived">Archived</Badge> : null}
        </>}
        actions={<>
          <Button size="sm" variant="quiet" disabled={archiving} onClick={() => void archive(!you.archived)}
            data-testid={you.archived ? "session-unarchive" : "session-archive"}>
            {you.archived ? "Unarchive" : "Archive"}
          </Button>
          {isOwner ? <Button size="sm" variant="secondary" onClick={() => setDialog("share")} data-testid="share-open">Share</Button> : null}
        </>}
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
                <DraftedComposer
                  place={`session:${sessionId}`}
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
                  toAside={startHint ? <span className="muted" data-testid="composer-start-hint">· {startHint}</span> : undefined}
                  data-testid="session-composer"
                />
              }
            >
              {interleaved(grouped, lines).map((item) => "node" in item
                ? <Fragment key={item.id}>{item.node}</Fragment>
                : Array.isArray(item.group)
                  ? <ChatAside key={item.group[0]!.id}>{item.group.map(render)}</ChatAside>
                  : render(item.group))}
              {starting ? <StartRow start={starting} role="brainstorm" /> : null}
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
                    tiers={Array.isArray(tierOptions) ? tierOptions : (detail.model.tier ? [pickerTier(detail.model.tier)] : [])}
                    organization={organizationOf(detail.model)}
                    value={picked ?? { tier: detail.model.tier?.id ?? null, harness: detail.model.harness }}
                    onChange={isOwner ? chooseModel : undefined}
                    onOpenChange={(open) => open && setMenuOpenings((n) => n + 1)}
                    menuNote={tierOptions === "failed" ? "Could not load the tiers" : tierOptions === null && menuOpenings > 0 ? "Loading the tiers…" : undefined} />
                  <span className="muted sessionModelNote">Applies from your next message: a fresh agent takes over with the conversation so far.</span>
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
              {/* Every member may Reset, readers included (the API's rule, as for starting a task). */}
              {raised ? <SessionMachineBlock client={client} id={sessionId} machine={raised}
                by={firstName(people.names.get(you.id) ?? "you")} onChanged={() => void load()} /> : null}
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
 * The header's model, "· <tier> · <harness>": what the agent runs on now
 * while one is live (a change waits for its next start), as its Run
 * records it, else what its next start would use, "(organisation
 * default)" when the session follows the organisation.
 */
export function headerModel(detail: SessionDetail): string {
  const run = detail.session.run;
  const live = run && !["completed", "failed", "aborted"].includes(run.status);
  if (live && (run.modelTier || run.model)) {
    // The scripted agent is recorded as "scripted": named so, as the Run's own chip leaves it out.
    const harness = run.harness ? HARNESS_LABEL[run.harness as Harness] ?? run.harness : null;
    return ` · ${[run.modelTier ?? run.model, harness].filter(Boolean).join(" · ")}`;
  }
  const m = detail.model;
  if (!m) return run?.model ? ` · ${run.modelTier ?? run.model}` : "";
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
      if (!tier && !harness.success) return `${who} set the model back to the organisation's default; it applies from the next message.`;
      return `${who} set the model to ${modelChangeWords(tier, harness.success ? harness.data : null)}; it applies from the next message.`;
    }
    case EventTypes.BrainstormModelApplied: {
      const harness = harnessSchema.safeParse(p.harness);
      const model = tierName(p.tier) ?? (typeof p.model === "string" ? p.model : null);
      const words = [model, harness.success ? HARNESS_LABEL[harness.data] : null].filter(Boolean).join(" · ");
      return `Started a fresh agent${words ? ` on ${words}` : ""}; it has the conversation so far.`;
    }
    case EventTypes.BrainstormModelFallback:
      return `The tier ${tierName(p.tier) ?? "this session chose"} was removed: the session follows the organisation's from the next message.`;
    default:
      return null;
  }
}

/** The name of a payload's `tier: { name }`, if it has one. */
function tierName(tier: unknown): string | null {
  const name = tier && typeof tier === "object" ? (tier as { name?: unknown }).name : null;
  return typeof name === "string" ? name : null;
}

/** One proposal's card: what the person looking can file, ticked, and filing it. */
function ProposalBlock({ client, sessionId, proposal, linked, statusOf, readOnly, filingAs, you, onFiled }: {
  client: ApiClient;
  sessionId: string;
  proposal: Proposal;
  linked: ReadonlyMap<string, { key: string; name: string }>;
  statusOf: ReadonlyMap<string, TaskStatus>;
  readOnly: boolean;
  filingAs: string;
  you: string;
  onFiled: () => void;
}) {
  const fileable = proposal.items.flatMap((_, i) => (proposal.status[i]?.canFile && !proposal.status[i]?.filed ? [i] : []));
  const [selected, setSelected] = useState<Set<number>>(() => new Set(fileable));
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const epics = new Set(proposal.items.filter((i) => i.kind === "epic").map((i) => i.title));
  const numbered = proposal.items.some((i) => i.blockedBy?.length || i.blocks?.length || i.related?.length);
  const items = proposal.items.map((item, i) => ({
    ...cardItem(item, proposal.status[i] ?? {}, linked, epics, you),
    ...(numbered ? { n: i + 1, relations: itemRelations(item, statusOf), tickedWith: tickedWith(proposal, selected, i) } : {}),
  }));
  const order = numbered ? proposalOrder(proposal) : undefined;
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
      <ProposalCard items={items} selected={selected} filingAs={filingAs} busy={busy} readOnly={readOnly} onFile={() => void file()} order={order}
        onToggle={(i) => setSelected((s) => {
          const n = new Set(s);
          if (n.has(i)) n.delete(i);
          else {
            // What it waits on is filed with it: ticked too.
            n.add(i);
            for (const b of proposal.status[i]?.filesWith ?? []) n.add(b);
          }
          return n;
        })} />
      {problem ? <Callout tone="danger" data-testid="proposal-problem">{problem}</Callout> : null}
    </div>
  );
}

function cardItem(item: ProposalItem, status: Proposal["status"][number], linked: ReadonlyMap<string, { key: string; name: string }>,
  epics: ReadonlySet<string | undefined>, you: string): ProposalCardItem {
  const project = item.project ? linked.get(item.project.toUpperCase()) : undefined;
  // Someone else may file it: its task's owner (never you), or a member when you only read.
  const theirs = (status.blockedBy === "owner" && status.owner?.id !== you) || status.blockedBy === "reader";
  const blockedFor: ProposalCardItem["blockedFor"] = theirs ? "you" : "everyone";
  const common = {
    canFile: Boolean(status.canFile),
    ...(status.canFile ? {} : { blockedFor }),
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
    case "relation":
      return { kind: "relation", title: `${item.from ?? ""} ${item.relationKind === "related" ? "related to" : "blocks"} ${item.to ?? ""}`,
        taskKey: item.to, detail: item.why ? `“${item.why}”` : undefined, ...common };
  }
}

/** A task item's relations as its card lists them: another item by number, an existing task by key with its status. */
function itemRelations(item: ProposalItem, statusOf: ReadonlyMap<string, TaskStatus>): ProposalRelation[] {
  const out: ProposalRelation[] = [];
  const add = (kind: ProposalRelation["kind"], refs: ProposalItem["blockedBy"]) => {
    for (const r of refs ?? []) {
      if (r.item) out.push({ kind, item: r.item });
      else if (r.key) out.push({ kind, task: { key: r.key, status: statusOf.get(r.key) ?? "received" } });
    }
  };
  add("blocked-by", item.blockedBy);
  add("blocks", item.blocks);
  add("related", item.related);
  return out;
}

/** The ticked items (by number) that wait on item `n` and so tick it with them; only while it is ticked. */
function tickedWith(proposal: Proposal, selected: ReadonlySet<number>, n: number): number[] {
  if (!selected.has(n)) return [];
  return [...selected].filter((i) => i !== n && proposal.status[i]?.filesWith?.includes(n)).sort((a, b) => a - b).map((i) => i + 1);
}

/** The items in steps (numbers, 1-based): each one step after the last item of the card it waits on. */
export function proposalOrder(proposal: Pick<Proposal, "items">): number[][] {
  const items = proposal.items;
  const waitsOn = items.map(() => new Set<number>());
  items.forEach((it, i) => {
    for (const r of it.blockedBy ?? []) if (r.item && r.item - 1 !== i && items[r.item - 1]) waitsOn[i]!.add(r.item - 1);
    for (const r of it.blocks ?? []) if (r.item && r.item - 1 !== i && items[r.item - 1]) waitsOn[r.item - 1]!.add(i);
  });
  const step = new Map<number, number>();
  const at = (i: number, seen: Set<number>): number => {
    const known = step.get(i);
    if (known !== undefined) return known;
    // A loop is refused when proposed; were one here, it ends the walk rather than recursing forever.
    if (seen.has(i)) return 0;
    seen.add(i);
    const s = Math.max(-1, ...[...waitsOn[i]!].map((b) => at(b, seen))) + 1;
    step.set(i, s);
    return s;
  };
  const steps: number[][] = [];
  items.forEach((_, i) => {
    const s = at(i, new Set());
    (steps[s] ??= []).push(i + 1);
  });
  return steps.filter(Boolean);
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

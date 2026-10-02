/**
 * A session — the operator's day-to-day view, on its task's page beside
 * the task's other sessions (or alone, for a Run the tree does not hold).
 *
 * A header says whose agent it is and what it is doing, with Pause and
 * Abort. Under it, Conversation (what someone supervising agents actually
 * reads, with a rail beside it: the session's facts, the tools it used and
 * the files it changed so far), Changes (its checkout as it changes), and
 * the event timeline — a debugging tool, reached when something looks
 * wrong, not watched continuously.
 */

import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  AgentPlan,
  ChatComposer,
  ChatEvent,
  ChatMessage,
  ChatNotice,
  ChatProgress,
  ChatTranscript,
  EventRow,
  EventStream,
  ChangedFiles,
  ChatAside,
  QuestionCard,
  Segmented,
  TerminalLink,
  SessionFacts,
  SessionHeader,
  SessionRail,
  SessionRailBlock,
  ThinkingBlock,
  ToolUsage,
  ToolCallCard,
  summarizeToolArgs,
  MachineChip,
  MachineTip,
  AttachDropZone,
  ImageViewer,
} from "@dude/design-system/components";
import { Button, Callout, Dialog, LinkButton, Spinner, Textarea } from "@dude/design-system/primitives";
import { DEFAULT_RUN_ROLE, EventTypes, MIB, SETTINGS_ROLE_LABEL, TERMINAL_RUN_STATUSES, gib, machineSpec, runLabel } from "@dude/domain";
import type { AgentRole, PersistedEvent } from "@dude/domain";
import type { ApiClient, Person, RunDetail, RunDiffSummary } from "../api/client.ts";
import { ApiError, modelCostShown } from "../api/client.ts";
import {
  PAUSE_WORDS, actorName, apply, emptyProjection, humanActor, landsHint, snapshot, steerWait, toolLabel, type HumanTurn, type SteerWait, type Turn,
} from "../api/conversation.ts";
import type { ComposerSubmission } from "@dude/design-system/components";
import { useEventStream } from "../hooks/useEventStream.ts";
import { conflictNotice, type Notice } from "../conflict.ts";
import { firstName, Icon } from "@dude/design-system";
import { usePeople, type People } from "../people.tsx";
import { NotFound } from "./NotFound.tsx";
import { DudeMark, dudeName } from "../DudeMark.tsx";
import { ChangesPanel } from "./ChangesPanel.tsx";
import { TurnImages, limitsHint, useAttachmentLimits, useImageTray, useSentImages, type SentImages } from "../hooks/useImages.tsx";
import type { AttachmentInfo } from "@dude/domain";

export interface RunScreenProps {
  client: ApiClient;
  runId: string;
  /** Open the task this Run works on, on its Servers tab when asked. Not given on the task's own page. */
  onOpenTask?: ((taskId: string, tab?: "servers") => void) | undefined;
  /** On the task's own page: show its Servers tab, where a Run's servers live. */
  onOpenServers?: (() => void) | undefined;
  /** Leave for somewhere that exists, when this Run does not. */
  onBack: () => void;
  /**
   * The task's owner and key, when the caller has them (its task's page):
   * not read again, and only what is given is shown — the task page gives
   * the owner alone, its key being on the page already.
   */
  task?: { owner: Person | null; key?: string | undefined } | undefined;
}

/** What a session shows: its conversation, its checkout's changes, or its event ledger. */
type SessionView = "chat" | "changes" | "events";

/**
 * Events that can change a Run's status, and so are worth a re-read.
 *
 * Not the `run.` prefix: lease acquisition and steering share it and fire
 * often, which would cost a round trip each without telling us anything new.
 */
const STATUS_EVENTS: ReadonlySet<string> = new Set([
  EventTypes.RunCreated,
  EventTypes.RunStarted,
  EventTypes.RunCompleted,
  EventTypes.RunFailed,
  EventTypes.RunAborted,
  EventTypes.RunPaused,
  EventTypes.RunResumed,
]);

export const RunScreen = memo(function RunScreen({ client, runId, onOpenTask, onOpenServers, onBack, task: given }: RunScreenProps) {
  const [view, setView] = useState<SessionView>("chat");
  // The bar's slot where Changes draws the diff's own controls.
  const [toolbar, setToolbar] = useState<HTMLDivElement | null>(null);
  // The file Changes shows alone, picked there or in the rail. Leaving
  // Changes forgets it, so coming back finds all of them.
  const [selected, setSelected] = useState<string | null>(null);
  const showView = (next: SessionView) => {
    if (next !== "changes") setSelected(null);
    setView(next);
  };
  const [run, setRun] = useState<RunDetail | null>(null);
  const [missing, setMissing] = useState(false);
  // Who drives the task: only its owner answers its agents, so anyone else
  // sees the asks read-only, with whom they wait on. And the task's key
  // (TEXT-14), which people know it by, for the header.
  const [read, setTask] = useState<{ owner: Person | null; key?: string | undefined } | null>(null);
  const task = given ?? read;
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  // Someone else acted first: said calmly, and gone once the Run catches up.
  // It remembers the status it was about: once the Run moves on, it has said its piece.
  const [notice, setNotice] = useState<(Notice & { about: RunDetail["status"] | undefined }) | null>(null);
  const [confirmAbort, setConfirmAbort] = useState(false);
  const [abortReason, setAbortReason] = useState("");
  const people = usePeople();

  const { events, reconnects } = useEventStream({ client, runId });

  // Re-read the Run whenever the ledger says its status changed, rather than
  // polling: the stream already tells us when something happened.
  const statusEventCount = useMemo(
    () => events.reduce((n, e) => (STATUS_EVENTS.has(e.eventType) ? n + 1 : n), 0),
    [events],
  );

  useEffect(() => {
    let cancelled = false;
    client
      .getRun(runId)
      .then((fresh) => {
        if (!cancelled) setRun(fresh);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        if (err instanceof ApiError && err.status === 404) setMissing(true);
        else setProblem(err instanceof Error ? err.message : String(err));
      });
    return () => {
      cancelled = true;
    };
  }, [client, runId, statusEventCount, reconnects]);

  // Who drives the task is read once per task, not on every status change,
  // and apart from the Run: failing to learn it only leaves the asks
  // answerable here, which the orchestrator still checks.
  const taskId = given ? undefined : run?.taskId;
  // The lux terminal's link, for the rail while the Run is running. The Run
  // itself does not carry it (lux's console URL is the orchestrator's), so
  // it is read from the Run's servers. Until it is known, a servers.changed
  // (lux took the Run) or a stream that came back asks again.
  const askAgain = useMemo(() => (events.findLast((e) => e.eventType === EventTypes.ServersChanged)?.cursor ?? 0) + reconnects * 1e9, [events, reconnects]);
  const { url: terminalUrl, memoryLimit } = useTerminalUrl(client, runId, run?.status === "running", askAgain);

  useEffect(() => {
    if (!taskId) return;
    let cancelled = false;
    client.getTask(taskId)
      .then((t) => {
        if (!cancelled) setTask({ owner: t.owner, key: t.key });
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [client, taskId]);

  /*
   * The projection is folded forward, not rebuilt. `apply` skips events at or
   * below the cursor it already consumed, so each frame costs one event
   * rather than the whole retained history — and settled turns keep their
   * object identity, which is what lets React leave them alone.
   */
  const projection = useRef(emptyProjection());
  const projectedRun = useRef(runId);
  const conversation = useMemo(() => {
    // The stream clears its buffer for a new Run; the fold must forget too.
    if (projectedRun.current !== runId) {
      projection.current = emptyProjection();
      projectedRun.current = runId;
    }
    return snapshot(apply(projection.current, events), run?.status);
  }, [events, runId, run?.status]);
  // A run of tool calls and thoughts is one group, worked out once per
  // snapshot: each fold gives a new conversation, though its turns array is
  // the same one, appended to and changed in place.
  const grouped = useMemo(() => asides(conversation.turns), [conversation]);
  const isLive = run ? !TERMINAL_RUN_STATUSES.includes(run.status) : false;
  // A branch preview is a run with no agent: nothing to steer, pause or abort
  // from here (the API says 409 not_an_agent); the servers are all of it.
  // The Run says so itself, so the agent's controls never flash (or a
  // parked preview's pause words go missing) before the servers are read.
  const isPreviewRun = run?.kind === "preview";
  // The latest diff's summary: its file count for the Changes tab, its
  // checksum for the panel to know when to fetch.
  const diffSummary = useMemo(
    () => events.findLast((e) => e.eventType === EventTypes.RunDiffUpdated)?.payload as RunDiffSummary | undefined,
    [events],
  );
  // Which tools it called, and how often: for the rail. The projection counts them.
  const tools = [...conversation.toolCounts].map(([name, count]) => ({ name: name.charAt(0).toUpperCase() + name.slice(1), count }));

  /**
   * Run an intervention. A conflict (409) means the Run moved on while
   * you were deciding — usually because someone else acted: that is a
   * calm notice naming them, not an error, and it clears when the Run's
   * state catches up. Anything else is a problem, said plainly.
   */
  const intervene = useCallback(
    async (action: () => Promise<unknown>, label: string) => {
      setBusy(true);
      setProblem(null);
      setNotice(null);
      try {
        await action();
        return true;
      } catch (err) {
        if (err instanceof ApiError && err.status === 409) {
          // The Run moved on: read where it is now, and who moved it —
          // someone the page may not have met yet. The act is in what the
          // page had not seen, or had only just seen before a re-read.
          const [unseen, fresh, known] = await Promise.all([
            client.events({ runId, after: events.at(-1)?.cursor ?? 0 }).then((r) => r.events, () => []),
            client.getRun(runId).then((r) => r, () => null),
            people.refresh(),
          ]);
          if (fresh) setRun(fresh);
          const status = fresh?.status ?? run?.status;
          setNotice({ ...conflictNotice(label, err.message, [...events.slice(-50), ...unseen], known, status), about: status });
        } else {
          setProblem(err instanceof ApiError ? `Could not ${label}: ${err.message}` : `Could not ${label}.`);
        }
        return false;
      } finally {
        setBusy(false);
      }
    },
    [client, runId, events, people, run?.status],
  );

  // A notice says the Run moved on; once it moves again, it has said its piece.
  useEffect(() => {
    if (notice && run?.status !== notice.about) setNotice(null);
  }, [notice, run?.status]);

  // Awaited, so the composer stays busy until the API has answered: no
  // second submit of the same words while the first is on its way.
  const limits = useAttachmentLimits(client);
  const tray = useImageTray(client, run?.taskId, limits);
  const images = useSentImages(client);
  // The images of one message, open in the viewer.
  const [viewing, setViewing] = useState<{ turn: ViewedTurn; index: number } | null>(null);
  const send = useCallback(
    async (submission: ComposerSubmission) => {
      const ok = await (submission.mode === "answer"
        ? intervene(() => client.answer(submission.questionId, submission.text, submission.attachmentIds), "answer the agent")
        : intervene(() => client.steer(runId, submission.text, { interrupt: submission.mode === "steer" && submission.interrupt,
          attachmentIds: submission.attachmentIds }), "steer this run"));
      // Sent: the images are the message's now, not the tray's.
      if (ok) tray.clear(submission.attachmentIds);
      else throw new Error("not sent");
    },
    [client, runId, intervene, tray],
  );

  // Interrupt now (a queued steer) and Retry (a failed one) send the same
  // words again, superseding the directive: the transcript keeps one turn.
  const resteer = useCallback(
    (turn: HumanTurn, interrupt: boolean) =>
      void intervene(() => client.steer(runId, turn.text, { interrupt, ...(turn.directiveId ? { supersedes: turn.directiveId } : {}) }),
        interrupt ? "interrupt the agent" : "steer this run"),
    [client, runId, intervene],
  );

  const decide = useCallback(
    (requestId: string, approve: boolean) =>
      void intervene(() => client.decideRepositoryRequest(requestId, approve),
        approve ? "approve the repository" : "decline the repository"),
    [client, intervene],
  );

  if (missing) return <NotFound what="run" onBack={onBack} />;
  if (!run) {
    return <div className="runScreen">{problem ?? <Spinner label="Loading the run…" />}</div>;
  }

  // Someone else's to answer: their name. A task nobody owns is anyone's.
  // Until you are known, nobody is waited on: the orchestrator still checks.
  const waitingOn = task?.owner && people.you && task.owner.id !== people.you ? task.owner.name : undefined;
  const owner = task?.owner ? (people.byId.get(task.owner.id) ?? task.owner) : undefined;
  const taskKey = task?.key;
  const youName = people.you ? people.names.get(people.you) : undefined;

  // The agent this Run is. A phase Run carries its role; one created
  // directly through the API runs as an orchestrator.
  const role: AgentRole = run.role ?? DEFAULT_RUN_ROLE;

  const session = {
    id: run.id,
    role,
    status: run.status,
    ...(owner ? { owner } : {}),
    subtitle: (
      <>
        {owner ? <span>for {firstName(owner.name)}</span> : <span>{runLabel(run)}</span>}
        {run.model ? <code>{run.model}</code> : null}
        {run.machine ? <RunMachineChip machine={run.machine} memoryLimit={memoryLimit} role={role} phase={run.phase} /> : null}
        {taskKey ? <code title={`task ${run.taskId} · run ${run.id}`}>{taskKey}</code> : null}
      </>
    ),
    // The id is already shown beside the title; repeating it as the title
    // leaves the header saying nothing about the work. The attempt only
    // once there is more than one to tell apart.
    title: [runLabel(run), run.attempt > 1 ? `attempt ${run.attempt}` : null].filter(Boolean).join(" · "),
    taskId: run.taskId,
    ...(taskKey ? { taskKey } : {}),
    startedAt: run.startedAt ?? run.createdAt,
    endedAt: run.endedAt,
    ...(run.model ? { model: run.model } : {}),
    costUsd: modelCostShown(conversation.costUsd, conversation.costSource.from),
    tokensFrom: conversation.costSource.from,
    settled: conversation.costSource.settled,
    // The Run's own totals are exact; the projection's are what has streamed
    // in so far, for a Run still working.
    tokens: Math.max(run.tokens.input + run.tokens.output, conversation.tokens),
  };

  // Images can be dropped while the composer takes a message.
  const composerOpen = isLive && !isPreviewRun && !((run.status === "paused" && !conversation.openQuestion) ||
    (conversation.openQuestion !== null && waitingOn !== undefined));
  const changed = diffSummary?.files ?? [];
  // The name dude signs this task's messages with.
  const dude = dudeName(run.taskId);
  // What a queued steer waits for, and the ways to act on one.
  const activeTool = conversation.activeTool?.name ?? null;
  const steer: SteerActions | undefined = isLive ? {
    wait: (turn) => steerWait(turn, run.status, activeTool, conversation.lands),
    resend: resteer,
  } : undefined;
  const shown = { images, open: (turn: ViewedTurn, index: number) => setViewing({ turn, index }) };
  const render = (turn: Turn) => renderTurn(turn, role, conversation.contextWindow, !isLive, people, dude, decide, waitingOn, steer, shown);
  // A checkout to show: a Run with one, or any Run that has reported a diff
  // (the rail's files open Changes, so Changes must be there to open).
  const hasChanges = Object.keys(run.baseRefs).length > 0 || run.phase !== null || changed.length > 0;
  // Changes gone from the switch (its diff emptied, with no checkout to fall back on): back to the conversation.
  if (view === "changes" && !hasChanges) showView("chat");
  const liveDiff = isLive && run.status !== "paused";

  // The terminal, while the Run is running: in the rail, and in the header
  // only where the rail is not (a narrow session, or a view other than the
  // conversation), so it is always one click away and never shown twice.
  const terminal = run.status === "running" ? terminalUrl : null;
  // A branch preview has no agent to pause or abort: only its terminal.
  const actions = isLive ? (
    <>
      {terminal ? (
        <LinkButton size="sm" iconOnly leadingIcon="terminal" label="Open terminal in lux" href={terminal} className="runTerminalFallback" data-testid="terminal-icon" />
      ) : null}
      {isPreviewRun ? null : run.status === "paused" ? (
        <Button size="sm" variant="secondary" disabled={busy} onClick={() => void intervene(() => client.resume(runId), "resume this run")}>
          Resume
        </Button>
      ) : (
        <Button size="sm" variant="secondary" disabled={busy} onClick={() => void intervene(() => client.pause(runId), "pause this run")}>
          Pause
        </Button>
      )}
      {isPreviewRun ? null : (
        <Button size="sm" variant="danger" disabled={busy} onClick={() => setConfirmAbort(true)} data-testid="abort">
          Abort…
        </Button>
      )}
    </>
  ) : undefined;

  // The session's views: one switch, in the same place on each, with the
  // diff's own controls beside it on Changes rather than on a row of their own.
  const switcher = (
    <Segmented<SessionView> label="Show" value={view} onChange={showView} data-testid="session-view"
      options={[
        { value: "chat", label: <><Icon name="message" size={13} />Conversation</> },
        // The agent's checkout, as it changes: only for a Run with one.
        ...(hasChanges ? [{ value: "changes" as const, label: (
          <>
            <Icon name="git-branch" size={13} />Changes
            {changed.length > 0 ? <span className="ds-tnum runCount">{changed.length}</span> : null}
            {liveDiff && changed.length > 0 ? <span className="ds-live-dot" aria-label="changing now" /> : null}
          </>
        ) }] : []),
        // Debugging, not the daily view — hence last.
        { value: "events", label: <><Icon name="list" size={13} />Events<span className="ds-tnum runCount">{events.length}</span></> },
      ]} />
  );

  return (
    <div className="runScreen" data-view={view} data-testid="run-screen">
      {/* The whole session takes a dropped image, header and rail included, on the conversation. */}
      <AttachDropZone className="runDrop" onFiles={tray.add} disabled={view !== "chat" || !composerOpen} disabledReason={tray.disabledReason}
        detail={<>They go with your next {conversation.openQuestion ? "answer" : "steer"} to <b>{runLabel(run)}</b>.{" "}
          {conversation.openQuestion ? "It reads them with your answer." : dropWhen(landsHint(run.status, activeTool, conversation.lands))}</>}>
      <SessionHeader session={session} actions={actions} />
      {/* One bar, kept mounted whichever view shows, so the switch keeps its
          focus; Changes draws its own controls into the slot after it. */}
      <div className="runBar">
        {switcher}
        <div className="runBarTools" ref={setToolbar} />
      </div>
      <div className="runView">
        {view === "changes" ? (
          <ChangesPanel client={client} runId={runId} role={role} events={events} checksum={diffSummary?.checksum ?? ""} live={liveDiff}
            selected={selected} onSelectedChange={setSelected} toolbarIn={toolbar} />
        ) : view === "events" ? (
          <div className="runEvents" data-testid="event-log">
            <EventStream>
              {events.map((event) => (
                <EventRow
                  key={event.eventId}
                  occurredAt={event.occurredAt}
                  eventType={event.eventType}
                   actor={{ type: event.actor.type === "person" ? "human" : event.actor.type, id: event.actor.id, ...namedActor(event, people) }}
                  summary={summarize(event)}
                  // An element, not a string: EventRow only renders the detail
                  // when the row is open, so the JSON is built for the handful
                  // of rows an operator actually expands.
                  detail={<PayloadDetail payload={event.payload} />}
                />
              ))}
            </EventStream>
          </div>
        ) : (
          <div className="runChat">
            <ChatTranscript
              fill
              live={isLive}
              revision={events.length}
              turns={conversation.turns.length}
              pinned={
                conversation.plan.length > 0 ? (
                  <AgentPlan items={conversation.plan} defaultCollapsed data-testid="plan" />
                ) : null
              }
              footer={!isLive ? <RunEnded run={run} onOpenTask={onOpenTask ? () => onOpenTask(run.taskId) : undefined} /> : isPreviewRun ? (
                // A preview run has no agent to steer: its servers are the whole of it,
                // and they are on the task's Servers tab. A task this page could not
                // read is no place to send anyone: the way there is said in words.
                <PreviewRunNote openServers={onOpenServers ?? (onOpenTask && read ? () => onOpenTask(run.taskId, "servers") : undefined)} />
              ) : (
                <ChatComposer
                  // The agent waiting on a question takes an answer; otherwise
                  // anything said steers it.
                  mode={conversation.openQuestion ? "answer" : "steer"}
                  question={
                    conversation.openQuestion
                      ? {
                          id: conversation.openQuestion.questionId,
                          text: conversation.openQuestion.text,
                          askedBy: runLabel(run),
                          askedAt: conversation.openQuestion.at,
                          // Choices only for whoever may choose.
                          options: waitingOn ? [] : conversation.openQuestion.options,
                        }
                      : undefined
                  }
                  // A paused Run takes an answer (a parked one is resumed by it),
                  // not a steer; a question is its owner's to answer.
                  disabled={(run.status === "paused" && !conversation.openQuestion) ||
                    (conversation.openQuestion !== null && waitingOn !== undefined)}
                  disabledReason={
                    waitingOn && (conversation.openQuestion || run.dudePause === "person")
                      ? `Waiting for ${waitingOn} to ${!conversation.openQuestion && conversation.openRequest ? "decide" : "answer"}.`
                      : run.dudePause ? PAUSE_WORDS[run.dudePause].composer
                      : "This run is paused. Resume it to steer."
                  }
                  onSubmit={send}
                  sentAs={youName ? firstName(youName) : undefined}
                  canInterrupt
                  landsHint={landsHint(run.status, activeTool, conversation.lands)}
                  attachments={tray.attachments}
                  onAttachFiles={tray.add}
                  onRemoveAttachment={tray.remove}
                  attachAccept="image/png,image/jpeg,image/webp,image/gif"
                  attachHint={limitsHint(tray.limits)}
                  attachDisabledReason={tray.disabledReason}
                />
              )}
              emptyMessage="Waiting for the agent to start."
            >
              {grouped.map((group) => Array.isArray(group)
                ? <ChatAside key={group[0]!.id}>{group.map(render)}</ChatAside>
                : render(group))}
              {conversation.activity ? (
                <ChatMessage
                  role={role}
                  activity={conversation.activity}
                  activityProps={
                    conversation.activeTool
                      ? { label: conversation.activeTool.name, since: conversation.activeTool.since }
                      : undefined
                  }
                />
              ) : null}
            </ChatTranscript>
            {viewing ? (
              <ImageViewer
                images={viewing.turn.attachments.map(images.sent)}
                index={viewing.index}
                onIndexChange={(index) => setViewing({ ...viewing, index })}
                onClose={() => setViewing(null)}
                context={viewedContext(viewing.turn, people, runLabel(run), dude)}
                readAt={viewing.turn.kind === "human" && viewing.turn.read && viewing.turn.deliveredAt ? clock(viewing.turn.deliveredAt) : undefined}
                onWantOriginal={(i) => images.wantOriginal(viewing.turn.attachments[i]!.id)}
                onDownload={(i, variant) => void images.download(viewing.turn.attachments[i]!, variant)}
              />
            ) : null}
            {/* Cost, tokens and elapsed are the header's, on every view: the rail has the rest. */}
            <SessionRail className="runRail" aria-label="This session" data-testid="session-rail">
              <SessionRailBlock label="Session">
                <SessionFacts facts={[
                  ...(run.model ? [{ label: "Model", value: run.model, mono: true }] : []),
                  ...(run.harness ? [{ label: "Agent", value: run.harness }] : []),
                  { label: "Attempt", value: run.attempt },
                ]} />
                {terminal ? <div className="runRailTerminal"><TerminalLink href={terminal} /></div> : null}
              </SessionRailBlock>
              {tools.length > 0 ? (
                <SessionRailBlock label="Tools used">
                  <ToolUsage tools={tools} />
                </SessionRailBlock>
              ) : null}
              {changed.length > 0 ? (
                <SessionRailBlock label="Files changed" live={liveDiff}>
                  <ChangedFiles files={changed} onOpen={(path) => {
                    setSelected(path);
                    showView("changes");
                  }} />
                </SessionRailBlock>
              ) : null}
            </SessionRail>
          </div>
        )}
      </div>
      </AttachDropZone>

      {notice ? (
        <Callout tone="neutral" data-testid="conflict-notice">
          <span className="noticeLine">
            {notice.text}
            <Button size="sm" variant="quiet" onClick={() => setNotice(null)}>
              Dismiss
            </Button>
          </span>
        </Callout>
      ) : null}
      {problem ? <Callout tone="danger">{problem}</Callout> : null}
      {confirmAbort ? (
        <Dialog
          open
          onOpenChange={(o) => !o && setConfirmAbort(false)}
          tone="danger"
          size="sm"
          title={`Abort ${runLabel(run).toLowerCase()}?`}
          description={
            owner && owner.id !== people.you
              ? `It stops for good and cannot be resumed. It works for ${firstName(owner.name)}, who will see you stopped it.`
              : "It stops for good and cannot be resumed. Its work so far stays on its branch."
          }
          footer={
            <>
              {run.status !== "paused" ? (
                <Button variant="secondary" data-testid="abort-pause-instead" onClick={() => {
                  setConfirmAbort(false);
                  void intervene(() => client.pause(runId), "pause this run");
                }}>
                  Pause instead
                </Button>
              ) : null}
              <Button variant="quiet" onClick={() => setConfirmAbort(false)}>
                Cancel
              </Button>
              <Button variant="danger" solid data-testid="abort-confirm" onClick={() => {
                setConfirmAbort(false);
                void intervene(() => client.abort(runId, abortReason.trim() || undefined), "abort this run");
              }}>
                Abort run
              </Button>
            </>
          }
        >
          <Textarea label="Why (optional)" hint="Shown in the conversation beside your name." rows={2} value={abortReason}
            onChange={(e) => setAbortReason(e.target.value)} />
        </Dialog>
      ) : null}
    </div>
  );
});

/**
 * The turns, with each run of tool calls and thoughts gathered into one
 * group: a `ChatAside` puts them on the message column, stacked close,
 * with a turn's air above and below the run — as the design system draws
 * what an agent does between its messages.
 */
function asides(turns: readonly Turn[]): Array<Turn | Turn[]> {
  const out: Array<Turn | Turn[]> = [];
  for (const turn of turns) {
    if (turn.kind !== "tool" && turn.kind !== "thought") {
      out.push(turn);
      continue;
    }
    const last = out[out.length - 1];
    if (Array.isArray(last)) last.push(turn);
    else out.push([turn]);
  }
  return out;
}

/** The actor's name for the event ledger, when it is a person the organisation knows. */
function namedActor(event: PersistedEvent, people: People): { name?: string } {
  const name = actorName(humanActor(event), people.names);
  return name ? { name } : {};
}

/** A live Run's queued steers: what each waits for, and sending one again (interrupting, or after a failure). */
interface SteerActions {
  wait: (turn: HumanTurn) => SteerWait;
  resend: (turn: HumanTurn, interrupt: boolean) => void;
}

/** The one line under a queued steer. */
function pendingReason(wait: SteerWait) {
  switch (wait.kind) {
    case "tool": return <>Lands after <b>{toolLabel(wait.tool)}</b> finishes.</>;
    case "next_step": return "Lands at the agent's next step.";
    case "next_turn": return "This agent reads messages only between turns — lands when this turn ends.";
    case "paused": return "Lands when the run resumes.";
    case "starting": return "Lands when the agent starts.";
  }
}

/** A turn whose images the viewer shows. */
type ViewedTurn = HumanTurn | { kind: "prompt"; attachments: AttachmentInfo[]; at: string };

/** How a turn's images are shown, and opened. */
interface ShownImages {
  images: Pick<SentImages, "sent" | "mounted" | "visible">;
  open: (turn: ViewedTurn, index: number) => void;
}

/** "15:52:40": a read time, in the viewer's line. */
function clock(at: string): string {
  return new Date(at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });
}

/** The lands hint as the drop overlay says it: "It reads them after the current tool." */
function dropWhen(hint: string | null): string {
  if (!hint) return "It reads them at its next step.";
  return `It reads them ${hint.replace(/^Lands /, "")}.`;
}

/** A turn's images, as ChatMessage's `attachments` prop; none when it has none. */
function turnImages(turn: ViewedTurn, shown: ShownImages | undefined) {
  if (turn.attachments.length === 0 || !shown) return {};
  return { attachments: <TurnImages attachments={turn.attachments} images={shown.images} onOpen={(i) => shown.open(turn, i)} /> };
}

/** "Márcio · steer to Implement · 15:52": who sent a message's images, to whom, when. */
function viewedContext(turn: ViewedTurn, people: People, agent: string, dude: string): string {
  const at = new Date(turn.at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
  if (turn.kind === "prompt") return `${dude} · task to ${agent} · ${at}`;
  const who = actorName(turn.by, people.names);
  return `${who ? firstName(who) : "Someone"} · ${turn.intent} to ${agent} · ${at}`;
}

function renderTurn(turn: Turn, role: AgentRole, contextWindow: number, ended: boolean, people: People, dude: string,
  decide?: (requestId: string, approve: boolean) => void, waitingOn?: string, steer?: SteerActions, shown?: ShownImages) {
  switch (turn.kind) {
    case "repositoryRequest": {
      // Asked of a person, like a question: approve brings it into the Run.
      const what = `${turn.access === "write" ? "Change" : "Read"} ${turn.repository}`;
      return (
        <QuestionCard
          key={turn.id}
          data-testid="repository-request"
          role={role}
          // The agent's reason, quoted: its words cannot pass for the card's.
          text={`**${what}?**\n\n${turn.reason.split("\n").map((line) => `> ${line}`).join("\n")}`}
          options={turn.decision === null && !ended ? ["Approve", "Decline"] : []}
          kind="request"
          askedAt={turn.at}
          answeredAt={turn.decidedAt}
          dismissed={ended && turn.decision === null}
          waitingOn={waitingOn}
          {...(decide && turn.decision === null && !ended
            ? { onChoose: (choice: string) => decide(turn.requestId, choice === "Approve") }
            : {})}
        />
      );
    }
    case "notice":
      return <ChatNotice key={turn.id} data-testid="chat-notice" kind={turn.notice} text={turn.text} at={turn.at}
        {...(turn.title ? { title: turn.title } : {})} />;
    case "ended":
      // Where the transcript stops, and why: a failure in its tone, not a
      // margin note (ChatNotice has no tones).
      return (
        <Callout key={turn.id} data-testid="chat-ended" data-outcome={turn.outcome}
          tone={turn.outcome === "failed" ? "danger" : "neutral"}>
          {turn.outcome === "aborted" ? abortedBy(turn, people) : turn.text}
        </Callout>
      );
    case "progress":
      // One row, updated in place as the agent reports.
      return (
        <ChatProgress key={turn.id} data-testid="chat-progress" role={role} done={turn.done} of={turn.of}
          step={turn.step} at={turn.at} startedAt={turn.startedAt} ended={ended} />
      );
    case "event":
      return <ChatEvent key={turn.id} data-testid="chat-event" role={role} type={turn.type} data={turn.data} at={turn.at} />;
    case "question":
      // A Run that ended on an unanswered question will never hear back.
      return (
        <QuestionCard
          key={turn.id}
          role={role}
          text={turn.text}
          options={turn.options}
          askedAt={turn.at}
          answeredAt={turn.answeredAt}
          dismissed={ended && turn.answeredAt === null}
          waitingOn={waitingOn}
        />
      );
    case "prompt":
      // Written by the factory, not a person: the avatar and name say so.
      return (
        <ChatMessage key={turn.id} role="system" name={dude} avatar={<DudeMark size="fill" />} intent="prompt" content={turn.text} startedAt={turn.at}
          {...turnImages(turn, shown)} />
      );
    case "message":
      return (
        <ChatMessage
          key={turn.id}
          role={role}
          content={turn.text}
          startedAt={turn.at}
          {...tokenFoot(turn, contextWindow)}
        />
      );
    case "thought":
      // Only when the thought ended is known, not how long it took.
      return <ThinkingBlock key={turn.id} text={turn.text} />;
    case "usage":
      // A turn's totals, at its end: a quiet foot, not a message.
      return (
        <ChatMessage
          key={turn.id}
          role={role}
          continued
          startedAt={turn.at}
          {...tokenFoot(turn, contextWindow)}
          costUsd={null}
        />
      );
    case "human": {
      // Signed: the person's face and name when known; "Someone" only when the ledger kept no one.
      const name = actorName(turn.by, people.names);
      const person = name && turn.by ? { ...(people.byId.get(turn.by.id) ?? {}), id: turn.by.id, name } : undefined;
      const wait = steer && turn.intent === "steer" && turn.deliveredAt === null && !turn.failed ? steer.wait(turn) : null;
      // Only where there is a turn to stop, and not twice.
      const interruptible = (wait?.kind === "tool" || wait?.kind === "next_step" || wait?.kind === "next_turn") && !turn.interrupting;
      return (
        <ChatMessage
          key={turn.id}
          data-testid="human-turn"
          role="human"
          intent={turn.intent}
          content={turn.text}
          startedAt={turn.at}
          deliveredAt={turn.deliveredAt}
          read={turn.read}
          readAfter={turn.after ? toolLabel(turn.after) : undefined}
          failed={turn.failed ?? undefined}
          {...(wait ? { pendingReason: pendingReason(wait) } : {})}
          {...(interruptible && steer ? { onInterrupt: () => steer.resend(turn, true) } : {})}
          {...(turn.failed && steer && turn.intent === "steer" ? { onRetry: () => steer.resend(turn, false) } : {})}
          person={person}
          name={name ?? "Someone"}
          {...turnImages(turn, shown)}
        />
      );
    }
    case "tool":
      return (
        <ToolCallCard
          key={turn.id}
          name={turn.tool}
          args={turn.args}
          status={turn.status}
          startedAt={turn.startedAt}
          endedAt={turn.endedAt}
          output={turn.result?.output ?? turn.result?.stdout}
          stderr={turn.result?.stderr}
          exitCode={turn.result?.exitCode}
        />
      );
  }
}

/** "Aborted by Ana: wrong task" — who stopped it, and why, when known. */
function abortedBy(turn: Extract<Turn, { kind: "ended" }>, people: People): string {
  const name = actorName(turn.by, people.names);
  const who = name ? `Aborted by ${name}` : "Aborted";
  return turn.why ? `${who}: ${turn.why}` : `${who}.`;
}

const ENDED_WORDS: Record<"completed" | "failed" | "aborted", string> = {
  completed: "This run finished.",
  failed: "This run failed.",
  aborted: "This run was aborted.",
};

/**
 * In place of the composer once a Run has ended — nobody would hear a
 * steer: how it ended, and the way back to its task, where what happens
 * next is decided. Why it failed is the transcript's last line, just above.
 */
function RunEnded({ run, onOpenTask }: { run: RunDetail; onOpenTask?: (() => void) | undefined }) {
  const outcome = run.status === "failed" || run.status === "aborted" ? run.status : "completed";
  return (
    <Callout data-testid="run-ended" data-outcome={outcome}
      tone={outcome === "failed" ? "danger" : outcome === "aborted" ? "attention" : "neutral"}>
      <span className="runEnded">
        <span>{ENDED_WORDS[outcome]}</span>
        {/* On its task's page, the task is already here. */}
        {!onOpenTask ? null : (
          <Button size="sm" variant="quiet" trailingIcon="arrow-right" onClick={onOpenTask} data-testid="run-ended-task">
            Back to the task
          </Button>
        )}
      </span>
    </Callout>
  );
}

/** In place of the composer on a branch preview: it has no agent, and its servers are on the task's Servers tab. */
export function PreviewRunNote({ openServers }: { openServers?: (() => void) | undefined }) {
  return (
    <Callout tone="neutral" data-testid="preview-run-note">
      <span className="runEnded">
        <span>
          A branch preview has no agent; its checkout is the branch as it stood.{" "}
          {openServers ? "Start, stop and open its servers on the task’s Servers tab." : "Its servers are on its task’s page, under the Servers tab."}
        </span>
        {openServers ? (
          <Button size="sm" variant="quiet" trailingIcon="arrow-right" onClick={openServers} data-testid="preview-run-servers">
            Servers
          </Button>
        ) : null}
      </span>
    </Callout>
  );
}

/**
 * The Run's lux terminal URL, read from its servers when it is running and
 * kept: it names the lux Run, which a resume keeps. Until known, read again
 * whenever `askAgain` moves. Null until known.
 *
 * One read at a time per client and Run: an `askAgain` during a read is
 * folded into one follow-up after it, and the read out still lands. Only a
 * new client or Run, or unmounting, discards an answer.
 */
function useTerminalUrl(client: ApiClient, runId: string, running: boolean, askAgain: number): { url: string | null; memoryLimit: number | null } {
  const [url, setUrl] = useState<{ runId: string; url: string; memoryLimit: number | null } | null>(null);
  const known = url?.runId === runId ? url.url : null;
  const reader = useRef<TerminalReader | null>(null);
  useEffect(() => {
    const r = new TerminalReader(client, runId, (found, memoryLimit) => setUrl({ runId, url: found, memoryLimit }));
    reader.current = r;
    return () => {
      r.dead = true;
    };
  }, [client, runId]);
  useEffect(() => {
    const r = reader.current;
    if (!r) return;
    r.wanted = running && !known;
    if (r.wanted) r.ask();
  }, [client, runId, running, known, askAgain]);
  return { url: known, memoryLimit: url?.runId === runId ? url.memoryLimit : null };
}

class TerminalReader {
  dead = false;
  /** Running and the URL still unknown: a follow-up is only read while this holds. */
  wanted = false;
  private inFlight = false;
  private again = false;
  constructor(
    private readonly client: ApiClient,
    private readonly runId: string,
    /** The terminal's URL, and the memory limit lux gave the container when it says (read in the same answer). */
    private readonly found: (url: string, memoryLimit: number | null) => void,
  ) {}

  ask(): void {
    if (this.dead) return;
    if (this.inFlight) {
      this.again = true;
      return;
    }
    this.inFlight = true;
    this.again = false;
    this.client
      .runServers(this.runId)
      .then((s) => {
        const url = s.run?.terminalUrl;
        if (!this.dead && url) {
          this.wanted = false;
          this.found(url, s.run?.memoryLimit ?? null);
        }
      }, () => undefined)
      .finally(() => {
        this.inFlight = false;
        if (this.again && this.wanted) this.ask();
      });
  }
}

/**
 * The machine the Run ran on, as it was when it started: its size's name
 * and spec, where the size came from, and — when lux reports it — the
 * memory its container actually got.
 */
function RunMachineChip({ machine, memoryLimit, role, phase }: { machine: NonNullable<RunDetail["machine"]>; memoryLimit: number | null; role: AgentRole; phase: string | null }) {
  const agent = phase === "fix" ? "Fixer" : (SETTINGS_ROLE_LABEL as Record<string, string>)[role] ?? "agent";
  const from = machine.from === "project" ? `From its project’s settings for the ${agent}.`
    : machine.from === "organization" ? `From the organisation’s settings for the ${agent}.`
    : machine.from === "implementer" ? "The implementer’s size: the fixer has none of its own."
    : "The organisation’s default size: nothing names another for it.";
  const asked = gib(machine.memoryMiB * MIB);
  const limit = memoryLimit ?? machine.memoryLimit ?? null;
  return (
    <MachineChip name={machine.name} spec={machineSpec(machine)} data-testid="run-machine"
      tooltip={
        <MachineTip name={machine.name}>
          {from} Fixed when the session started — editing {machine.name} now changes the next session, not this one.
          {limit ? ` It asked for ${asked} GiB and got ${gib(limit)}: every run on its host gives up the same share to Linux.` : null}
          {machine.pool ? ` Pool ${machine.pool}.` : null}
        </MachineTip>
      } />
  );
}

/** A message's token foot: the context then, against the window when known, and the turn's output. */
function tokenFoot(turn: { contextTokens: number | null; outputTokens: number | null }, contextWindow: number) {
  return {
    contextTokens: turn.contextTokens ?? undefined,
    contextWindowTokens: turn.contextTokens !== null && contextWindow > 0 ? contextWindow : undefined,
    outputTokens: turn.outputTokens ?? undefined,
  };
}

/** One line describing an event, for the debugging timeline. */
export function summarize(event: PersistedEvent): string {
  const payload = event.payload;
  if (event.eventType === EventTypes.GitClone || event.eventType === EventTypes.GitCheckout) {
    const repo = typeof payload.repo === "string" ? payload.repo : "repository";
    const ref = typeof payload.ref === "string" ? payload.ref : "";
    const branch = typeof payload.branch === "string" ? payload.branch : "";
    const base = typeof payload.base === "string" ? payload.base : "";
    const status = typeof payload.status === "string" ? payload.status : "";
    const error = typeof payload.error === "string" ? payload.error.split("\n")[0] : "";
    const action = event.eventType === EventTypes.GitCheckout ? "Checked out" : status === "cloned" ? "Cloned" : "Clone";
    return [action, repo, ref && `at ${ref}`, branch && `on ${branch}`, base && `from ${base}`,
      event.eventType === EventTypes.GitClone && status !== "cloned" && status, error].filter(Boolean).join(" · ");
  }
  // Tool events carry the harness's arguments, which the design system
  // already knows how to condense (todo lists, paths, commands).
  if (typeof payload.tool === "string") {
    const args = summarizeToolArgs(payload.input);
    return args ? `${payload.tool} · ${args}` : payload.tool;
  }
  for (const key of ["text", "reason", "error", "status"]) {
    const value = payload[key];
    if (typeof value === "string" && value.length > 0) return value.split("\n")[0]!;
  }
  return "";
}

/** The raw payload, rendered only when a row is expanded. */
function PayloadDetail({ payload }: { payload: Record<string, unknown> }) {
  return <pre>{JSON.stringify(payload, null, 2)}</pre>;
}

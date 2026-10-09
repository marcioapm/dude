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

import { Fragment, memo, useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import {
  AgentPlan,
  ChatComposer,
  ChatEvent,
  ChatMessage,
  ChatNotice,
  ChatProgress,
  ChatTranscript,
  CostDisplay,
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
  TierChip,
  TierTip,
  AttachDropZone,
  CanRunContainersBadge,
  ImageViewer,
} from "@dude/design-system/components";
import { Badge, Button, Callout, Dialog, LinkButton, Spinner, Textarea } from "@dude/design-system/primitives";
import { BUILDER_GIVE_UP_MINUTES, builderOffline, DEFAULT_RUN_ROLE, EventTypes, MIB, SETTINGS_ROLE_LABEL, TERMINAL_RUN_STATUSES, gib, machineSpec, runLabel, shortDigest } from "@dude/domain";
import type { AgentRole, PersistedEvent } from "@dude/domain";
import type { ApiClient, CostSplit, Person, RecoverAction, RunDetail, RunDiffSummary } from "../api/client.ts";
import { keptUntil as keptUntilDay } from "./Recovery.tsx";
import { WaitingForHost } from "../waiting.tsx";
import { useRunServers } from "../runServers.ts";
import { ApiError, modelCostShown } from "../api/client.ts";
import { CostOf } from "./MetricsSection.tsx";
import {
  PAUSE_WORDS, actorName, apply, emptyProjection, humanActor, landsHint, project, snapshot, steerWait, toolLabel, type GithubRef, type HumanTurn, type SteerWait, type ToolTurn, type Turn,
} from "../api/conversation.ts";
import { useNetworkNotes } from "../networkRefused.tsx";
import type { ComposerSubmission } from "@dude/design-system/components";
import { useEventStream } from "../hooks/useEventStream.ts";
import { conflictNotice, type Notice } from "../conflict.ts";
import { firstName, formatTimestamp, Icon } from "@dude/design-system";
import { usePeople, type People } from "../people.tsx";
import { NotFound } from "./NotFound.tsx";
import { DudeMark, dudeName } from "../DudeMark.tsx";
import { ChangesPanel } from "./ChangesPanel.tsx";
import { TurnImages, limitsHint, useAttachmentLimits, useImageTray, useSentImages, type SentImages } from "../hooks/useImages.tsx";
import type { EndedLedgers } from "./endedLedgers.ts";
import type { AttachmentInfo } from "@dude/domain";
import { RunReplacement, runRestartedText, runStatusLabel } from "../runPresentation.tsx";

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
  /**
   * Shown as a task's Chat, its conductor's conversation: no session
   * header or view switch, `head` above the turns (the task's history), and
   * a composer that talks to the conductor through `send` — always open: a
   * parked conductor wakes for a message, and one that ended is replaced.
   */
  chat?: ChatVariant | undefined;
  /** What its end strip says of a session that stopped, beyond how it ended. */
  stopped?: StoppedRun | undefined;
}

export interface ChatVariant {
  head: ReactNode;
  send: (text: string) => Promise<unknown>;
  /** What the rail says of the task, beside the conductor's own facts. */
  briefedWith: ReadonlyArray<{ label: string; value: ReactNode; mono?: boolean }>;
  /** The task's earlier conductors' conversations, above this one's turns. */
  before?: ReactNode;
  /** The conductor's whole cost, from the task's metrics; null until read. */
  cost?: RunCost | null;
  /**
   * Lines beside the conductor's turns, each where it happened: the Runs it
   * started, the decisions waited on, dude's notices. Merged in by time.
   */
  lines?: ReadonlyArray<{ id: string; at: string; node: ReactNode }>;
  /** Above the composer: who decides, and the way to hand it back. */
  above?: ReactNode;
  /** The conductor only reads and answers, though there is a line above (Deliver decides). */
  readOnly?: boolean;
}

/** A Run's cost as the task's metrics split it: tokens and machine time. */
export interface RunCost {
  cost: CostSplit;
  tokens: number;
  activeMs: number;
}

/**
 * A stopped session: the work went on elsewhere (a start over set its
 * attempt aside, or a new session took its step up again), or its task can
 * be picked back up from here — resumed while it is kept, until when.
 * One of an attempt set aside is read-only whatever its state, and may
 * offer the way to the current attempt.
 */
export type StoppedRun =
  | { readonly setAside: "restart" | "retry"; readonly toCurrent?: { readonly attempt: number; readonly go: () => void } | undefined }
  | { readonly onPickUp: (action: RecoverAction) => void; readonly keptUntil: string | null };

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
  EventTypes.RunRestarted,
  EventTypes.RunPaused,
  EventTypes.RunResumed,
  // Waiting for its image, then handed to lux: "Preparing image" comes and goes.
  EventTypes.RunImagePreparing,
  EventTypes.RunLeaseAcquired,
]);

export const RunScreen = memo(function RunScreen({ client, runId, onOpenTask, onOpenServers, onBack, task: given, chat, stopped }: RunScreenProps) {
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
  // A tool call whose output names a host lux refused this Run says so under it.
  const networkNote = useNetworkNotes(client, run?.projectId, events);

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

  // Preparing its image: the builder's progress is not on the Run's
  // stream, so its state is read again every few seconds until it goes.
  const preparing = Boolean(run?.preparingImage);
  useEffect(() => {
    if (!preparing) return;
    const t = setInterval(() => void client.getRun(runId).then(setRun, () => undefined), 4000);
    return () => clearInterval(t);
  }, [client, runId, preparing]);

  // Who drives the task is read once per task, not on every status change,
  // and apart from the Run: failing to learn it only leaves the asks
  // answerable here, which the orchestrator still checks.
  const taskId = given ? undefined : run?.taskId;
  // The lux terminal's link, for the rail while the Run is running. The Run
  // itself does not carry it (lux's console URL is the orchestrator's), so
  // it is read from the Run's servers. Until it is known, a servers.changed
  // (lux took the Run) or a stream that came back asks again.
  const askAgain = useMemo(() => (events.findLast((e) => e.eventType === EventTypes.ServersChanged)?.cursor ?? 0) + reconnects * 1e9, [events, reconnects]);
  // lux has no host for the Run yet (a first placement, a resume, a move):
  // why, as lux says it, asked again when lux's state changes. The answer,
  // not dude's status, is what shows the wait.
  const waitAsk = useMemo(() => (events.findLast((e) => e.eventType === EventTypes.ServersChanged &&
    (e.payload as { change?: unknown } | null)?.change === "state")?.cursor ?? 0) + reconnects * 1e9, [events, reconnects]);
  const { terminalUrl, memoryLimit, waitingReason } = useRunServers(client, runId, run?.status, askAgain, waitAsk);

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
  // A session of an attempt set aside is only to read: no Pause, Abort or composer.
  const readOnly = stopped !== undefined && "setAside" in stopped && stopped.setAside === "restart";
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
   * state catches up. Anything else is a problem, said plainly. Resolves
   * whether it went: a composer keeps the words of one that did not.
   */
  const intervene = useCallback(
    async (action: () => Promise<unknown>, label: string): Promise<boolean> => {
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
  // second submit of the same words while the first is on its way. In a
  // task's Chat every message — an answer to its question too — goes to
  // the task's Chat, which knows what the conductor waits on (words only:
  // Chat's composer has no image tray).
  const limits = useAttachmentLimits(client);
  const tray = useImageTray(client, run?.taskId, limits);
  const images = useSentImages(client);
  // The images of one message, open in the viewer.
  const [viewing, setViewing] = useState<{ turn: ViewedTurn; index: number } | null>(null);
  const send = useCallback(
    async (submission: ComposerSubmission) => {
      const ok = await (chat ? intervene(() => chat.send(submission.text), "send the message")
        : submission.mode === "answer"
        ? intervene(() => client.answer(submission.questionId, submission.text, submission.attachmentIds), "answer the agent")
        : intervene(() => client.steer(runId, submission.text, { interrupt: submission.mode === "steer" && submission.interrupt,
          attachmentIds: submission.attachmentIds }), "steer this run"));
      // Sent: the images are the message's now, not the tray's.
      if (ok) tray.clear(submission.attachmentIds);
      else throw new Error("not sent");
    },
    [client, runId, intervene, tray, chat],
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
    statusLabel: runStatusLabel(run),
    ...(waitingReason ? { statusNote: <Badge size="sm" icon="clock" data-testid="run-waiting">Waiting for a host</Badge> } : {}),
    ...(owner ? { owner } : {}),
    subtitle: (
      <>
        {owner ? <span>for {firstName(owner.name)}</span> : <span>{runLabel(run)}</span>}
        {run.model ? <RunTierChip tier={run.modelTier} model={run.model} effort={run.effort} role={role} phase={run.phase} /> : null}
        {run.machine ? <RunMachineChip machine={run.machine} memoryLimit={memoryLimit} role={role} phase={run.phase} /> : null}
        {run.image ? <RunImageChip image={run.image} /> : null}
        {/* Recorded at submit; nothing for false or a Run from before it was recorded. */}
        {run.canRunContainers ? <CanRunContainersBadge tooltip={CAN_RUN_CONTAINERS_TIP} /> : null}
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
  const composerOpen = isLive && !readOnly && !isPreviewRun && !((run.status === "paused" && !conversation.openQuestion) ||
    (conversation.openQuestion !== null && waitingOn !== undefined));
  const changed = diffSummary?.files ?? [];
  // The name dude signs this task's messages with.
  const dude = dudeName(run.taskId);
  // What a queued steer waits for, and the ways to act on one.
  const activeTool = conversation.activeTool?.name ?? null;
  // A set-aside session is ended to its transcript too, though its Run may
  // still be recorded as running: no decisions, no Interrupt now or Retry.
  const steer: SteerActions | undefined = isLive && !readOnly ? {
    wait: (turn) => steerWait(turn, run.status, activeTool, conversation.lands),
    resend: resteer,
  } : undefined;
  const shown = { images, open: (turn: ViewedTurn, index: number) => setViewing({ turn, index }) };
  const restartedText = runRestartedText(run, events, people);
  const render = (turn: Turn) => turn.kind === "ended" && turn.outcome === "aborted" && restartedText ? null
    : renderTurn(turn, role, conversation.contextWindow, !isLive || readOnly, people, dude, decide, waitingOn, steer, shown, networkNote);

  if (chat) {
    // A task's Chat: the conductor's conversation under the task's history,
    // a composer that always talks to the task's conductor, and the rail
    // with what it was briefed with — no session header or views: the task
    // page around it is the context.
    const asking = conversation.openQuestion;
    return (
      <div className="runScreen" data-view="chat" data-testid="chat-screen">
        <div className="runView">
          <div className="runChat">
            <ChatTranscript
              fill
              live={isLive}
              revision={events.length}
              turns={conversation.turns.length}
              pinned={chat.head}
              footer={
                <>
                {chat.above}
                <ChatComposer
                  mode={asking ? "answer" : "chat"}
                  question={asking ? {
                    id: asking.questionId, text: asking.text, askedBy: "the conductor", askedAt: asking.at,
                    options: waitingOn ? [] : asking.options,
                  } : undefined}
                  disabled={asking !== null && waitingOn !== undefined}
                  disabledReason={waitingOn ? `Waiting for ${waitingOn} to answer.` : undefined}
                  onSubmit={send}
                  sentAs={youName ? firstName(youName) : undefined}
                  to={chat.above && !chat.readOnly ? <>To <b>Conductor</b></> : <>To <b>Conductor</b> · read-only</>}
                />
                </>
              }
              emptyMessage="Waiting for the conductor to start."
            >
              {chat.before}
              {interleaved(grouped, chat.lines ?? []).map((item) => "node" in item
                ? <Fragment key={item.id}>{item.node}</Fragment>
                : Array.isArray(item.group)
                  ? <ChatAside key={item.group[0]!.id}>{item.group.map(render)}</ChatAside>
                  : render(item.group))}
              {conversation.activity ? (
                <ChatMessage role={role} activity={conversation.activity}
                  activityProps={conversation.activeTool ? { label: conversation.activeTool.name, since: conversation.activeTool.since } : undefined} />
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
            <SessionRail className="runRail" aria-label="The conductor" data-testid="chat-rail">
              <SessionRailBlock label="Briefed with">
                <SessionFacts facts={chat.briefedWith} />
              </SessionRailBlock>
              <SessionRailBlock label="Conductor">
                <SessionFacts facts={[
                  ...(run.model ? [{ label: "Model", value: run.model, mono: true }] : []),
                  ...(run.machine ? [{ label: "Machine", value: run.machine.name }] : []),
                  // The whole cost, tokens and machine time, as the task's metrics split it.
                  { label: "Cost", value: chat.cost
                    ? <CostOf cost={chat.cost.cost} tokens={chat.cost.tokens} activeMs={chat.cost.activeMs} />
                    : <CostDisplay usd={modelCostShown(conversation.costUsd, conversation.costSource.from)} /> },
                ]} />
              </SessionRailBlock>
              {tools.length > 0 ? (
                <SessionRailBlock label="Tools used">
                  <ToolUsage tools={tools} />
                </SessionRailBlock>
              ) : null}
            </SessionRail>
          </div>
        </div>
        {notice ? <Callout tone="neutral" data-testid="conflict-notice">{notice.text}</Callout> : null}
        {problem ? <Callout tone="danger">{problem}</Callout> : null}
      </div>
    );
  }
  // A checkout to show: a Run with one, or any Run that has reported a diff
  // (the rail's files open Changes, so Changes must be there to open).
  const hasChanges = Object.keys(run.baseRefs).length > 0 || run.phase !== null || changed.length > 0;
  // Changes gone from the switch (its diff emptied, with no checkout to fall back on): back to the conversation.
  if (view === "changes" && !hasChanges) showView("chat");
  const liveDiff = isLive && run.status !== "paused";

  // The terminal, while the Run is running: in the rail, and in the header
  // only where the rail is not (a narrow session, or a view other than the
  // conversation), so it is always one click away and never shown twice. A
  // set-aside session offers none: a shell there could push to its branch.
  const terminal = run.status === "running" && !readOnly ? terminalUrl : null;
  // A branch preview has no agent to pause or abort: only its terminal.
  const actions = isLive && !readOnly ? (
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
        conversationOption,
        // The agent's checkout, as it changes: only for a Run with one.
        ...(hasChanges ? [{ value: "changes" as const, label: (
          <>
            <Icon name="git-branch" size={13} />Changes
            {changed.length > 0 ? <span className="ds-tnum runCount">{changed.length}</span> : null}
            {liveDiff && changed.length > 0 ? <span className="ds-live-dot" aria-label="changing now" /> : null}
          </>
        ) }] : []),
        // Debugging, not the daily view — hence last.
        eventsOption(events.length),
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
          <EventLog events={events} people={people} />
        ) : (
          <div className="runChat">
            <ChatTranscript
              fill
              live={isLive}
              revision={events.length}
              turns={conversation.turns.length}
              pinned={
                run.preparingImage ? (
                  <PreparingImage preparing={run.preparingImage} />
                ) : waitingReason ? (
                  <WaitingForHost reason={waitingReason} onRunPage />
                ) : conversation.plan.length > 0 ? (
                  <AgentPlan items={conversation.plan} defaultCollapsed data-testid="plan" />
                ) : null
              }
              footer={!isLive || readOnly ? <RunEnded run={run} onOpenTask={onOpenTask ? () => onOpenTask(run.taskId) : undefined}
                stopped={stopped} restartedText={restartedText} /> : isPreviewRun ? (
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
 * The turns, with each run of what an agent does between its messages —
 * tool calls, thoughts, the events and progress it records — gathered
 * into one group: a `ChatAside` puts them on the message column, stacked
 * close, with a turn's air above and below the run, as the design system
 * draws it. Never one of them alone at the transcript's edge.
 */
/**
 * The conductor's turns (grouped into asides) and the Chat's other lines,
 * merged by time: a line goes before the first turn that came after it.
 * Stable: turns keep their order, and lines theirs.
 */
export function interleaved(groups: ReadonlyArray<Turn | Turn[]>, lines: ReadonlyArray<{ id: string; at: string; node: ReactNode }>):
  Array<{ group: Turn | Turn[] } | { id: string; at: string; node: ReactNode }> {
  const out: Array<{ group: Turn | Turn[] } | { id: string; at: string; node: ReactNode }> = [];
  let i = 0;
  for (const group of groups) {
    const turn = Array.isArray(group) ? group[0]! : group;
    let at: string | null = null;
    if ("at" in turn) at = turn.at;
    else if ("startedAt" in turn) at = turn.startedAt;
    while (i < lines.length && at !== null && lines[i]!.at < at) out.push(lines[i++]!);
    out.push({ group });
  }
  while (i < lines.length) out.push(lines[i++]!);
  return out;
}

export function asides(turns: readonly Turn[]): Array<Turn | Turn[]> {
  const out: Array<Turn | Turn[]> = [];
  for (const turn of turns) {
    if (turn.kind !== "tool" && turn.kind !== "thought" && turn.kind !== "event" && turn.kind !== "progress") {
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

/** The view switch's Conversation option, as every session's bar has it. */
export const conversationOption = { value: "chat" as const, label: <><Icon name="message" size={13} />Conversation</> };

/** The view switch's Events option, with the ledger's count: last, being for debugging. */
export function eventsOption(count: number) {
  return { value: "events" as const, label: <><Icon name="list" size={13} />Events<span className="ds-tnum runCount">{count}</span></> };
}

/**
 * Events: a session's ledger as it arrived, one row per event, each
 * opening onto its payload. An agent session's, and a brainstorm
 * session's (every Run of it, and the session's own events).
 */
export function EventLog({ events, people }: { events: readonly PersistedEvent[]; people: People }) {
  return (
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
  );
}

/** A live Run's queued steers: what each waits for, and sending one again (interrupting, or after a failure). */
export interface SteerActions {
  wait: (turn: HumanTurn) => SteerWait;
  resend: (turn: HumanTurn, interrupt: boolean) => void;
}

/** The one line under a queued steer. */
export function pendingReason(wait: SteerWait) {
  switch (wait.kind) {
    case "tool": return <>Lands after <b>{toolLabel(wait.tool)}</b> finishes.</>;
    case "next_step": return "Lands at the agent's next step.";
    case "next_turn": return "This agent reads messages only between turns — lands when this turn ends.";
    case "paused": return "Lands when the run resumes.";
    case "starting": return "Lands when the agent starts.";
  }
}

/**
 * A task's conductor that ended, in its Chat above the next: its whole
 * conversation, read once for the task's page (EndedLedgers), with nothing
 * to answer or steer; its images shown, and opened in the viewer, as the
 * live one's are. A read that failed says so, with a retry. A line says
 * where it ended; the latest conductor takes what is written next.
 */
export const EndedConductor = memo(function EndedConductor({ ledgers, runId, status }: {
  ledgers: EndedLedgers;
  runId: string;
  status: RunDetail["status"];
}) {
  const people = usePeople();
  const images = useSentImages(ledgers.client);
  const [viewing, setViewing] = useState<{ turn: ViewedTurn; index: number } | null>(null);
  const [events, setEvents] = useState<PersistedEvent[] | null>(() => ledgers.cached(runId));
  const [failed, setFailed] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (ledgers.cached(runId)) return;
    let cancelled = false;
    setFailed(null);
    ledgers.read(runId, () => cancelled).then((read) => {
      if (read && !cancelled) setEvents(read);
    }, (err: unknown) => {
      if (!cancelled) setFailed(err instanceof ApiError ? err.message : "the request failed");
    });
    return () => {
      cancelled = true;
    };
  }, [ledgers, runId, attempt]);
  const turns = useMemo(() => (events ? asides(project(events, status).turns) : []), [events, status]);
  if (failed && !events) {
    return (
      <div className="chatEarlier" data-testid="earlier-conductor" data-run={runId}>
        <Callout tone="danger" data-testid="earlier-conductor-failed">
          <span className="noticeLine">
            Could not read this earlier conductor’s conversation: {failed}
            <Button size="sm" variant="quiet" onClick={() => setAttempt((n) => n + 1)} data-testid="earlier-conductor-retry">
              Retry
            </Button>
          </span>
        </Callout>
      </div>
    );
  }
  if (!events) return null;
  const dude = dudeName(events[0]?.taskId ?? "");
  const shown = { images, open: (turn: ViewedTurn, index: number) => setViewing({ turn, index }) };
  const render = (turn: Turn) => renderTurn(turn, "conductor", 0, true, people, dude, undefined, undefined, undefined, shown);
  return (
    <div className="chatEarlier" data-testid="earlier-conductor" data-run={runId}>
      {turns.map((group) => Array.isArray(group)
        ? <ChatAside key={group[0]!.id}>{group.map(render)}</ChatAside>
        : render(group))}
      <ChatNotice kind="parked" by={dude} at={events.at(-1)?.occurredAt ?? Date.now()} data-testid="earlier-conductor-ended"
        text="This conductor has ended. The next one takes what you write, briefed afresh." />
      {viewing ? (
        <ImageViewer
          images={viewing.turn.attachments.map(images.sent)}
          index={viewing.index}
          onIndexChange={(index) => setViewing({ ...viewing, index })}
          onClose={() => setViewing(null)}
          context={viewedContext(viewing.turn, people, "Conductor", dude)}
          readAt={viewing.turn.kind === "human" && viewing.turn.read && viewing.turn.deliveredAt ? clock(viewing.turn.deliveredAt) : undefined}
          onWantOriginal={(i) => images.wantOriginal(viewing.turn.attachments[i]!.id)}
          onDownload={(i, variant) => void images.download(viewing.turn.attachments[i]!, variant)}
        />
      ) : null}
    </div>
  );
});

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

/** Where a Chat message was written on GitHub, or where the conductor's reply went: the pull request, linked to the comment. */
function GithubSource({ github, reply }: { github: GithubRef; reply: boolean }) {
  const pr = `${github.repo}#${github.number}`;
  const what = reply ? <>Replied on {pr}</> : <>On {pr}</>;
  return (
    <div className="githubSource" data-testid="github-source">
      {github.url ? <a href={github.url} target="_blank" rel="noreferrer noopener">{what} on GitHub</a> : <>{what} on GitHub</>}
    </div>
  );
}

/** "Márcio · steer to Implement · 15:52": who sent a message's images, to whom, when. */
function viewedContext(turn: ViewedTurn, people: People, agent: string, dude: string): string {
  const at = new Date(turn.at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
  if (turn.kind === "prompt") return `${dude} · task to ${agent} · ${at}`;
  const who = actorName(turn.by, people.names);
  return `${who ? firstName(who) : "Someone"} · ${turn.intent} to ${agent} · ${at}`;
}

export function renderTurn(turn: Turn, role: AgentRole, contextWindow: number, ended: boolean, people: People, dude: string,
  decide?: (requestId: string, approve: boolean) => void, waitingOn?: string, steer?: SteerActions, shown?: ShownImages,
  toolNote?: (turn: ToolTurn) => ReactNode) {
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
      return <ChatNotice key={turn.id} data-testid="chat-notice" kind={turn.notice} text={turn.text} at={turn.at} by={dude}
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
          settledBy={turn.closedAt === null ? undefined : turn.closedBy === "withdrawn" ? "Withdrawn" : "Decided on the banner"}
          waitingOn={turn.to ? questionFor(turn.to, people) : waitingOn}
          onlyThey={turn.to !== null}
        />
      );
    case "prompt":
      // Written by the factory, not a person: the avatar and name say so.
      // A conductor's is dude's briefing of it, tagged so.
      return (
        <ChatMessage key={turn.id} role="system" name={dude} avatar={<DudeMark size="fill" />} intent={turn.briefing ? "briefing" : "prompt"}
          content={turn.text} startedAt={turn.at} data-testid={turn.briefing ? "chat-briefing" : undefined} {...turnImages(turn, shown)} />
      );
    case "message":
      return (
        <ChatMessage
          key={turn.id}
          data-testid={role === "conductor" ? "conductor-turn" : undefined}
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
      // Signed: the person's face and name when known; "Someone" only when the ledger kept no one. A
      // conductor's steer is the conductor's: its face, colour and name, never a person's. One from a
      // pull request is signed by its GitHub login, and says where it was written.
      const name = turn.conductor ? null : actorName(turn.by, people.names);
      const person = name && turn.by && !turn.github ? { ...(people.byId.get(turn.by.id) ?? {}), id: turn.by.id, name } : undefined;
      const wait = steer && (turn.intent === "steer" || turn.intent === "message") && turn.deliveredAt === null && !turn.failed ? steer.wait(turn) : null;
      // Only where there is a turn to stop, and not twice; never a message in Chat, which starts a turn.
      const interruptible = turn.intent === "steer" && (wait?.kind === "tool" || wait?.kind === "next_step" || wait?.kind === "next_turn") && !turn.interrupting;
      return (
        <ChatMessage
          key={turn.id}
          data-testid="human-turn"
          data-by={turn.conductor ? "conductor" : turn.github ? "github" : undefined}
          role={turn.conductor ? "conductor" : turn.github ? "integration" : "human"}
          intent={turn.intent}
          content={turn.text}
          startedAt={turn.at}
          deliveredAt={turn.deliveredAt}
          read={turn.read}
          readAfter={turn.after ? toolLabel(turn.after) : undefined}
          failed={turn.failed ?? undefined}
          {...(turn.heldFor && turn.deliveredAt === null && !turn.failed
            ? { pendingReason: "Queued: goes after the answer the agent is waiting for." }
            : wait ? { pendingReason: pendingReason(wait) } : {})}
          {...(interruptible && steer ? { onInterrupt: () => steer.resend(turn, true) } : {})}
          {...(turn.failed && steer && turn.intent === "steer" ? { onRetry: () => steer.resend(turn, false) } : {})}
          person={person}
          name={turn.conductor ? "Conductor" : name ?? "Someone"}
          {...turnImages(turn, shown)}
          {...(turn.github ? { attachments: <GithubSource github={turn.github} reply={turn.conductor} /> } : {})}
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
          note={toolNote?.(turn)}
        />
      );
  }
}

/** Whom a session's question waits on: none when it is put to you, else their first name. */
function questionFor(to: { id: string; name: string | null }, people: People): string | undefined {
  if (to.id === people.you) return undefined;
  const name = actorName(to, people.names);
  return name ? firstName(name) : "someone else";
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
 * One that stopped (aborted, failed) offers its task's ways back — Resume…
 * while it is kept, and Other ways… — or, set aside, says where the work
 * went on.
 */
function RunEnded({ run, onOpenTask, stopped, restartedText }: { run: RunDetail; onOpenTask?: (() => void) | undefined; stopped?: StoppedRun | undefined; restartedText: string | null }) {
  const outcome = run.status === "failed" || run.status === "aborted" ? run.status : "completed";
  const setAside = stopped && "setAside" in stopped ? stopped.setAside : null;
  const toCurrent = stopped && "setAside" in stopped ? stopped.toCurrent : undefined;
  const pickUp = stopped && "onPickUp" in stopped ? stopped : null;
  return (
    <Callout data-testid="run-ended" data-outcome={outcome}
      tone={restartedText || setAside ? "neutral" : outcome === "failed" ? "danger" : outcome === "aborted" ? "attention" : "neutral"}>
      <span className="runEnded">
        <span>
          {restartedText ?? (setAside === "restart" ? "Set aside when its task was started over." : setAside === "retry" ? "Set aside: a new session took its step up again." : ENDED_WORDS[outcome])}
          {pickUp ? <span className="runEndedKept"> {pickUp.keptUntil ? `Kept until ${keptUntilDay(pickUp.keptUntil)}.` : "Its workspace is no longer kept."}</span> : null}
        </span>
        <span className="runEndedActions">
          <RunReplacement run={run} />
          {pickUp ? (
            <>
              {pickUp.keptUntil ? (
                <Button size="sm" variant="primary" leadingIcon="play" onClick={() => pickUp.onPickUp("resume")} data-testid="run-ended-resume">Resume…</Button>
              ) : null}
              <Button size="sm" variant="quiet" onClick={() => pickUp.onPickUp("retry")} data-testid="run-ended-other">
                {pickUp.keptUntil ? "Other ways…" : "Pick it back up…"}
              </Button>
            </>
          ) : null}
          {toCurrent ? (
            <Button size="sm" variant="quiet" trailingIcon="arrow-right" onClick={toCurrent.go} data-testid="run-ended-current">
              Go to attempt {toCurrent.attempt}
            </Button>
          ) : null}
          {/* On its task's page, the task is already here. */}
          {!onOpenTask ? null : (
            <Button size="sm" variant="quiet" trailingIcon="arrow-right" onClick={onOpenTask} data-testid="run-ended-task">
              Back to the task
            </Button>
          )}
        </span>
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
 * What the session asked the proxy for: its tier, the model the tier
 * requested when the session started, and the effort it asked for. What
 * the proxy served is the proxy's to say. A Run from before tiers shows its
 * model alone.
 */
function RunTierChip({ tier, model, effort, role, phase }: { tier: string | null; model: string; effort: string | null; role: AgentRole; phase: string | null }) {
  const agent = phase === "fix" ? "Fixer" : (SETTINGS_ROLE_LABEL as Record<string, string>)[role] ?? "agent";
  const asked = <><code>{model}</code>{effort ? ` at effort ${effort}` : ""}</>;
  return (
    <TierChip tier={tier} model={model} effort={effort} data-testid="run-model"
      tooltip={
        <TierTip title={tier ?? model}
          aside="That is what dude asked for; how the proxy served it is the proxy’s to say.">
          {tier
            ? <>The {agent}’s tier. When this session started, {tier} asked the proxy for {asked}; changing {tier} now changes the next session, not this one.</>
            : <>When this session started, dude asked the proxy for {asked}.</>}
        </TierTip>
      } />
  );
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

const CAN_RUN_CONTAINERS_TIP = "This Run can start containers inside it. Fixed when the session started: its resumes keep it.";

/**
 * The library image the Run got, as it was when it started: its name and
 * version, and its digest in the tooltip. A newer version published since
 * reaches the next Run, never this one (nor its resume).
 */
function RunImageChip({ image }: { image: NonNullable<RunDetail["image"]> }) {
  return (
    <MachineChip icon="cube" name={image.name} spec={`v${image.version}`} data-testid="run-image"
      tooltip={
        <MachineTip name={`${image.name} v${image.version}`}>
          Fixed when the session started: a version published since reaches the next session, not this one. {shortDigest(image.ref)}, with
          the dude layer {shortDigest(image.layer)}.
        </MachineTip>
      } />
  );
}

/**
 * While a Run waits for its image — the dude layer being added to it, or
 * its first version building — before it goes to lux: what it waits on, and
 * that no model time is spent meanwhile.
 */
export function PreparingImage({ preparing }: { preparing: NonNullable<RunDetail["preparingImage"]> }) {
  const label = `${preparing.imageName}${preparing.version ? ` v${preparing.version}` : ""}`;
  if (preparing.builderOfflineSince) {
    const offline = builderOffline(preparing.builderOfflineSince, (iso) => formatTimestamp(iso, "datetime"));
    return (
      <Callout tone="attention" data-testid="preparing-image">
        <b>Preparing image: waiting for {label}</b>, but the {offline}. The session starts once the builder is back and done;
        if it stays offline for {BUILDER_GIVE_UP_MINUTES} minutes of the wait, this Run fails before it starts. Nothing is spent meanwhile.
      </Callout>
    );
  }
  const now = preparing.state === "running" ? "The builder is on it now" : "It is next in the builder’s line";
  if (preparing.kind === "build") {
    return (
      <Callout tone="info" data-testid="preparing-image">
        <b>Preparing image: building {label}</b> (its first version). {now}; the session starts once it is built and published.
        Nothing is spent until then.
      </Callout>
    );
  }
  return (
    <Callout tone="info" data-testid="preparing-image">
      <b>Preparing image: adding the dude layer</b> to {label}. {now};
      the session starts once it is done, usually within a minute or two. Nothing is spent until then.
    </Callout>
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

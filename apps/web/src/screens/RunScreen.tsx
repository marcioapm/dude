/**
 * The Run screen — the operator's day-to-day view.
 *
 * Opens on the conversation, because that is what someone supervising agents
 * actually reads. The event timeline and logs are available behind a tab:
 * they are debugging tools, reached when something looks wrong, not watched
 * continuously.
 */

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
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
  QuestionCard,
  ThinkingBlock,
  ToolCallCard,
  summarizeToolArgs,
} from "@dude/design-system/components";
import { Button, Callout, Dialog, Spinner, Tab, TabList, TabPanel, Tabs, Textarea } from "@dude/design-system/primitives";
import { DEFAULT_RUN_ROLE, EventTypes, TERMINAL_RUN_STATUSES, runLabel } from "@dude/domain";
import type { AgentRole, PersistedEvent } from "@dude/domain";
import type { ApiClient, Person, RunDetail } from "../api/client.ts";
import { ApiError, reportedCost } from "../api/client.ts";
import { PAUSE_WORDS, actorName, apply, emptyProjection, humanActor, snapshot, type Turn } from "../api/conversation.ts";
import type { ComposerSubmission } from "@dude/design-system/components";
import { useEventStream } from "../hooks/useEventStream.ts";
import { conflictNotice, type Notice } from "../conflict.ts";
import { firstName, usePeople, type People } from "../people.tsx";
import { NotFound } from "./NotFound.tsx";

export interface RunScreenProps {
  client: ApiClient;
  runId: string;
  /** The task's title, when the caller already knows it. */
  title?: string | undefined;
  /** Where this conversation sits, shown above it: a way back up. */
  breadcrumb?: ReactNode;
  /** Open the task this Run works on. */
  onOpenTask: (taskId: string) => void;
  /** Leave for somewhere that exists, when this Run does not. */
  onBack: () => void;
}

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

export function RunScreen({ client, runId, title, breadcrumb, onOpenTask, onBack }: RunScreenProps) {
  const [run, setRun] = useState<RunDetail | null>(null);
  const [missing, setMissing] = useState(false);
  // Who drives the task, and who is reading: only its owner answers its
  // agents, so anyone else sees the asks read-only, with whom they wait on.
  const [driver, setDriver] = useState<{ owner: Person | null; you: string } | null>(null);
  // The task's key (TEXT-14), which people know it by, for the header.
  const [taskKey, setTaskKey] = useState<string | undefined>(undefined);
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
  const taskId = run?.taskId;
  useEffect(() => {
    if (!taskId) return;
    let cancelled = false;
    Promise.all([client.getTask(taskId), client.listPeople()])
      .then(([task, people]) => {
        if (cancelled) return;
        setDriver({ owner: task.owner, you: people.you });
        setTaskKey(task.key);
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
  const isLive = run ? !TERMINAL_RUN_STATUSES.includes(run.status) : false;

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

  const send = useCallback(
    (submission: ComposerSubmission) =>
      void (submission.mode === "answer"
        ? intervene(() => client.answer(submission.questionId, submission.text), "answer the agent")
        : intervene(() => client.steer(runId, submission.text, { interrupt: submission.mode === "steer" && submission.interrupt }), "steer this run")),
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
  const waitingOn = driver?.owner && driver.owner.id !== driver.you ? driver.owner.name : undefined;
  const owner = driver?.owner ? (people.byId.get(driver.owner.id) ?? driver.owner) : undefined;
  const youName = people.you ? people.names.get(people.you) : undefined;

  // The agent this Run is. A phase Run carries its role; one created
  // directly through the API runs as an orchestrator.
  const role: AgentRole = run.role ?? DEFAULT_RUN_ROLE;
  const phase = run.phase ? runLabel(run) : null;

  const session = {
    id: run.id,
    role,
    status: run.status,
    ...(owner ? { owner } : {}),
    subtitle: (
      <>
        {owner ? <span>for {firstName(owner.name)}</span> : <span>{runLabel(run)}</span>}
        {run.model ? <code>{run.model}</code> : null}
        {taskKey ? <code title={`task ${run.taskId} · run ${run.id}`}>{taskKey}</code> : null}
      </>
    ),
    // The id is already shown beside the title; repeating it as the title
    // leaves the header saying nothing about the work. The attempt only
    // once there is more than one to tell apart.
    title: [title, phase, run.attempt > 1 ? `attempt ${run.attempt}` : null].filter(Boolean).join(" · "),
    taskId: run.taskId,
    ...(taskKey ? { taskKey } : {}),
    startedAt: run.startedAt ?? run.createdAt,
    endedAt: run.endedAt,
    ...(run.model ? { model: run.model } : {}),
    costUsd: reportedCost(conversation.costUsd),
    // The Run's own totals are exact; the projection's are what has streamed
    // in so far, for a Run still working.
    tokens: Math.max(run.tokens.input + run.tokens.output, conversation.tokens),
  };

  return (
    <div className="runScreen">
      {breadcrumb ? <div className="runCrumbs">{breadcrumb}</div> : null}
      <Tabs defaultValue="chat" fill>
        <TabList>
          <Tab value="chat">Conversation</Tab>
          {/* Debugging, not the daily view — hence second and quieter. */}
          <Tab value="events" count={events.length}>Events</Tab>
        </TabList>

        <TabPanel value="chat" fill>
          <ChatTranscript
            fill
            live={isLive}
            revision={events.length}
            session={session}
            headerActions={
              isLive ? (
                <>
                  {run.status === "paused" ? (
                    <Button
                      size="sm"
                      variant="secondary"
                      disabled={busy}
                      onClick={() => void intervene(() => client.resume(runId), "resume this run")}
                    >
                      Resume
                    </Button>
                  ) : (
                    <Button
                      size="sm"
                      variant="secondary"
                      disabled={busy}
                      onClick={() => void intervene(() => client.pause(runId), "pause this run")}
                    >
                      Pause
                    </Button>
                  )}
                  <Button size="sm" variant="danger" disabled={busy} onClick={() => setConfirmAbort(true)} data-testid="abort">
                    Abort…
                  </Button>
                </>
              ) : null
            }
            pinned={
              conversation.plan.length > 0 ? (
                <AgentPlan items={conversation.plan} defaultCollapsed data-testid="plan" />
              ) : null
            }
            footer={!isLive ? <RunEnded run={run} onOpenTask={() => onOpenTask(run.taskId)} /> : (
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
                  waitingOn && (conversation.openQuestion || run.dudePause === "person") ? `Waiting for ${waitingOn} to answer.`
                    : run.dudePause ? PAUSE_WORDS[run.dudePause].composer
                    : "This run is paused. Resume it to steer."
                }
                onSubmit={send}
                sentAs={youName ? firstName(youName) : undefined}
                canInterrupt
              />
            )}
            emptyMessage="Waiting for the agent to start."
          >
            {conversation.turns.map((turn) => renderTurn(turn, role, conversation.contextWindow, !isLive, people, decide, waitingOn))}
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
        </TabPanel>

        <TabPanel value="events" fill>
          <EventStream>
            {events.map((event) => (
              <EventRow
                key={event.eventId}
                occurredAt={event.occurredAt}
                eventType={event.eventType}
                actor={{ type: event.actor.type, id: event.actor.id, ...namedActor(event, people) }}
                summary={summarize(event)}
                // An element, not a string: EventRow only renders the detail
                // when the row is open, so the JSON is built for the handful
                // of rows an operator actually expands.
                detail={<PayloadDetail payload={event.payload} />}
              />
            ))}
          </EventStream>
        </TabPanel>
      </Tabs>

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
}

/** The actor's name for the event ledger, when it is a person the organisation knows. */
function namedActor(event: PersistedEvent, people: People): { name?: string } {
  const name = actorName(humanActor(event), people.names);
  return name ? { name } : {};
}

function renderTurn(turn: Turn, role: AgentRole, contextWindow: number, ended: boolean, people: People,
  decide?: (requestId: string, approve: boolean) => void, waitingOn?: string) {
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
      return <ChatNotice key={turn.id} data-testid="chat-notice" kind={turn.notice} text={turn.text} at={turn.at} />;
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
        <ChatMessage key={turn.id} role="system" name="dude" intent="prompt" content={turn.text} startedAt={turn.at} />
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
      return (
        <ChatMessage
          key={turn.id}
          data-testid="human-turn"
          role="human"
          intent={turn.intent}
          content={turn.text}
          startedAt={turn.at}
          deliveredAt={turn.deliveredAt}
          person={person}
          name={name ?? "Someone"}
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
function RunEnded({ run, onOpenTask }: { run: RunDetail; onOpenTask: () => void }) {
  const outcome = run.status === "failed" || run.status === "aborted" ? run.status : "completed";
  return (
    <Callout data-testid="run-ended" data-outcome={outcome}
      tone={outcome === "failed" ? "danger" : outcome === "aborted" ? "attention" : "neutral"}>
      <span className="runEnded">
        <span>{ENDED_WORDS[outcome]}</span>
        <Button size="sm" variant="quiet" trailingIcon="arrow-right" onClick={onOpenTask} data-testid="run-ended-task">
          Back to the task
        </Button>
      </span>
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
function summarize(event: PersistedEvent): string {
  const payload = event.payload;
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

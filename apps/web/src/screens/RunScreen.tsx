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
import { Button, Callout, Spinner, Tab, TabList, TabPanel, Tabs } from "@dude/design-system/primitives";
import { DEFAULT_RUN_ROLE, EventTypes, TERMINAL_RUN_STATUSES, runLabel } from "@dude/domain";
import type { AgentRole, PersistedEvent } from "@dude/domain";
import type { ApiClient, RunDetail } from "../api/client.ts";
import { ApiError } from "../api/client.ts";
import { PAUSE_WORDS, apply, emptyProjection, snapshot, type Turn } from "../api/conversation.ts";
import type { ComposerSubmission } from "@dude/design-system/components";
import { useEventStream } from "../hooks/useEventStream.ts";

export interface RunScreenProps {
  client: ApiClient;
  runId: string;
  /** The task's title, when the caller already knows it. */
  title?: string | undefined;
  /** Where this conversation sits, shown above it: a way back up. */
  breadcrumb?: ReactNode;
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

export function RunScreen({ client, runId, title, breadcrumb }: RunScreenProps) {
  const [run, setRun] = useState<RunDetail | null>(null);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);

  const { events, status: streamStatus } = useEventStream({ client, runId });

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
        if (!cancelled) setProblem(err instanceof Error ? err.message : String(err));
      });
    return () => {
      cancelled = true;
    };
  }, [client, runId, statusEventCount]);

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

  /** Run an intervention, surfacing conflicts as readable text. */
  const intervene = useCallback(
    async (action: () => Promise<unknown>, label: string) => {
      setBusy(true);
      setProblem(null);
      try {
        await action();
      } catch (err) {
        // A 409 usually means the Run moved on while the operator was
        // deciding — worth saying plainly rather than showing a stack.
        setProblem(
          err instanceof ApiError
            ? `Could not ${label}: ${err.message}`
            : `Could not ${label}.`,
        );
      } finally {
        setBusy(false);
      }
    },
    [],
  );

  const send = useCallback(
    (submission: ComposerSubmission) =>
      void (submission.mode === "answer"
        ? intervene(() => client.answer(submission.questionId, submission.text), "answer the agent")
        : intervene(() => client.steer(runId, submission.text), "steer this run")),
    [client, runId, intervene],
  );

  const decide = useCallback(
    (requestId: string, approve: boolean) =>
      void intervene(() => client.decideRepositoryRequest(requestId, approve),
        approve ? "approve the repository" : "decline the repository"),
    [client, intervene],
  );

  if (!run) {
    return <div className="runScreen">{problem ?? <Spinner label="Loading the run…" />}</div>;
  }

  // The agent this Run is. A phase Run carries its role; one created
  // directly through the API runs as an orchestrator.
  const role: AgentRole = run.role ?? DEFAULT_RUN_ROLE;
  const phase = run.phase ? runLabel(run) : null;

  const session = {
    id: run.id,
    role,
    status: run.status,
    // The id is already shown beside the title; repeating it as the title
    // leaves the header saying nothing about the work.
    title: [title, phase, `attempt ${run.attempt}`].filter(Boolean).join(" · "),
    taskId: run.taskId,
    startedAt: run.startedAt ?? run.createdAt,
    endedAt: run.endedAt,
    ...(run.model ? { model: run.model } : {}),
    // A cost of zero means the agent did not report one (a model behind a
    // proxy with no prices), not that the work was free.
    costUsd: conversation.costUsd > 0 ? conversation.costUsd : null,
    // The Run's own totals are exact; the projection's are what has streamed
    // in so far, for a Run still working.
    tokens: Math.max(run.tokens.input + run.tokens.output, conversation.tokens),
  };

  return (
    <div className="runScreen">
      {breadcrumb}
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
                  <Button
                    size="sm"
                    variant="destructive-outline"
                    disabled={busy}
                    onClick={() => void intervene(() => client.abort(runId), "abort this run")}
                  >
                    Abort
                  </Button>
                </>
              ) : null
            }
            pinned={
              conversation.plan.length > 0 ? (
                <AgentPlan items={conversation.plan} defaultCollapsed sticky />
              ) : null
            }
            footer={
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
                        options: conversation.openQuestion.options,
                      }
                    : undefined
                }
                // A paused Run takes an answer (a parked one is resumed by it),
                // not a steer.
                disabled={!isLive || (run.status === "paused" && !conversation.openQuestion)}
                disabledReason={
                  !isLive ? "This run has finished — nobody would hear it."
                    : run.dudePause ? PAUSE_WORDS[run.dudePause].composer
                    : "This run is paused. Resume it to steer."
                }
                onSubmit={send}
              />
            }
            emptyMessage="Waiting for the agent to start."
          >
            {conversation.turns.map((turn) => renderTurn(turn, role, conversation.contextWindow, !isLive, decide))}
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
                actor={{ type: event.actor.type, id: event.actor.id }}
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

      {problem ? <Callout tone="danger">{problem}</Callout> : null}
      {streamStatus === "reconnecting" ? <Callout tone="attention">Reconnecting…</Callout> : null}
    </div>
  );
}

function renderTurn(turn: Turn, role: AgentRole, contextWindow: number, ended: boolean,
  decide?: (requestId: string, approve: boolean) => void) {
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
          {...(decide && turn.decision === null && !ended
            ? { onChoose: (choice: string) => decide(turn.requestId, choice === "Approve") }
            : {})}
        />
      );
    }
    case "notice":
      return <ChatNotice key={turn.id} data-testid="chat-notice" kind={turn.notice} text={turn.text} at={turn.at} />;
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
    case "human":
      return (
        <ChatMessage
          key={turn.id}
          role="human"
          intent={turn.intent}
          content={turn.text}
          startedAt={turn.at}
          deliveredAt={turn.deliveredAt}
        />
      );
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

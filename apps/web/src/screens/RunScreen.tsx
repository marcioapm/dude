/**
 * The Run screen — the operator's day-to-day view.
 *
 * Opens on the conversation, because that is what someone supervising agents
 * actually reads. The event timeline and logs are available behind a tab:
 * they are debugging tools, reached when something looks wrong, not watched
 * continuously.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  AgentPlan,
  ChatComposer,
  ChatMessage,
  ChatTranscript,
  EventRow,
  EventStream,
  ThinkingBlock,
  ToolCallCard,
  summarizeToolArgs,
} from "@dude/design-system/components";
import { Button, Spinner, Tab, TabList, TabPanel, Tabs } from "@dude/design-system/primitives";
import { DEFAULT_RUN_ROLE, EventTypes, TERMINAL_RUN_STATUSES, runLabel } from "@dude/domain";
import type { AgentRole, PersistedEvent } from "@dude/domain";
import type { ApiClient, RunDetail } from "../api/client.ts";
import { ApiError } from "../api/client.ts";
import { apply, emptyProjection, snapshot, type Turn } from "../api/conversation.ts";
import type { ComposerSubmission } from "@dude/design-system/components";
import { useEventStream } from "../hooks/useEventStream.ts";

export interface RunScreenProps {
  client: ApiClient;
  runId: string;
  /** The work item's title, when the caller already knows it. */
  title?: string | undefined;
  onBack?: (() => void) | undefined;
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

export function RunScreen({ client, runId, title, onBack }: RunScreenProps) {
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
      void intervene(() => client.steer(runId, submission.text), "steer this run"),
    [client, runId, intervene],
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
    workItemId: run.workItemId,
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
                mode="steer"
                disabled={!isLive || run.status === "paused"}
                disabledReason={
                  run.status === "paused"
                    ? "This run is paused. Resume it to steer."
                    : "This run has finished."
                }
                onSubmit={send}
              />
            }
            emptyMessage="Waiting for the agent to start."
          >
            {conversation.turns.map((turn) => renderTurn(turn, role, conversation.contextWindow))}
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

      {problem ? <p className="problem">{problem}</p> : null}
      {streamStatus === "reconnecting" ? <p className="muted">Reconnecting…</p> : null}
      {onBack ? <Button variant="ghost" onClick={onBack}>Back</Button> : null}
    </div>
  );
}

function renderTurn(turn: Turn, role: AgentRole, contextWindow: number) {
  switch (turn.kind) {
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
  return <pre className="payload">{JSON.stringify(payload, null, 2)}</pre>;
}

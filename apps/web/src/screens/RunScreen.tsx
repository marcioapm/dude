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
  ToolCallCard,
  summarizeToolArgs,
} from "@dude/design-system/components";
import { Button, Spinner, Tab, TabList, TabPanel, Tabs } from "@dude/design-system/primitives";
import { EventTypes, TERMINAL_RUN_STATUSES } from "@dude/domain";
import type { PersistedEvent, RunStatus, SessionStatus } from "@dude/domain";
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
  onBack?: () => void;
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
    return snapshot(apply(projection.current, events));
  }, [events, runId]);
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

  const session = {
    id: run.id,
    role: "orchestrator" as const,
    status: SESSION_STATUS_FOR_RUN[run.status],
    // The id is already shown beside the title; repeating it as the title
    // leaves the header saying nothing about the work.
    title: title ? `${title} · attempt ${run.attempt}` : `Attempt ${run.attempt}`,
    workItemId: run.workItemId,
    startedAt: run.startedAt ?? run.createdAt,
    endedAt: run.endedAt,
    costUsd: conversation.costUsd,
    tokens: conversation.tokens,
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
            {conversation.turns.map(renderTurn)}
            {conversation.activity ? (
              <ChatMessage
                role="orchestrator"
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

function renderTurn(turn: Turn) {
  switch (turn.kind) {
    case "message":
      return (
        <ChatMessage
          key={turn.id}
          role="orchestrator"
          content={turn.text}
          startedAt={turn.at}
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
        />
      );
  }
}

/**
 * A Run status as the transcript header's session vocabulary.
 *
 * A Record rather than a switch: the transcript takes a SessionStatus, and
 * keying on the domain union means a new Run status is a compile error here
 * rather than a row that quietly reads "pending".
 */
const SESSION_STATUS_FOR_RUN: Record<RunStatus, SessionStatus> = {
  pending: "pending",
  scheduled: "pending",
  starting: "running",
  running: "running",
  // No session vocabulary for "paused"; awaiting_input is the closest —
  // both mean the agent has stopped and is waiting on a person.
  paused: "awaiting_input",
  completed: "completed",
  failed: "failed",
  aborted: "aborted",
};

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

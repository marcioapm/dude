/**
 * A task's Chat: its conversation with its conductor.
 *
 * Until anyone has written, the task's history so far in one line and an
 * empty composer under it. The first message starts the conductor, briefed
 * by dude; from then on the Chat is the conductors' transcripts in order
 * (RunScreen's, as a chat) — each that ended read-only, the latest live —
 * under the history, and every message goes to the task's conductor, which
 * the orchestrator wakes, or replaces once it ended.
 */

import { useEffect, useMemo, useState } from "react";
import { ChatComposer, ChatTranscript, TaskHistory } from "@dude/design-system/components";
import { Callout } from "@dude/design-system/primitives";
import { firstName, formatUsd } from "@dude/design-system";
import type { Finding, PersistedEvent, PullRequest, RunStatus } from "@dude/domain";
import { ApiError, type ApiClient, type Person, type TaskDetail } from "../api/client.ts";
import { usePeople } from "../people.tsx";
import { taskHistory } from "../taskHistory.ts";
import { EndedConductor, RunScreen, type ChatVariant, type RunCost } from "./RunScreen.tsx";
import type { EndedLedgers } from "./endedLedgers.ts";

export interface ChatSectionProps {
  client: ApiClient;
  task: TaskDetail;
  /** The task's conductor, the latest one if it has had several; null before anyone wrote. */
  conductorId: string | null;
  /** The task's earlier conductors, oldest first: ended, shown read-only above the latest. */
  earlier?: ReadonlyArray<{ id: string; status: RunStatus }> | undefined;
  /** Their ledgers, read once for the task's page. */
  ledgers: EndedLedgers;
  findings: readonly Finding[];
  pullRequests: readonly PullRequest[];
  /** The task's ledger, for what its pull requests heard. */
  events: readonly PersistedEvent[];
  /** The task's owner, as its open session takes it. */
  owner: { owner: Person | null };
  /** Bumped on each reload of the page: the cost is read again. */
  version: number;
  /** A message went: the page reads the task again, to find its conductor. */
  onSent: () => void;
  onBack: () => void;
}

export function ChatSection({ client, task, conductorId, earlier = [], ledgers, findings, pullRequests, events, owner, version, onSent, onBack }: ChatSectionProps) {
  const people = usePeople();
  const [costUsd, setCostUsd] = useState<number | null>(null);
  // Each Run's cost, split as the task's metrics split it: the rail's conductor cost.
  const [runCosts, setRunCosts] = useState<ReadonlyMap<string, RunCost>>(new Map());
  const [problem, setProblem] = useState<string | null>(null);
  useEffect(() => {
    let cancelled = false;
    void client.taskMetrics(task.id).then((m) => {
      if (cancelled) return;
      setCostUsd(m.cost.totalUsd);
      setRunCosts(new Map(m.runs.map((r) => [r.id, { cost: r.cost, tokens: r.tokens.input + r.tokens.output, activeMs: r.activeMs }])));
    }, () => undefined);
    return () => {
      cancelled = true;
    };
  }, [client, task.id, version]);
  const conductorCost = conductorId ? runCosts.get(conductorId) ?? null : null;

  const line = taskHistory(task, findings, pullRequests, costUsd, (usd) => formatUsd(usd));
  const lineKey = [line.lead, ...line.steps, "|", ...line.facts].join("\u0000");
  const head = useMemo(() => (
    <TaskHistory data-testid="chat-history" lead={line.lead} steps={line.steps} facts={line.facts} icon="zap" />
  ), [lineKey]); // eslint-disable-line react-hooks/exhaustive-deps -- the line, by its words

  const send = useMemo(() => async (text: string) => {
    const sent = await client.chat(task.id, text);
    if (sent.runId !== conductorId) onSent();
    return sent;
  }, [client, task.id, conductorId, onSent]);

  const feedback = events.filter((e) => e.eventType === "pull_request.commented").length;
  const open = findings.filter((f) => f.status === "open").length;
  const sessions = task.runs.filter((r) => r.phase).length;
  const checks = pullRequests.map((p) => p.checkState).join(", ");
  const briefedWith = useMemo<ChatVariant["briefedWith"]>(() => [
    { label: "Sessions", value: sessions },
    { label: "Findings", value: findings.length === 0 ? "none" : open === 0 ? `${findings.length} · all settled` : `${findings.length} · ${open} open` },
    ...(pullRequests.length > 0 ? [{ label: "PR comments", value: feedback }, { label: "Checks", value: checks }] : []),
  ], [sessions, findings.length, open, pullRequests.length, feedback, checks]);
  // One object while nothing in it changed: the conductor's transcript is not redrawn for each reload here.
  const earlierKey = earlier.map((r) => r.id).join(",");
  const before = useMemo(() => earlier.length === 0 ? null
    : earlier.map((r) => <EndedConductor key={r.id} ledgers={ledgers} runId={r.id} status={r.status} />),
  [ledgers, earlierKey]); // eslint-disable-line react-hooks/exhaustive-deps -- the conductors, by their ids
  const chat = useMemo<ChatVariant>(() => ({ head, send, briefedWith, before, cost: conductorCost }), [head, send, briefedWith, before, conductorCost]);

  if (conductorId) {
    return (
      <div className="taskChat" data-testid="task-chat">
        <RunScreen key={conductorId} client={client} runId={conductorId} onBack={onBack} task={owner} chat={chat} />
      </div>
    );
  }

  const you = people.you ? people.names.get(people.you) : undefined;
  return (
    <div className="taskChat" data-testid="task-chat">
      <div className="runScreen" data-view="chat">
        <ChatTranscript
          fill
          pinned={head}
          emptyMessage="Nobody has written here yet. Ask about this task: its conductor reads the code and dude's records, and answers. It changes nothing."
          footer={
            <ChatComposer
              mode="chat"
              onSubmit={async ({ text }) => {
                setProblem(null);
                try {
                  await send(text);
                  return true;
                } catch (err) {
                  setProblem(err instanceof ApiError ? `Could not send the message: ${err.message}` : "Could not send the message.");
                  return false;
                }
              }}
              sentAs={you ? firstName(you) : undefined}
              to={<>To <b>Conductor</b> · read-only</>}
            />
          }
        />
        {problem ? <Callout tone="danger">{problem}</Callout> : null}
      </div>
    </div>
  );
}

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
import { ChatComposer, ChatNotice, ChatRunLine, ChatTranscript, DeciderLine, TaskHistory } from "@dude/design-system/components";
import { Button, Callout } from "@dude/design-system/primitives";
import { firstName, formatDuration, formatUsd } from "@dude/design-system";
import { DECISION_POINT_LABEL, isConductor, type Finding, type PersistedEvent, type PullRequest, type Run, type RunStatus } from "@dude/domain";
import { ApiError, type ApiClient, type Person, type TaskDetail } from "../api/client.ts";
import { conductedLines, runWhat } from "../conducted.ts";
import { dudeName } from "../DudeMark.tsx";
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
  /** Open a Run's session: a Run the conductor started, from its line. */
  onOpenRun: (runId: string) => void;
  onBack: () => void;
}

/** A Run's line's facts: how long it took, what it cost. */
function runFacts(run: Run, cost: RunCost | undefined): string[] {
  const facts: string[] = [];
  if (run.startedAt) {
    const ms = Date.parse(run.endedAt ?? new Date().toISOString()) - Date.parse(run.startedAt);
    if (ms > 0) facts.push(formatDuration(ms));
  }
  const usd = cost ? cost.cost.totalUsd : null;
  if (usd !== null && usd > 0) facts.push(formatUsd(usd));
  return facts;
}

export function ChatSection({ client, task, conductorId, earlier = [], ledgers, findings, pullRequests, events, owner, version, onSent, onOpenRun, onBack }: ChatSectionProps) {
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

  const line = taskHistory(task, findings, pullRequests, costUsd, formatUsd);
  const lineKey = [line.lead, ...line.steps, "|", ...line.facts].join("\u0000");
  const head = useMemo(() => (
    <TaskHistory data-testid="chat-history" lead={line.lead} steps={line.steps} facts={line.facts} />
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

  // The Runs the conductor started, decisions waited on, dude's notices.
  const dude = dudeName(task.id);
  const conducted = conductedLines(task, events);
  const linesKey = conducted.map((l) => l.kind === "run" ? `${l.id}:${l.run.status}` : l.id).join(",");
  const lines = useMemo(() => conducted.map((l) => ({
    id: l.id, at: l.at,
    node: l.kind === "run"
      ? <ChatRunLine data-testid="chat-run" data-run={l.run.id} role={l.run.role ?? "implementer"} status={l.run.status}
          what={runWhat(l.run)} facts={runFacts(l.run, runCosts.get(l.run.id))} onOpen={() => onOpenRun(l.run.id)} />
      : <ChatNotice data-testid={l.kind === "decision" ? "chat-decision" : "chat-dude-notice"} kind={l.kind} by={dude} text={l.text} at={l.at} />,
  })), [linesKey, runCosts, dude, onOpenRun]); // eslint-disable-line react-hooks/exhaustive-deps -- the lines, by their ids and statuses

  // Who decides, while a delivery is in progress, and the way to hand it back.
  const [handing, setHanding] = useState(false);
  const inProgress = !["done", "aborted", "failed"].includes(task.status) && task.runs.some((r) => r.phase || isConductor(r));
  const waiting = task.awaitingDecision ? DECISION_POINT_LABEL[task.awaitingDecision.point] : undefined;
  const above = useMemo(() => {
    if (task.decider !== "conductor" || !inProgress) return null;
    const handBack = async () => {
      setHanding(true);
      setProblem(null);
      try {
        await client.setDecider(task.id, "policy");
        onSent();
      } catch (err) {
        setProblem(err instanceof ApiError ? `Could not hand it back: ${err.message}` : "Could not hand it back.");
      } finally {
        setHanding(false);
      }
    };
    return <DeciderLine data-testid="decider-line" decider="conductor" waiting={waiting}
      action={<Button variant="quiet" size="sm" disabled={handing} onClick={() => void handBack()} data-testid="let-deliver-finish">Let Deliver finish it</Button>} />;
  }, [task.decider, task.id, inProgress, waiting, handing, client, onSent]);
  const chat = useMemo<ChatVariant>(() => ({ head, send, briefedWith, before, cost: conductorCost, lines, above }),
    [head, send, briefedWith, before, conductorCost, lines, above]);

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

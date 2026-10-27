/**
 * An automation run's activity, live: its agent's messages, thoughts and
 * tool calls as a task's Run page draws them (transcriptTurns.tsx's
 * renderTurn and asides), the deliveries sent to it where they arrived
 * (FeedDelivery), and a command's or server's output as a LogStream. Read
 * from the run's ledger (events keyed by automation_run_id) over the same
 * events stream RunScreen uses.
 */

import { Fragment, useMemo, type ReactNode } from "react";
import { ChatMessage, ChatAside, ChatTranscript, FeedDelivery, LogStream, summarizeToolArgs, type LogLine } from "@dude/design-system/components";
import { formatDuration, Icon } from "@dude/design-system";
import type { AgentRole, AutomationRunDetail, PersistedEvent } from "@dude/domain";
import type { ApiClient } from "../api/client.ts";
import { project, resultFrom, type ToolTurn, type Turn } from "../api/conversation.ts";
import { useEventStream } from "../hooks/useEventStream.ts";
import { usePeople } from "../people.tsx";
import { asides, interleaved, renderTurn, type Speaker } from "./transcriptTurns.tsx";

/** The ledger's word for a delivery sent to a running run (orchestrator automations.EvFollowUp). */
export const EV_FOLLOW_UP = "automation.delivery.sent";
/** A command's or server's output line, and a command's exit (orchestrator phases.EvLogLine, EvLogExited). */
export const EV_LOG_LINE = "automation.log.line";
export const EV_LOG_EXITED = "automation.log.exited";

/** The run's activity as it arrives: its backfill, then live. */
export function useRunActivity(client: ApiClient, runId: string, agent = true) {
  return useEventStream({ client, automationRunId: runId, limit: agent ? Number.POSITIVE_INFINITY : 2500 });
}

/**
 * The integration square with a zap: an automation's face in its turns'
 * gutter, at the chat avatar's size (or the small one), with the live dot
 * while it runs. Named "Automation".
 */
export function AutomationFace({ size = "chat", live = false }: { size?: "chat" | "sm"; live?: boolean }) {
  return (
    <span className="automationFace" data-size={size} role="img" aria-label="Automation">
      <Icon name="zap" size={size === "chat" ? 14 : 12} />
      {live ? <span className="automationFaceLive ds-live-dot" aria-hidden /> : null}
    </span>
  );
}

/** "Live": the live dot and the word, on the run line and in a log's toolbar. */
export function LiveMark({ children = "Live" }: { children?: ReactNode }) {
  return <span className="liveMark" data-testid="live-mark"><span className="ds-live-dot" aria-hidden />{children}</span>;
}

/** The runs list's second line for a running row: the newest call not finished. */
export function RunNowLine({ tool, args }: { tool: string; args: unknown }) {
  const summary = summarizeToolArgs(args);
  // Generic elements take no accessible name: the full summary is screen-reader text, the visible line is hidden from it.
  return (
    <span className="runNowLine" data-testid="run-now-line">
      <span className="ds-sr-only">{`Now: ${tool}${summary ? `, ${summary}` : ""}`}</span>
      <span aria-hidden>now:</span> <span className="ds-mono runNowTool" aria-hidden>{tool}</span>
      {summary ? <span className="ds-mono runNowArgs" aria-hidden> · {summary}</span> : null}
    </span>
  );
}

/** How many messages and tool calls the activity holds, for the section's hint once the run has ended. */
export function activityCount(turns: readonly Turn[]): string {
  const messages = turns.filter((t) => t.kind === "message").length;
  const calls = turns.filter((t) => t.kind === "tool").length;
  const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? "" : "s"}`;
  return `${plural(messages, "message")} and ${plural(calls, "tool call")}. Kept as long as the run's log.`;
}

/** A call's note in the feed: where a post went, or why the run's end cut it short. */
function feedNote(turn: ToolTurn): ReactNode {
  const posted = turn.dude?.posted;
  if (posted) {
    return (
      <p className="automationToolCallPosted ds-hint" data-testid="posted-link">
        <Icon name="slack" size={12} /> Posted to <b>{posted.channel}</b> ·{" "}
        <a href={posted.permalink} target="_blank" rel="noreferrer">Open the message in Slack</a>
      </p>
    );
  }
  if (turn.status === "aborted" && turn.stoppedNote) {
    const ran = turn.endedAt ? Date.parse(turn.endedAt) - Date.parse(turn.startedAt) : 0;
    // The call's duration goes after the first clause: "Stopped by the time limit after 27m 14s, when …".
    const [head, ...rest] = turn.stoppedNote.replace(/\.$/, "").split(", ");
    return (
      <p className="automationStoppedNote" data-testid="stopped-note">
        <Icon name="stop" size={12} /> {head}{ran > 0 ? ` after ${formatDuration(ran)}` : ""}{rest.length ? `, ${rest.join(", ")}` : ""}.
      </p>
    );
  }
  return undefined;
}

/**
 * The agent's activity as a headerless transcript. Live, it is its own
 * scroller filling the window under it, following the tail (ChatTranscript's
 * "N new turns · Jump to latest" when the person scrolls up); ended, it flows
 * with the page as the record.
 */
export function AutomationFeed({ run, events, name, role, live, onDelivery }: {
  run: AutomationRunDetail;
  events: readonly PersistedEvent[];
  name: string;
  role: AgentRole;
  live: boolean;
  onDelivery: (id: string) => void;
}) {
  const people = usePeople();
  const status = live ? "running" : run.status === "succeeded" ? "completed" : run.status === "cancelled" ? "aborted" : "failed";
  const conversation = useMemo(() => project(events, status), [events, status]);
  // A call still running shows the output the orchestrator holds for it so far (run.live, read with the run's facts).
  const liveOutput = live ? run.live?.output ?? {} : {};
  const turns = useMemo(() => conversation.turns.filter((t) => t.kind !== "usage").map((t) => {
    if (t.kind !== "tool") return t;
    if (t.status === "running" && liveOutput[t.id]) return { ...t, result: resultFrom(liveOutput[t.id]!) };
    // A call that exited non-zero is failed — opened, in danger — whatever the harness called it.
    return t.status === "completed" && (t.result?.exitCode ?? 0) !== 0 ? { ...t, status: "failed" as const } : t;
  }), [conversation, liveOutput]);
  const groups = useMemo(() => asides(turns), [turns]);
  const sent = useMemo(() => events.filter((e) => e.eventType === EV_FOLLOW_UP).map((e) => {
    const id = String(e.payload.deliveryId ?? "");
    return {
      id: e.eventId, at: e.occurredAt,
      node: (
        <ChatAside key={e.eventId}>
          <FeedDelivery at={e.occurredAt} deliveryId={id} summary={String(e.payload.summary ?? e.payload.event ?? "")}
            body={run.sentBodies?.[id] ?? undefined} source={e.payload.source === "github" ? "github" : "incoming"}
            onOpen={() => onDelivery(id)} />
        </ChatAside>
      ),
    };
  }), [events, run.sentBodies, onDelivery]);
  const speaker: Speaker = { name, roleLabel: "Automation", avatar: <AutomationFace live={live} /> };
  // The unfinished reply read with the run is stale once a finished message arrived after that read.
  const liveCursor = run.live?.cursor;
  const streaming = live && run.live?.message && !(liveCursor !== undefined
    && events.some((e) => e.eventType === "agent.message" && e.cursor > liveCursor)) ? run.live.message : "";
  const render = (turn: Turn) => renderTurn(turn, role, conversation.contextWindow, !live, people, "dude",
    undefined, undefined, undefined, undefined, feedNote, undefined, undefined, undefined, speaker);
  return (
    <ChatTranscript className="automationFeed" fill={live} maxHeight="none" live={live} revision={events.length} turns={conversation.turns.length + sent.length}
      data-testid="run-feed" data-live={live || undefined}
      emptyMessage={live ? "Starting…" : "This run recorded no activity."}>
      {interleaved(groups, sent).map((item) => "node" in item
        ? <Fragment key={item.id}>{item.node}</Fragment>
        : Array.isArray(item.group) ? <ChatAside key={item.group[0]!.id}>{item.group.map(render)}</ChatAside> : render(item.group))}
      {streaming ? <ChatMessage role={role} name={name} roleLabel="Automation" avatar={speaker.avatar}
        content={streaming} streaming data-testid="automation-streaming" /> : live && conversation.activity ? <ChatMessage role={role} name={name} roleLabel="Automation" avatar={speaker.avatar}
        activity={conversation.activity} data-testid="automation-activity"
        activityProps={conversation.activeTool ? { label: conversation.activeTool.name, since: conversation.activeTool.since } : undefined} /> : null}
    </ChatTranscript>
  );
}

/** A command's or server's lines, in the LogStream's shape. */
export function logLines(events: readonly PersistedEvent[], server: boolean): { lines: LogLine[]; exitCode: number | null } {
  const lines: LogLine[] = [];
  let exitCode: number | null = null;
  for (const e of events) {
    if (e.eventType === EV_LOG_EXITED && typeof e.payload.exitCode === "number") exitCode = e.payload.exitCode;
    if (e.eventType === "automation.log.cut") {
      lines.push({ seq: e.cursor, text: String(e.payload.text ?? "Log lines omitted"), channel: "stdout" });
      continue;
    }
    if (e.eventType !== EV_LOG_LINE || Boolean(e.payload.server) !== server) continue;
    const stderr = e.payload.stream === "stderr";
    lines.push({ seq: e.cursor, text: String(e.payload.text ?? ""), ts: e.occurredAt, channel: stderr ? "stderr" : "stdout",
      ...(stderr ? { level: "error" as const } : {}) });
  }
  return { lines, exitCode };
}

/** A command's output, live: line numbers, following, the live mark while it runs and its exit code when it ends. */
export function CommandLog({ command, events, live }: { command: string; events: readonly PersistedEvent[]; live: boolean }) {
  const { lines, exitCode } = useMemo(() => logLines(events, false), [events]);
  return (
    <LogStream data-testid="command-log" lines={lines} lineNumbers live={live} maxHeight={480}
      title={<span className="ds-mono logTitle"><Icon name="terminal" size={12} /> {command}</span>}
      toolbar={<span className="logToolbar">{live ? <LiveMark>Running</LiveMark> : exitCode !== null
        ? <span className={exitCode === 0 ? "logExit" : "logExit logExitBad"} data-testid="command-exit">exit {exitCode}</span> : null}</span>}
      emptyMessage={live ? "Waiting for output…" : "It printed nothing."} />
  );
}

/** A server's container log, with timestamps, under its timeline. */
export function ContainerLog({ image, events, live }: { image: string; events: readonly PersistedEvent[]; live: boolean }) {
  const { lines } = useMemo(() => logLines(events, true), [events]);
  return (
    <LogStream data-testid="container-log" lines={lines} timestamps lineNumbers={false} live={live} maxHeight={480}
      title={<span className="ds-mono logTitle"><Icon name="layers" size={12} /> {image}</span>}
      toolbar={live ? <LiveMark /> : undefined}
      emptyMessage={live ? "Waiting for the container's output…" : "Its container printed nothing."} />
  );
}

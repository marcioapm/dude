/**
 * A tool call's child sessions under its card: one ChatThread each (role
 * subagent), with the child's own calls and messages inside, and while one
 * waits on a permission nothing in the Run can answer, that said three
 * times — the thread's header, a note on its stuck call, a notice.
 */

import { Fragment, type ReactNode } from "react";
import { ChatMessage, ChatNotice, ChatThread, ToolCallCard } from "@dude/design-system/components";
import type { ChildTurn, ToolTurn } from "./api/conversation.ts";

/** The permission a child waits on, as its notice says it. */
function waitingText(waitingOn: NonNullable<ChildTurn["waitingOn"]>): string {
  const patterns = waitingOn.patterns.length > 0 ? ` for ${waitingOn.patterns.join(", ")}` : "";
  return `Waiting on a permission: ${waitingOn.permission}${patterns}. Nothing in this Run can answer it.`;
}

/** A tool call's card, and under it the threads of the children it started, at depth. */
export function ToolWithChildren({ turn, depth, note }: { turn: ToolTurn; depth: number; note?: ReactNode }) {
  return (
    <>
      <ToolCallCard
        name={turn.tool}
        args={turn.args}
        status={turn.status}
        startedAt={turn.startedAt}
        endedAt={turn.endedAt}
        output={turn.result?.output ?? turn.result?.stdout}
        stderr={turn.result?.stderr}
        exitCode={turn.result?.exitCode}
        note={note}
      />
      {turn.children.map((child) => <ChildThread key={child.id} child={child} depth={depth} />)}
    </>
  );
}

function ChildThread({ child, depth }: { child: ChildTurn; depth: number }) {
  const ended = child.status !== "running";
  const lastMessage = [...child.turns].reverse().find((t) => t.kind === "message");
  const summary = child.summary ?? (lastMessage?.kind === "message" ? lastMessage.text : undefined);
  const waiting = child.waitingOn;
  // The call it is stuck in: its last one still running.
  const stuck = waiting ? [...child.turns].reverse().find((t) => t.kind === "tool" && t.status === "running")?.id : undefined;
  return (
    <ChatThread
      // Its end folds it, as a child already done is drawn: keyed on it.
      key={ended ? "ended" : "live"}
      data-testid="child-thread"
      data-child={child.id}
      sessionId={child.id}
      role="subagent"
      status={child.status}
      model={child.model ?? undefined}
      task={child.agentType ? `${child.title} · ${child.agentType}` : child.title}
      startedAt={child.startedAt}
      endedAt={child.endedAt}
      costUsd={child.costUsd ?? undefined}
      turnCount={child.turns.length}
      summary={summary ? firstLine(summary) : undefined}
      depth={depth}
      defaultCollapsed={ended}
      waiting={waiting ? { on: "a permission", since: waiting.since, detail: `${waiting.permission}${waiting.patterns.length ? ` for ${waiting.patterns.join(", ")}` : ""}` } : undefined}
    >
      {child.turns.map((turn) => turn.kind === "tool"
        ? (
          <Fragment key={turn.id}>
            <ToolWithChildren turn={turn} depth={depth + 1}
              note={turn.id === stuck && waiting ? <>Waiting on <code>{waiting.permission}</code>{waiting.patterns.length ? <> for <code>{waiting.patterns.join(", ")}</code></> : null}: nothing in this Run can allow or refuse it.</> : undefined} />
          </Fragment>
        )
        : <ChatMessage key={turn.id} role="subagent" model={child.model ?? undefined} content={turn.text} startedAt={turn.at} />)}
      {waiting ? <ChatNotice data-testid="child-waiting-notice" kind="notice" at={waiting.since} text={waitingText(waiting)} /> : null}
    </ChatThread>
  );
}

function firstLine(text: string): string {
  return text.split("\n").find((l) => l.trim())?.trim() ?? text;
}

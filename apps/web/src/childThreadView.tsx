/**
 * One child's subagent thread, unmemoised: its header, its body, and the
 * dialog its Open action shows. `childThreads.tsx` memoises it.
 */

import { useState } from "react";
import { ChatThread } from "@dude/design-system/components";
import { Dialog } from "@dude/design-system/primitives";
import type { ChildTurn } from "./api/conversation.ts";
import { ChildBody } from "./childBody.tsx";

export interface ChildThreadProps {
  child: ChildTurn;
  revision: number;
  settled: boolean;
  depth: number;
}

export function ChildThreadView({ child, depth }: ChildThreadProps) {
  const [open, setOpen] = useState(false);
  const ended = child.status !== "running";
  const lastMessage = child.summary === null ? child.turns.findLast((t) => t.kind === "message") : undefined;
  const summary = child.summary ?? (lastMessage?.kind === "message" ? lastMessage.text : undefined);
  const waiting = child.waitingOn;
  const task = child.agentType ? `${child.title} · ${child.agentType}` : child.title;
  return (
    <>
      <ChatThread
        // Its end folds it, as a child already done is drawn: keyed on it.
        key={ended ? "ended" : "live"}
        data-testid="child-thread"
        data-child={child.id}
        sessionId={child.id}
        role="subagent"
        status={child.status}
        model={child.model ?? undefined}
        task={task}
        startedAt={child.startedAt}
        endedAt={child.endedAt}
        costUsd={child.costUsd ?? undefined}
        turnCount={child.turns.length}
        summary={summary ? firstLine(summary) : undefined}
        depth={depth}
        // The design system's depth rule (depth 2 starts collapsed), and a done child folds.
        defaultCollapsed={ended || depth >= 2}
        onOpen={() => setOpen(true)}
        waiting={waiting ? { on: "a permission", since: waiting.since, detail: `${waiting.permission}${waiting.patterns.length ? ` for ${waiting.patterns.join(", ")}` : ""}` } : undefined}
      >
        <ChildBody child={child} depth={depth} />
      </ChatThread>
      {open ? (
        <Dialog open onOpenChange={setOpen} size="xl" context="Subagent" title={task}>
          <div data-testid="child-session" data-child={child.id}>
            <ChildBody child={child} depth={0} />
          </div>
        </Dialog>
      ) : null}
    </>
  );
}

function firstLine(text: string): string {
  return text.split("\n").find((l) => l.trim())?.trim() ?? text;
}

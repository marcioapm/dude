import { ChatThread } from "@dude/design-system/components";
import type { ChildTurn } from "./api/conversation.ts";

export interface ChildThreadProps {
  child: ChildTurn;
  revision: number;
  settled: boolean;
  depth: number;
  onOpen?: ((id: string) => void) | undefined;
}

export function childTitle(child: ChildTurn): string {
  return child.agentType ? `${child.title} · ${child.agentType}` : child.title;
}

export function childSummary(child: ChildTurn): string | undefined {
  const last = child.turns.findLast((t) => t.kind === "message");
  const text = child.summary ?? (last?.kind === "message" ? last.text : undefined);
  return text?.split("\n").find((line) => line.trim())?.trim();
}

export function ChildThreadView({ child, depth, onOpen }: ChildThreadProps) {
  const waiting = child.waitingOn;
  const lastTool = child.turns.findLast((t) => t.kind === "tool");
  return (
    <ChatThread
      data-testid="child-thread"
      data-child={child.id}
      sessionId={child.id}
      role="subagent"
      status={child.status}
      model={child.model ?? undefined}
      task={childTitle(child)}
      startedAt={child.startedAt}
      endedAt={child.endedAt}
      costUsd={child.costUsd ?? undefined}
      turnCount={child.turns.length}
      summary={childSummary(child)}
      depth={depth}
      collapsed
      openOnly
      activity={child.status === "running" && lastTool?.kind === "tool" && lastTool.status === "running" ? "tool" : undefined}
      activityProps={lastTool?.kind === "tool" ? { tool: lastTool.tool, since: lastTool.startedAt } : undefined}
      onOpen={onOpen}
      waiting={waiting ? { on: "a permission", since: waiting.since } : undefined}
    />
  );
}

/** Folded child rows under their spawning calls, and the rail's quicklinks. */

import { createContext, memo, useContext, type ReactNode } from "react";
import { AgentAvatar, Duration, SessionRailBlock, ToolCallCard } from "@dude/design-system/components";
import { childSummary, ChildThreadView, type ChildThreadProps } from "./childThreadView.tsx";
import type { ChildTurn, ToolTurn, Turn } from "./api/conversation.ts";

export const ChildOpenContext = createContext<((id: string) => void) | undefined>(undefined);

export function childEntries(turns: readonly (Turn | ChildTurn["turns"][number])[]): Array<{ child: ChildTurn; call: ToolTurn }> {
  return turns.flatMap((turn) => turn.kind === "tool" ? turn.children.flatMap((child) => [
    { child, call: turn }, ...childEntries(child.turns),
  ]) : []);
}

export function ChildStatus({ child }: { child: ChildTurn }) {
  const waiting = child.status === "running" ? child.waitingOn : null;
  const tool = child.turns.findLast((turn) => turn.kind === "tool");
  return <span style={{ color: waiting ? "var(--ds-tone-attention-fg)" : "var(--ds-color-text-muted)" }}>
    {waiting ? <>waiting <Duration since={waiting.since} live /> on a permission</>
      : child.status === "running" ? `running${tool?.kind === "tool" ? ` · ${tool.tool}` : " · thinking"}`
      : `${child.status === "completed" ? "done" : child.status} · ${childSummary(child) ?? ""}`}
  </span>;
}

export function SubagentsBlock({ turns, onOpen }: { turns: readonly Turn[]; onOpen: (id: string) => void }) {
  const entries = childEntries(turns);
  if (!entries.length) return null;
  const children = entries.filter(({ child }) => child.parentChildId === null).map(({ child }) => child)
    .sort((a, b) => Date.parse(a.startedAt) - Date.parse(b.startedAt));
  return <SessionRailBlock label={`Subagents · ${entries.length}`} live={entries.some(({ child }) => child.status === "running")} data-testid="rail-subagents">
    {children.map((child) => <button key={child.id} type="button" data-child={child.id} onClick={() => onOpen(child.id)}
      style={{ display: "flex", alignItems: "center", gap: 8, width: "100%", padding: "6px 8px", border: 0, background: "transparent", textAlign: "left", color: "var(--ds-color-text-primary)" }}>
      <AgentAvatar role="subagent" size="xs" live={child.status === "running"} />
      <span style={{ display: "flex", flexDirection: "column", minWidth: 0, fontSize: "var(--ds-text-xs)" }}>
        <span title={child.title} style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{child.title}</span>
        <ChildStatus child={child} />
      </span>
    </button>)}
  </SessionRailBlock>;
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
      {turn.children.map((child) => <ChildThread key={child.id} child={child} revision={child.revision} settled={child.settled} depth={depth} />)}
    </>
  );
}

// The projection mutates a child in place and bumps its revision (and its
// ancestors') on every change: a sibling whose revision is unchanged is
// skipped. A terminal Run's settled copy keeps the live revision, so
// `settled` tells the two apart.
function sameChild(a: ChildThreadProps, b: ChildThreadProps): boolean {
  return a.child.id === b.child.id && a.revision === b.revision && a.settled === b.settled && a.depth === b.depth;
}

const ChildThread = memo(function ChildThread(props: ChildThreadProps) {
  const onOpen = useContext(ChildOpenContext);
  return <ChildThreadView {...props} onOpen={onOpen} />;
}, sameChild);

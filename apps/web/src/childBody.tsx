/** A child's own calls and messages, its children's threads one deeper than `depth`. */

import type { ReactElement } from "react";
import { ChatMessage, ChatNotice } from "@dude/design-system/components";
import type { ChildTurn } from "./api/conversation.ts";
import { ToolWithChildren } from "./childThreads.tsx";

function waitingText(waitingOn: NonNullable<ChildTurn["waitingOn"]>): string {
  const patterns = waitingOn.patterns.length > 0 ? ` for ${waitingOn.patterns.join(", ")}` : "";
  return `Waiting on a permission: ${waitingOn.permission}${patterns}. Nothing in this Run can answer it.`;
}

interface ChildBodyProps {
  child: ChildTurn;
  depth: number;
}

export function ChildBody({ child, depth }: ChildBodyProps): ReactElement {
  const waiting = child.waitingOn;
  // The call it is stuck in: its last one still running.
  const stuck = waiting ? child.turns.findLast((t) => t.kind === "tool" && t.status === "running")?.id : undefined;
  return (
    <>
      {child.turns.map((turn) => turn.kind === "tool"
        ? (
          <ToolWithChildren key={turn.id} turn={turn} depth={depth + 1}
            note={turn.id === stuck && waiting ? <>Waiting on <code>{waiting.permission}</code>{waiting.patterns.length ? <> for <code>{waiting.patterns.join(", ")}</code></> : null}: nothing in this Run can allow or refuse it.</> : undefined} />
        )
        : <ChatMessage key={turn.id} role="subagent" model={child.model ?? undefined} content={turn.text} startedAt={turn.at} />)}
      {waiting ? <ChatNotice data-testid="child-waiting-notice" kind="notice" at={waiting.since} text={waitingText(waiting)} /> : null}
    </>
  );
}

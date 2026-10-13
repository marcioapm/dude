/**
 * A tool call's child sessions under its card: one ChatThread each (role
 * subagent), with the child's own calls and messages inside, and while one
 * waits on a permission nothing in the Run can answer, that said three
 * times — the thread's header, a note on its stuck call, a notice.
 *
 * Every thread has an Open action showing the child alone in a dialog: a
 * child session has no route of its own, and at depth 3+ the thread is
 * header-only, so the dialog is the only way to read it there.
 */

import { memo, type ReactNode } from "react";
import { ToolCallCard } from "@dude/design-system/components";
import type { ToolTurn } from "./api/conversation.ts";
import { ChildThreadView, type ChildThreadProps } from "./childThreadView.tsx";

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
  return <ChildThreadView {...props} />;
}, sameChild);

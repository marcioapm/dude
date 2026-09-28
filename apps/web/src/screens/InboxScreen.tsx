/**
 * Waiting on you, then on others: every task that needs a person, across
 * projects, oldest wait first. Yours are what you can answer — your tasks,
 * and tasks nobody owns — and are the loud list; others' are listed
 * quietly with whom they wait on, and Take over makes one yours. Each row
 * opens where the answer is given: the asking agent's chat, or the task.
 */

import { useMemo } from "react";
import { AttentionList, attentionItems, splitAttention, type NavProject, type NavRef, type NavRow } from "@dude/design-system";
import { EmptyState, Page, PageHeader } from "@dude/design-system/primitives";

export function InboxScreen({ projects, you, selected, onSelect, onTakeOver }: {
  projects: ReadonlyArray<NavProject>;
  /** Your person id; unknown yet, everything is yours. */
  you: string | undefined;
  selected: NavRef | null;
  onSelect: (ref: NavRef, node: NavRow["node"]) => void;
  onTakeOver: (taskId: string) => void;
}) {
  const { yours, others } = useMemo(() => splitAttention(attentionItems(projects, you)), [projects, you]);
  return (
    <Page data-testid="inbox">
      <PageHeader title="Waiting on you" />
      {yours.length === 0 ? (
        <EmptyState title="Nothing is waiting on you" description="When an agent asks you something, or a delivery of yours needs a decision, it shows here." />
      ) : (
        <div data-testid="inbox-yours">
          <AttentionList title="Yours · oldest first" items={yours} selected={selected} max={Infinity} onSelect={onSelect} />
        </div>
      )}
      {others.length > 0 ? (
        <div data-testid="inbox-others">
          <AttentionList title="Waiting on others" others items={others} selected={selected} max={Infinity} onSelect={onSelect}
            onTakeOver={(it) => onTakeOver(it.task.id)} />
        </div>
      ) : null}
    </Page>
  );
}

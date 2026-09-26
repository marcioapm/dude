/**
 * Waiting on you: every task that needs a person, across projects,
 * oldest wait first — the sidebar's "Needs you", in full. Each row opens
 * where the answer is given: the asking agent's chat, or the task.
 */

import { useMemo } from "react";
import { AttentionList, attentionItems, type NavProject, type NavRef, type NavRow } from "@dude/design-system";
import { EmptyState, Page, PageHeader } from "@dude/design-system/primitives";

export function InboxScreen({ projects, selected, onSelect }: {
  projects: ReadonlyArray<NavProject>;
  selected: NavRef | null;
  onSelect: (ref: NavRef, node: NavRow["node"]) => void;
}) {
  const items = useMemo(() => attentionItems(projects), [projects]);
  return (
    <Page data-testid="inbox">
      <PageHeader title="Waiting on you" />
      {items.length === 0 ? (
        <EmptyState title="Nothing is waiting on you" description="When an agent asks something, or a delivery needs a decision, it shows here." />
      ) : (
        <AttentionList title="Oldest wait first" items={items} selected={selected} max={Infinity} onSelect={onSelect} />
      )}
    </Page>
  );
}

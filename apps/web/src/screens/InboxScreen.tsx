/**
 * Waiting on you, then on others: everything that needs a person, across
 * projects, oldest wait first.
 *
 * Only what is yours is highlighted and counted: a task you own (or that
 * nobody owns, so anyone may act). Others' are listed with their face,
 * and "Take over" makes the task yours — the same reassignment the task
 * page offers — so a colleague's stuck question need not wait for them.
 */

import { useMemo, useState } from "react";
import { attentionItems, taskOwner, toMs, useNow, waitingSplit, waitingWords, type AttentionItem, type NavProject, type NavRef } from "@dude/design-system";
import { Duration, PersonAvatar, ProjectAvatar, ScreenHeader, WaitingGroup, WaitingRow } from "@dude/design-system/components";
import { Button, EmptyState, useToast } from "@dude/design-system/primitives";
import type { ApiClient } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";
import { firstName } from "@dude/design-system";
import { usePeople } from "../people.tsx";

export function InboxScreen({ client, projects, onSelect, onChanged }: {
  client: ApiClient;
  projects: ReadonlyArray<NavProject>;
  onSelect: (ref: NavRef) => void;
  /** Something was taken over: the tree re-reads. */
  onChanged: () => void;
}) {
  const people = usePeople();
  const { yours, others } = useMemo(() => waitingSplit(attentionItems(projects), people.you), [projects, people.you]);
  const now = useNow(true, 60_000);
  const { toast } = useToast();
  const [taking, setTaking] = useState<string | null>(null);

  const open = (it: AttentionItem) => onSelect(it.session ? { kind: "session", id: it.session.id } : { kind: "task", id: it.task.id });
  const takeOver = async (it: AttentionItem) => {
    if (!people.you) return;
    setTaking(it.task.id);
    try {
      await client.reassignTask(it.task.id, people.you);
      toast({ title: `${it.task.key ?? it.task.title} is yours now`, tone: "success" });
      onChanged();
    } catch (err) {
      toast({ title: `Could not take it over: ${errorText(err)}`, tone: "danger" });
    } finally {
      setTaking(null);
    }
  };

  const row = (it: AttentionItem, mine: boolean) => {
    const owner = taskOwner(it.task);
    const since = toMs(it.task.statusSince);
    const ask = it.session ? (it.session.activity ?? "is waiting for you") : waitingWords(it.task);
    const whose = mine ? (it.session ? "your agent is waiting" : "decide how it goes on") : owner ? `${firstName(owner.name)}'s task` : "nobody's task";
    return (
      <WaitingRow
        key={it.task.id}
        mine={mine}
        onOpen={() => open(it)}
        face={owner ? <PersonAvatar person={people.byId.get(owner.id ?? "") ?? owner} size={40} {...(it.session ? { agent: it.session.role } : {})} ring={!mine} /> : null}
        ask={ask}
        whereTitle={it.epic ? `${it.project.name} · ${it.epic.title}` : it.project.name}
        where={<><ProjectAvatar project={it.project} size={16} aria-hidden title={undefined} />{[it.task.key, it.task.title, whose].filter(Boolean).join(" · ")}</>}
        age={since !== null ? <Duration ms={Math.max(0, now - since)} format="age" tone="muted" /> : null}
        action={mine ? (
          <Button size="sm" variant="primary" onClick={() => open(it)}>
            {it.session ? "Answer" : "Decide"}
          </Button>
        ) : (
          <Button size="sm" variant="quiet" disabled={!people.you || taking === it.task.id} onClick={() => void takeOver(it)}
            title="Make this task yours: you answer for it from now on">
            Take over
          </Button>
        )}
      />
    );
  };

  return (
    <div className="screen" data-testid="inbox">
      <ScreenHeader title="Waiting on you" />
      <div className="screenBody narrow">
        {yours.length + others.length === 0 ? (
          <EmptyState title="Nothing is waiting on you" description="When an agent asks something, or a delivery needs a decision, it shows here." />
        ) : (
          <>
            <WaitingGroup title="Yours" count={yours.length} empty="Nothing of yours. Others' are below.">
              {yours.map((it) => row(it, true))}
            </WaitingGroup>
            {others.length > 0 ? (
              <WaitingGroup title="Waiting on others" count={others.length}>
                {others.map((it) => row(it, false))}
              </WaitingGroup>
            ) : null}
          </>
        )}
      </div>
    </div>
  );
}

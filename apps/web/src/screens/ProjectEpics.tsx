/**
 * A project's epics by state, above its board: those in progress as cards
 * — how far along by lane, their pull requests, who drives them and what
 * they cost — and planned and done ones as quiet lists side by side. An
 * epic's state is what its tasks say until a person sets it here.
 */

import { useCallback, useEffect, useState } from "react";
import { EpicCard, EpicRow, type EpicSummary } from "@dude/design-system/components";
import { formatTimestamp, formatUsd, plural } from "@dude/design-system";
import { Callout, RowMenu, Section, useToast } from "@dude/design-system/primitives";
import { EPIC_STATES, type EpicState } from "@dude/domain";
import type { ApiClient, EpicOverview } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";

const STATE_LABEL: Record<EpicState, string> = { planned: "Planned", active: "In progress", done: "Done" };

function summary(e: EpicOverview, when: string): EpicSummary {
  return {
    id: e.id,
    title: e.title,
    description: e.description || undefined,
    lanes: e.lanes,
    tasks: e.tasks,
    prs: e.prs,
    people: e.owners,
    costUsd: e.costUsd,
    machineUsd: e.machineUsd,
    when,
    needsYou: e.needsYou,
  };
}

export function ProjectEpics({ client, projectId, version, onOpenEpic }: {
  client: ApiClient;
  projectId: string;
  /** Bumped when the tree reloads: something changed. */
  version: number;
  onOpenEpic: (epicId: string) => void;
}) {
  const [epics, setEpics] = useState<EpicOverview[] | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const { toast } = useToast();
  const load = useCallback(
    () => client.projectOverview(projectId).then((o) => setEpics(o.epics), (err: unknown) => setProblem(errorText(err))),
    [client, projectId],
  );
  useEffect(() => {
    void load();
  }, [load, version]);

  if (problem) return <Callout tone="danger">{problem}</Callout>;
  if (!epics?.length) return null;

  const setState = (e: EpicOverview, state: EpicState | null) =>
    void client.updateEpic(e.id, { state }).then(
      () => load(),
      (err: unknown) => toast({ title: errorText(err), tone: "danger" }),
    );
  const menu = (e: EpicOverview) => (
    <RowMenu
      label={`State of ${e.title}`}
      size="sm"
      items={[
        ...EPIC_STATES.map((s) => ({
          id: s,
          label: STATE_LABEL[s],
          icon: s === e.state ? ("check" as const) : undefined,
          disabled: s === e.state && e.stateSet,
          disabledReason: "It is already",
          onSelect: () => setState(e, s),
        })),
        { kind: "separator" as const },
        {
          id: "auto",
          label: "As its tasks say",
          disabled: !e.stateSet,
          disabledReason: "It already follows its tasks",
          onSelect: () => setState(e, null),
        },
      ]}
    />
  );

  const active = epics.filter((e) => e.state === "active");
  const planned = epics.filter((e) => e.state === "planned");
  const done = epics.filter((e) => e.state === "done");
  const updated = (e: EpicOverview) => `updated ${formatTimestamp(e.lastActivity ?? e.updatedAt, "relative")}`;
  return (
    <div data-testid="project-epics" className="projectEpics">
      {active.length ? (
        <Section title="In progress" count={active.length} data-epic-state="active">
          <div className="epicCards">
            {active.map((e) => (
              <EpicCard key={e.id} epic={summary(e, updated(e))} onOpen={() => onOpenEpic(e.id)} actions={menu(e)} />
            ))}
          </div>
        </Section>
      ) : null}
      {planned.length || done.length ? (
        <div className="epicLists">
          <Section title="Planned" count={planned.length} data-epic-state="planned">
            {planned.map((e) => (
              <EpicRow key={e.id} epic={summary(e, e.tasks ? "not started" : "")} onOpen={() => onOpenEpic(e.id)}
                detail={e.tasks ? plural(e.tasks, "task") : e.description || "no tasks yet"} actions={menu(e)} />
            ))}
          </Section>
          <Section title="Done" count={done.length} data-epic-state="done">
            {done.map((e) => (
              <EpicRow key={e.id} epic={summary(e, `finished ${formatTimestamp(e.lastActivity ?? e.updatedAt, "relative")}`)}
                onOpen={() => onOpenEpic(e.id)}
                detail={`${plural(e.tasks, "task")} · ${formatUsd(e.costUsd + e.machineUsd)}`} actions={menu(e)} />
            ))}
          </Section>
        </div>
      ) : null}
    </div>
  );
}

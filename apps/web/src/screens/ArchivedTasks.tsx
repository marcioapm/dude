/**
 * A project's archived work, opened from the board's quiet "N archived": its
 * archived tasks (`GET /v1/tasks?archived=1`) and epics, newest first, each
 * openable, and each with Unarchive for whoever may. The tasks list's
 * Archived view replaces this when the list is built (tracking-work.md,
 * "The tasks list", Epic 3).
 */

import { useCallback, useEffect, useState } from "react";
import { ArchivedMark, ResolutionMark } from "@dude/design-system/components";
import { Button, Callout, Section } from "@dude/design-system/primitives";
import { StatusMark } from "@dude/design-system/components";
import type { ApiClient, ProjectOverview, Task } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";

export function ArchivedTasks({ client, projectId, version, onOpen }: {
  client: ApiClient;
  projectId: string;
  version: number;
  onOpen: (taskId: string) => void;
}) {
  const [tasks, setTasks] = useState<Task[] | null>(null);
  const [epics, setEpics] = useState<NonNullable<ProjectOverview["archivedEpics"]>>([]);
  const [problem, setProblem] = useState<string | null>(null);
  const load = useCallback(() => {
    void client.archivedTasks(projectId).then((r) => setTasks(r.tasks), (err: unknown) => setProblem(errorText(err)));
    void client.projectOverview(projectId).then((o) => setEpics(o.archivedEpics ?? []), () => undefined);
  }, [client, projectId]);
  useEffect(load, [load, version]);
  const act = (fn: () => Promise<unknown>) => {
    setProblem(null);
    fn().then(load, (err: unknown) => setProblem(errorText(err)));
  };
  return (
    <Section title="Archived" data-testid="archived-list">
      {problem ? <Callout tone="danger">{problem}</Callout> : null}
      {tasks === null ? null : tasks.length === 0 && epics.length === 0 ? <p className="muted">Nothing archived.</p> : (
        <ul className="archivedList">
          {epics.map((e) => (
            <li key={e.id} data-testid="archived-epic">
              <span className="archivedLead">Epic</span>
              <span className="archivedTitle">{e.title}</span>
              <ArchivedMark />
              <Button size="sm" variant="quiet" onClick={() => act(() => client.unarchiveEpic(e.id))}>Unarchive</Button>
            </li>
          ))}
          {tasks.map((t) => (
            <li key={t.id} data-testid="archived-task">
              <StatusMark status={t.status} size="sm" />
              <span className="ds-mono">{t.key}</span>
              <button type="button" className="archivedTitle linkish" onClick={() => onOpen(t.id)}>{t.title}</button>
              {t.resolution && t.resolution !== "nothing_to_merge" ? <ResolutionMark resolution={t.resolution} of={t.duplicateOf ?? undefined} /> : null}
              <ArchivedMark />
              <Button size="sm" variant="quiet" onClick={() => act(() => client.unarchiveTask(t.id))} data-testid="unarchive-row">Unarchive</Button>
            </li>
          ))}
        </ul>
      )}
    </Section>
  );
}

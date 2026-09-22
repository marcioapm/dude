/**
 * The shell: work item list on the left, the selected Run on the right.
 *
 * A placeholder for the sidebar being designed separately — this is enough
 * navigation to reach a Run and drive it, and it will be replaced by the real
 * project/epic/work-item tree when that lands.
 */

import { useCallback, useEffect, useState } from "react";
import { StatusBadge } from "@dude/design-system/components";
import { Button, EmptyState } from "@dude/design-system/primitives";
import type { ApiClient, Project, WorkItem } from "./api/client.ts";
import { RunScreen } from "./screens/RunScreen.tsx";

export interface AppProps {
  client: ApiClient;
  onSignOut: () => void;
}

export function App({ client, onSignOut }: AppProps) {
  const [projects, setProjects] = useState<Project[]>([]);
  const [workItems, setWorkItems] = useState<WorkItem[]>([]);
  const [active, setActive] = useState<{ runId: string; title: string } | null>(null);
  const [problem, setProblem] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;

    void (async () => {
      try {
        const [{ projects: found }, { workItems: items }] = await Promise.all([
          client.listProjects(),
          client.listWorkItems(),
        ]);
        if (cancelled) return;
        setProjects(found);
        setWorkItems(items);
      } catch (err) {
        if (!cancelled) setProblem(err instanceof Error ? err.message : String(err));
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [client]);

  /** Open a work item's most recent Run, or start one if it has none. */
  const open = useCallback(
    async (workItem: WorkItem) => {
      setProblem(null);
      try {
        const full = await client.getWorkItem(workItem.id);
        const latest = full.runs?.[0];
        const run = latest ?? (await client.createRun(workItem.id));
        setActive({ runId: run.id, title: full.title });
      } catch (err) {
        setProblem(err instanceof Error ? err.message : String(err));
      }
    },
    [client],
  );

  if (active) {
    return (
      <div className="app" data-testid="run-view">
        <RunScreen
          client={client}
          runId={active.runId}
          title={active.title}
          onBack={() => setActive(null)}
        />
      </div>
    );
  }

  return (
    <div className="app" data-testid="work-item-list">
      <header className="appHeader">
        <h1>dude</h1>
        <Button size="sm" variant="ghost" onClick={onSignOut}>
          Sign out
        </Button>
      </header>

      {problem ? <p className="problem">{problem}</p> : null}

      {workItems.length === 0 ? (
        <EmptyState
          title="Nothing to work on yet"
          description="Create a project and a work item through the API, then reload."
        />
      ) : null}

      <ul className="workItems">
        {workItems.map((item) => (
          <li key={item.id}>
            <button
              type="button"
              className="workItem"
              data-testid="work-item"
              onClick={() => void open(item)}
            >
              <StatusBadge status={item.status} />
              <span className="workItemTitle">{item.title}</span>
              <span className="workItemProject">
                {projects.find((p) => p.id === item.projectId)?.name ?? ""}
              </span>
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}

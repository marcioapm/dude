/**
 * The shell: navigation on the left, the selected thing on the right.
 *
 * The sidebar's selection decides the main pane, the way the design system's
 * `boardScope` describes it: a project or epic opens its board, a work item
 * opens its delivery view, and an agent (a phase Run) opens its conversation.
 *
 * The tree is re-read when the organization's event stream says something
 * happened, not on a timer — so a reviewer starting in another tab shows up
 * here within a moment, and an idle page makes no requests at all.
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import { boardScope, type NavProject, type NavRef } from "@dude/design-system";
import { Board, Sidebar } from "@dude/design-system/components";
import { Button, EmptyState, Spinner } from "@dude/design-system/primitives";
import type { ApiClient } from "./api/client.ts";
import { useReloadOnEvents } from "./hooks/useEventStream.ts";
import { RunScreen } from "./screens/RunScreen.tsx";
import { WorkItemScreen } from "./screens/WorkItemScreen.tsx";
import { WorkItemDialog } from "./screens/WorkItemDialog.tsx";
import { ProjectSettingsScreen } from "./screens/ProjectSettingsScreen.tsx";
import { NewProjectDialog } from "./screens/NewProjectDialog.tsx";
import { OrganizationSettingsScreen } from "./screens/OrganizationSettingsScreen.tsx";

export interface AppProps {
  client: ApiClient;
  onSignOut: () => void;
}

/**
 * A place in the app: something in the tree, a project's settings, or the
 * organization's (kind "org", which the tree does not have).
 */
type Place = (NavRef & { settings?: boolean }) | { kind: "org"; id: "settings"; settings?: undefined };

/** Selection lives in the URL hash, so a reload lands where you were. */
function readSelection(): Place | null {
  const [kind, id, view] = window.location.hash.replace(/^#\/?/, "").split("/");
  if (kind === "org" && id === "settings") return { kind: "org", id: "settings" };
  if (!kind || !id) return null;
  if (!["project", "epic", "workItem", "run", "session"].includes(kind)) return null;
  const ref = { kind: kind as NavRef["kind"], id: decodeURIComponent(id) };
  return kind === "project" && view === "settings" ? { ...ref, settings: true } : ref;
}

function writeSelection(ref: Place | null) {
  const hash = ref ? `#/${ref.kind}/${encodeURIComponent(ref.id)}${ref.settings ? "/settings" : ""}` : "";
  if (window.location.hash !== hash) window.history.replaceState(null, "", hash || " ");
}

/** The work item an agent row belongs to, for the header over its chat. */
function workItemOfAgent(projects: readonly NavProject[], agentId: string) {
  for (const project of projects) {
    const items = [...(project.workItems ?? []), ...(project.epics ?? []).flatMap((e) => e.workItems)];
    for (const item of items) {
      for (const run of item.runs ?? []) {
        if (run.id === agentId || run.sessions.some((s) => s.id === agentId)) return item;
      }
    }
  }
  return null;
}

export function App({ client, onSignOut }: AppProps) {
  const [projects, setProjects] = useState<NavProject[] | null>(null);
  const [selected, setSelectedState] = useState<Place | null>(readSelection);
  const [problem, setProblem] = useState<string | null>(null);
  const [newProject, setNewProject] = useState(false);

  const setSelected = useCallback((ref: Place | null) => {
    setSelectedState(ref);
    writeSelection(ref);
  }, []);

  const load = useCallback(async () => {
    try {
      const { projects: found } = await client.navigation();
      setProjects(found);
      setProblem(null);
    } catch (err) {
      setProblem(err instanceof Error ? err.message : String(err));
    }
  }, [client]);

  useEffect(() => {
    void load();
  }, [load]);

  useReloadOnEvents({ client, all: true }, () => void load(), 400);

  // First load with nothing selected: open the first project's board rather
  // than an empty pane.
  useEffect(() => {
    if (!selected && projects && projects[0]) setSelected({ kind: "project", id: projects[0].id });
  }, [projects, selected, setSelected]);

  // What the tree knows of the selection: the organization is not in it.
  const inTree = selected?.kind === "org" ? null : selected;
  const scope = useMemo(() => (projects ? boardScope(projects, inTree) : null), [projects, inTree]);

  let main;
  if (!projects) {
    main = <div className="centered"><Spinner label="Loading…" /></div>;
  } else if (projects.length === 0) {
    main = (
      <EmptyState
        title="No projects yet"
        description="A project is where work for a codebase lives: its repositories, its agents, its work items."
        action={
          <Button variant="primary" leadingIcon="plus" onClick={() => setNewProject(true)} data-testid="new-project-empty">
            New project
          </Button>
        }
      />
    );
  } else if (selected?.kind === "org") {
    main = <OrganizationSettingsScreen client={client} />;
  } else if (selected?.kind === "project" && selected.settings) {
    main = (
      <ProjectSettingsScreen
        key={selected.id}
        client={client}
        projectId={selected.id}
        onChanged={() => void load()}
        onBack={() => setSelected({ kind: "project", id: selected.id })}
      />
    );
  } else if (scope) {
    main = (
      <Board
        project={scope.project}
        epic={scope.epic}
        selected={selected}
        onSelect={(ref) => setSelected(ref)}
        headerActions={
          <>
          {scope.epic ? null : (
            <Button
              size="sm"
              variant="secondary"
              leadingIcon="list-check"
              onClick={() => setSelected({ kind: "project", id: scope.project.id, settings: true })}
              data-testid="project-settings-button"
            >
              Settings
            </Button>
          )}
          <NewWorkItemButton
            client={client}
            projectId={scope.project.id}
            epicId={scope.epic?.id ?? null}
            onCreated={(id) => {
              void load();
              setSelected({ kind: "workItem", id });
            }}
          />
          </>
        }
      />
    );
  } else if (selected?.kind === "workItem") {
    main = (
      <WorkItemScreen
        key={selected.id}
        client={client}
        workItemId={selected.id}
        onOpenRun={(runId) => setSelected({ kind: "session", id: runId })}
      />
    );
  } else if (selected && (selected.kind === "session" || selected.kind === "run")) {
    const item = workItemOfAgent(projects, selected.id);
    main = (
      <RunScreen
        key={selected.id}
        client={client}
        runId={selected.id}
        title={item?.title}
        onBack={item ? () => setSelected({ kind: "workItem", id: item.id }) : undefined}
      />
    );
  } else {
    main = <EmptyState title="Nothing selected" description="Pick something from the sidebar." />;
  }

  return (
    <div className="shell" data-testid="shell">
      <Sidebar
        projects={projects ?? []}
        loading={!projects}
        selected={inTree}
        onSelect={(ref) => setSelected(ref)}
        title="dude"
        footer={
          <div className="sidebarFooter">
            <Button size="sm" variant="ghost" leadingIcon="plus" onClick={() => setNewProject(true)} data-testid="new-project">
              New project
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setSelected({ kind: "org", id: "settings" })}
              data-testid="org-settings-button">
              Organization
            </Button>
            <Button size="sm" variant="ghost" onClick={onSignOut}>
              Sign out
            </Button>
          </div>
        }
      />
      {newProject ? (
      <NewProjectDialog
        client={client}
        open={newProject}
        onOpenChange={setNewProject}
        onCreated={(id) => {
          void load();
          setSelected({ kind: "project", id, settings: true });
        }}
      />
      ) : null}
      <main className="main">
        {problem ? <p className="problem">{problem}</p> : null}
        {main}
      </main>
    </div>
  );
}

/** Create a work item from the board, in the board's epic when it has one. */
function NewWorkItemButton(props: {
  client: ApiClient;
  projectId: string;
  epicId: string | null;
  onCreated: (id: string) => void;
}) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button size="sm" variant="primary" leadingIcon="plus" onClick={() => setOpen(true)} data-testid="new-work-item">
        New work item
      </Button>
      {open ? (
        <WorkItemDialog
          client={props.client}
          projectId={props.projectId}
          epicId={props.epicId}
          onClose={() => setOpen(false)}
          onSaved={(id) => props.onCreated(id)}
        />
      ) : null}
    </>
  );
}

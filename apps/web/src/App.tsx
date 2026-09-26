/**
 * The shell: navigation on the left, the selected thing on the right.
 *
 * The sidebar's selection decides the main pane, the way the design system's
 * `boardScope` describes it: a project or epic opens its board, a task
 * opens its delivery view, and an agent (a phase Run) opens its conversation.
 * Settings are places too (`place.ts`), outside the tree.
 *
 * The tree is re-read when the organization's event stream says something
 * happened, not on a timer — so a reviewer starting in another tab shows up
 * here within a moment, and an idle page makes no requests at all.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { boardScope, type NavProject, type NavRow } from "@dude/design-system";
import { Board, Breadcrumb, Sidebar, type BreadcrumbItem } from "@dude/design-system/components";
import { Button, Callout, EmptyState, RowMenu, Spinner, useToast } from "@dude/design-system/primitives";
import type { ApiClient } from "./api/client.ts";
import { useReloadOnEvents } from "./hooks/useEventStream.ts";
import { errorText } from "./hooks/useSave.tsx";
import { formatPlace, inTree, parsePlace, treeSelection, type Place } from "./place.ts";
import { startPush } from "./push.ts";
import { DeleteEpicDialog, EpicDialog, epicRef, rowActions, type Intent } from "./screens/actions.tsx";
import { NewProjectDialog } from "./screens/NewProjectDialog.tsx";
import { InboxScreen } from "./screens/InboxScreen.tsx";
import { EpicMetricsSection } from "./screens/MetricsSection.tsx";
import { MySettingsScreen } from "./screens/MySettingsScreen.tsx";
import { OrganizationSettingsScreen } from "./screens/OrganizationSettingsScreen.tsx";
import { ProjectSettingsScreen } from "./screens/ProjectSettingsScreen.tsx";
import { RunScreen } from "./screens/RunScreen.tsx";
import { existingTask, TaskDialog, type ExistingTask } from "./screens/TaskDialog.tsx";
import { TaskScreen } from "./screens/TaskScreen.tsx";

export interface AppProps {
  client: ApiClient;
  onSignOut: () => void;
}

/** The one dialog the shell may have open. */
type Open =
  | { kind: "newProject" }
  | { kind: "task"; projectId: string; epicId: string | null; editing?: string }
  | Extract<Intent, { kind: "newEpic" }>
  | Extract<Intent, { kind: "editEpic" }>
  | Extract<Intent, { kind: "deleteEpic" }>;

const GROUP_BY_EPIC = "dude.board.groupByEpic";

const OPENS_A_DIALOG: ReadonlySet<Intent["kind"]> = new Set(["newTask", "editTask", "newEpic", "editEpic", "deleteEpic"]);

/**
 * Where something sits: its project, its epic when it has one, and its work
 * item — found by the task's id, or by one of its agents' (Run or
 * session) ids, in which case `agent` names that agent as the tree does.
 */
function locate(projects: readonly NavProject[], id: string) {
  for (const project of projects) {
    const groups = [{ epic: null, items: project.tasks ?? [] }, ...(project.epics ?? []).map((e) => ({ epic: e, items: e.tasks }))];
    for (const { epic, items } of groups) {
      for (const item of items) {
        if (item.id === id) return { project, epic, item, agent: null };
        for (const run of item.runs ?? []) {
          const session = run.sessions.find((s) => s.id === id);
          if (run.id === id || session) return { project, epic, item, agent: session?.title ?? "Agent" };
        }
      }
    }
  }
  return null;
}

export function App({ client, onSignOut }: AppProps) {
  const [projects, setProjects] = useState<NavProject[] | null>(null);
  const [place, setPlaceState] = useState<Place | null>(() => parsePlace(window.location.hash));
  const [problem, setProblem] = useState<string | null>(null);
  const [open, setOpen] = useState<Open | null>(null);
  const [groupByEpic, setGroupByEpic] = useState(() => localStorage.getItem(GROUP_BY_EPIC) === "1");
  const { toast } = useToast();

  /** Move to a place, as a step Back can undo — or, with `replace`, in place of this one. */
  const go = useCallback((next: Place | null, replace = false) => {
    setPlaceState(next);
    const hash = formatPlace(next);
    if (window.location.hash === hash) return;
    if (replace) window.history.replaceState(null, "", hash || " ");
    else window.history.pushState(null, "", hash || " ");
  }, []);

  // Back, Forward and an edited URL move the app too.
  useEffect(() => {
    const follow = () => setPlaceState(parsePlace(window.location.hash));
    window.addEventListener("hashchange", follow);
    return () => window.removeEventListener("hashchange", follow);
  }, []);

  // Notifications: the service worker, and a clicked one opening its place.
  useEffect(() => startPush(client, (hash) => {
    window.location.hash = hash;
  }), [client]);

  const load = useCallback(async () => {
    try {
      const { projects: found } = await client.navigation();
      setProjects(found);
      setProblem(null);
    } catch (err) {
      setProblem(errorText(err));
    }
  }, [client]);

  useEffect(() => {
    void load();
  }, [load]);

  useReloadOnEvents({ client, all: true }, () => void load(), 400);

  // First load with nothing selected: open the first project's board rather
  // than an empty pane.
  useEffect(() => {
    if (!place && projects && projects[0]) go(inTree({ kind: "project", id: projects[0].id }), true);
  }, [projects, place, go]);

  const selected = treeSelection(place);
  const scope = useMemo(() => (projects ? boardScope(projects, selected) : null), [projects, selected]);

  /** Carry out a row or board action: quick ones here, the rest in a dialog. */
  const act = useCallback(
    (intent: Intent) => {
      const quietly = (what: Promise<unknown>) =>
        void what.then(() => load(), (err: unknown) => toast({ title: errorText(err), tone: "danger" }));
      switch (intent.kind) {
        case "newTask":
          return setOpen({ kind: "task", projectId: intent.projectId, epicId: intent.epicId });
        case "editTask": {
          const project = projects && locate(projects, intent.taskId)?.project;
          return project ? setOpen({ kind: "task", projectId: project.id, epicId: null, editing: intent.taskId }) : undefined;
        }
        case "projectSettings":
          return go({ view: "projectSettings", projectId: intent.projectId });
        case "moveEpic":
          return quietly(client.updateEpic(intent.epicId, { position: intent.position }));
        case "moveTask":
          return quietly(client.updateTask(intent.taskId, { epicId: intent.epicId }));
        default:
          return setOpen(intent);
      }
    },
    [client, go, load, projects, toast],
  );

  // A dialog opened from a row's menu gives focus back to that row when it
  // closes: the menu item that opened it is gone by then.
  const returnTo = useRef<string | null>(null);
  const close = useCallback(() => {
    setOpen(null);
    const key = returnTo.current;
    returnTo.current = null;
    if (key) requestAnimationFrame(() => document.querySelector<HTMLElement>(`[data-nav-key="${CSS.escape(key)}"]`)?.focus());
  }, []);

  const menuItems = useCallback(
    (row: NavRow) => {
      const project = projects?.find((p) => p.id === row.projectId);
      return project
        ? rowActions(project, row.ref, (intent) => {
            if (OPENS_A_DIALOG.has(intent.kind)) returnTo.current = row.key;
            act(intent);
          })
        : null;
    },
    [projects, act],
  );

  let main;
  // Settings that are not a project's come first: a new organization with
  // no projects yet still sets up its GitHub connection, and you your view.
  if (place?.view === "orgSettings") {
    main = <OrganizationSettingsScreen client={client} />;
  } else if (place?.view === "mySettings") {
    main = <MySettingsScreen client={client} />;
  } else if (!projects) {
    main = <div className="centered"><Spinner label="Loading…" /></div>;
  } else if (projects.length === 0) {
    main = (
      <EmptyState
        title="No projects yet"
        description="A project is where work for a codebase lives: its repositories, its agents, its tasks."
        action={
          <Button variant="primary" leadingIcon="plus" onClick={() => setOpen({ kind: "newProject" })} data-testid="new-project-empty">
            New project
          </Button>
        }
      />
    );
  } else if (place?.view === "inbox") {
    main = <InboxScreen projects={projects} selected={selected} onSelect={(ref) => go(inTree(ref))} />;
  } else if (place?.view === "projectSettings") {
    main = (
      <ProjectSettingsScreen
        key={place.projectId}
        client={client}
        projectId={place.projectId}
        onChanged={() => void load()}
        onBack={() => go(inTree({ kind: "project", id: place.projectId }))}
      />
    );
  } else if (scope) {
    const project = scope.project;
    main = (
      <Board
        project={project}
        epic={scope.epic}
        overview={scope.epic ? <EpicMetricsSection client={client} epicId={scope.epic.id} /> : undefined}
        selected={selected}
        onSelect={(ref) => go(inTree(ref))}
        groupBy={groupByEpic ? "epic" : null}
        laneMenu={(lane) => {
          if (!lane.epic) return null;
          const items = rowActions(project, { kind: "epic", id: lane.epic.id }, act);
          return items ? <RowMenu items={items} label={`Actions for ${lane.title}`} size="sm" /> : null;
        }}
        headerActions={
          <>
            {scope.epic ? (
              <Button size="sm" variant="secondary" leadingIcon="edit" data-testid="edit-epic-button"
                onClick={() => act({ kind: "editEpic", epic: epicRef(project.id, scope.epic!) })}>
                Edit epic
              </Button>
            ) : (
              <>
                <Button size="sm" variant={groupByEpic ? "secondary" : "ghost"} leadingIcon="layers" aria-pressed={groupByEpic}
                  data-testid="group-by-epic"
                  onClick={() => {
                    localStorage.setItem(GROUP_BY_EPIC, groupByEpic ? "0" : "1");
                    setGroupByEpic(!groupByEpic);
                  }}>
                  Group by epic
                </Button>
                <Button size="sm" variant="ghost" leadingIcon="layers" data-testid="new-epic"
                  onClick={() => act({ kind: "newEpic", projectId: project.id })}>
                  New epic
                </Button>
                <Button size="sm" variant="secondary" leadingIcon="settings" data-testid="project-settings-button"
                  onClick={() => go({ view: "projectSettings", projectId: project.id })}>
                  Settings
                </Button>
              </>
            )}
            <Button size="sm" variant="primary" leadingIcon="plus" data-testid="new-task"
              onClick={() => act({ kind: "newTask", projectId: project.id, epicId: scope.epic?.id ?? null })}>
              New task
            </Button>
          </>
        }
      />
    );
  } else if (selected?.kind === "task") {
    main = (
      <TaskScreen
        key={selected.id}
        client={client}
        taskId={selected.id}
        onOpenRun={(runId) => go(inTree({ kind: "session", id: runId }))}
        breadcrumb={trail(selected.id)}
      />
    );
  } else if (selected && (selected.kind === "session" || selected.kind === "run")) {
    const where = locate(projects, selected.id);
    main = (
      <RunScreen
        key={selected.id}
        client={client}
        runId={selected.id}
        title={where?.item.title}
        breadcrumb={trail(selected.id)}
      />
    );
  } else {
    main = <EmptyState title="Nothing selected" description="Pick something from the sidebar." />;
  }

  /**
   * Project › Epic › KEY for a task, each a way back up — and, on an
   * agent's conversation, the agent last, so the task is a link too.
   */
  function trail(id: string) {
    const where = projects ? locate(projects, id) : null;
    if (!where) return null;
    const taskId = where.item.id;
    const items: BreadcrumbItem[] = [
      { id: where.project.id, label: where.project.name, onSelect: () => go(inTree({ kind: "project", id: where.project.id })) },
    ];
    if (where.epic) {
      const epicId = where.epic.id;
      items.push({ id: epicId, label: where.epic.title, icon: "layers", onSelect: () => go(inTree({ kind: "epic", id: epicId })) });
    }
    items.push({
      id: taskId,
      label: where.item.key ?? where.item.title,
      mono: Boolean(where.item.key),
      ...(where.agent !== null ? { onSelect: () => go(inTree({ kind: "task", id: taskId })) } : {}),
    });
    if (where.agent !== null) items.push({ id, label: where.agent });
    return <Breadcrumb items={items} />;
  }

  const saved = () => void load();

  return (
    <div className="shell" data-testid="shell">
      <Sidebar
        projects={projects ?? []}
        loading={!projects}
        selected={selected}
        onSelect={(ref) => go(inTree(ref))}
        onShowAllAttention={() => go({ view: "inbox" })}
        menuItems={menuItems}
        title="dude"
        footer={
          <div className="sidebarFooter">
            <Button size="sm" variant="ghost" leadingIcon="plus" onClick={() => setOpen({ kind: "newProject" })} data-testid="new-project">
              New project
            </Button>
            <Button size="sm" variant="ghost" onClick={() => go({ view: "orgSettings" })} data-testid="org-settings-button">
              Organization
            </Button>
            <Button size="sm" variant="ghost" onClick={() => go({ view: "mySettings" })} data-testid="my-settings-button">
              You
            </Button>
            <Button size="sm" variant="ghost" onClick={onSignOut}>
              Sign out
            </Button>
          </div>
        }
      />
      {open?.kind === "newProject" ? (
        <NewProjectDialog
          client={client}
          open
          onOpenChange={(o) => !o && close()}
          onCreated={(id) => {
            saved();
            go({ view: "projectSettings", projectId: id });
          }}
        />
      ) : null}
      {open?.kind === "task" ? (
        <TaskDialogFor key={open.editing ?? "new"} client={client} open={open} onClose={close} onSaved={(id) => {
          saved();
          if (!open.editing) go(inTree({ kind: "task", id }));
        }} />
      ) : null}
      {open?.kind === "newEpic" || open?.kind === "editEpic" ? (
        <EpicDialog
          client={client}
          projectId={open.kind === "newEpic" ? open.projectId : open.epic.projectId}
          epic={open.kind === "editEpic" ? open.epic : null}
          onClose={close}
          onSaved={(epic) => {
            saved();
            // A new epic is shown where it is: its board, which reveals it in the tree.
            if (open.kind === "newEpic") go(inTree({ kind: "epic", id: epic.id }));
          }}
        />
      ) : null}
      {open?.kind === "deleteEpic" ? (
        <DeleteEpicDialog
          client={client}
          epic={open.epic}
          onClose={close}
          onDeleted={() => {
            saved();
            // Replacing it, so Back does not return to an epic that is gone.
            if (selected?.kind === "epic" && selected.id === open.epic.id) go(inTree({ kind: "project", id: open.epic.projectId }), true);
          }}
        />
      ) : null}
      <main className="main">
        {problem ? <Callout tone="danger">{problem}</Callout> : null}
        {main}
      </main>
    </div>
  );
}

/**
 * The task dialog, for a new task or an existing one — which it
 * reads first, since the tree does not carry what a task asks for.
 */
function TaskDialogFor(props: {
  client: ApiClient;
  open: Extract<Open, { kind: "task" }>;
  onClose: () => void;
  onSaved: (id: string) => void;
}) {
  const { client, open, onClose } = props;
  const [existing, setExisting] = useState<ExistingTask | null>(null);
  const { toast } = useToast();
  // Keyed by what it edits, so a late answer for another task lands
  // in an unmounted dialog, not this one.
  useEffect(() => {
    if (!open.editing) return;
    let current = true;
    void client.getTask(open.editing).then(
      (item) => current && setExisting(existingTask(item, item.runs.length > 0)),
      (err: unknown) => {
        if (!current) return;
        toast({ title: errorText(err), tone: "danger" });
        onClose();
      },
    );
    return () => {
      current = false;
    };
  }, [client, open.editing, toast, onClose]);
  if (open.editing && !existing) return null;
  return (
    <TaskDialog
      client={client}
      projectId={open.projectId}
      epicId={open.epicId}
      existing={existing ?? undefined}
      onClose={props.onClose}
      onSaved={props.onSaved}
    />
  );
}

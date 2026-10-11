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
import { EventTypes } from "@dude/domain";
import { boardScope, type NavProject, type NavRow, type NavTask } from "@dude/design-system";
import { Board, Breadcrumb, Sidebar, SidebarLink, SidebarProfile, SidebarRailItem, SidebarSessions, SidebarToggle, SIDEBAR_DRAWER_QUERY, PersonAvatar, type BreadcrumbItem, type PrChipPullRequest } from "@dude/design-system/components";
import { Button, Callout, EmptyState, IconButton, RowMenu, Spinner, isBareKey, useToast } from "@dude/design-system/primitives";
import { Icon } from "@dude/design-system";
import { sessionTitle, type SessionsList } from "@dude/domain";
import { ApiError, type ApiClient, type PullRequest } from "./api/client.ts";
import { usePeople } from "./people.tsx";
import { Reconnecting } from "./Reconnecting.tsx";
import { AGENT_CHATTER, cameBack, useReloadOnEvents } from "./hooks/useEventStream.ts";
import { useVisibleInterval } from "./hooks/useVisibleInterval.ts";
import { errorText } from "./hooks/useSave.tsx";
import { formatPlace, inTree, parsePlace, treeSelection, type Place } from "./place.ts";
import { startPush } from "./push.ts";
import { DeleteEpicDialog, EpicDialog, epicRef, rowActions, type Intent } from "./screens/actions.tsx";
import { NewProjectDialog } from "./screens/NewProjectDialog.tsx";
import { InboxScreen } from "./screens/InboxScreen.tsx";
import { NotFound } from "./screens/NotFound.tsx";
import { EpicMetricsSection } from "./screens/MetricsSection.tsx";
import { MySettingsScreen } from "./screens/MySettingsScreen.tsx";
import { OrganizationSettingsScreen } from "./screens/OrganizationSettingsScreen.tsx";
import { ProjectSettingsScreen } from "./screens/ProjectSettingsScreen.tsx";
import { ProjectEpics } from "./screens/ProjectEpics.tsx";
import { RunScreen } from "./screens/RunScreen.tsx";
import { SessionScreen } from "./screens/SessionScreen.tsx";
import { SessionsScreen } from "./screens/SessionsScreen.tsx";
import { WelcomeScreen } from "./screens/WelcomeScreen.tsx";
import { forgetModelOptions } from "./sessionModel.ts";
import { existingTask, TaskDialog, type ExistingTask } from "./screens/TaskDialog.tsx";
import { TaskScreen } from "./screens/TaskScreen.tsx";
import { DudeMark } from "./DudeMark.tsx";

export interface AppProps {
  client: ApiClient;
  onSignOut: () => void;
  /** `client`'s credential was refused: it is wrong, revoked or expired. */
  onKeyRefused: (client: ApiClient) => void;
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

/**
 * The tree with each task's pull requests on it, so a card and a row can
 * show the one state that matters. The navigation read does not carry
 * them; one read of the organisation's recent pull requests does.
 */
export function withPullRequests(projects: NavProject[], prs: readonly PullRequest[]): NavProject[] {
  if (prs.length === 0) return projects;
  const byTask = new Map<string, PrChipPullRequest[]>();
  for (const pr of [...prs].sort((a, b) => a.createdAt.localeCompare(b.createdAt))) {
    const list = byTask.get(pr.taskId) ?? [];
    list.push(pr);
    byTask.set(pr.taskId, list);
  }
  const task = (t: NavTask): NavTask => (byTask.has(t.id) ? { ...t, pullRequests: byTask.get(t.id) } : t);
  return projects.map((p) => ({
    ...p,
    ...(p.epics ? { epics: p.epics.map((e) => ({ ...e, tasks: e.tasks.map(task) })) } : {}),
    ...(p.tasks ? { tasks: p.tasks.map(task) } : {}),
  }));
}

const MINE = "dude.tree.mine";
const SIDEBAR = "dude.sidebar";

/**
 * An agent at work, and its plan and heartbeat: none of it is in the tree.
 * What it spends is (a card's, a lane's, an epic's cost), so a finished
 * model request and a cost sample still reload.
 */
const QUIET_EVENTS: ReadonlySet<string> = new Set([...AGENT_CHATTER, EventTypes.PlanUpdated, EventTypes.WorkerHeartbeat]);

/** A session's events that change your list of sessions or what waits on you. */
const SESSION_LIST_EVENTS: ReadonlySet<string> = new Set([
  EventTypes.BrainstormCreated, EventTypes.BrainstormShared, EventTypes.BrainstormJoined, EventTypes.BrainstormDeclined,
  EventTypes.BrainstormMemberRemoved, EventTypes.BrainstormOwnerChanged, EventTypes.BrainstormLinked, EventTypes.BrainstormFiled,
  EventTypes.BrainstormRenamed,
  EventTypes.QuestionAsked, EventTypes.QuestionAnswered, EventTypes.RunCreated, EventTypes.RunCompleted, EventTypes.RunFailed,
  EventTypes.RunPaused, EventTypes.RunResumed,
]);

export function App({ client, onSignOut, onKeyRefused }: AppProps) {
  const [projects, setProjects] = useState<NavProject[] | null>(null);
  const people = usePeople();
  // Making a project is an admin's: it brings its own models, image and repositories.
  const isAdmin = people.me?.role === "admin";
  const [mine, setMine] = useState(() => localStorage.getItem(MINE) === "1");
  // The sidebar drawer, on a narrow screen.
  const [navOpen, setNavOpen] = useState(false);
  // Folded to its rail on a wide screen: the person's choice, kept as the density is.
  const [railed, setRailed] = useState(() => localStorage.getItem(SIDEBAR) === "rail");
  const setCollapsed = useCallback((rail: boolean) => {
    localStorage.setItem(SIDEBAR, rail ? "rail" : "full");
    setRailed(rail);
  }, []);
  // `[` outside a field folds or unfolds it (the rail's own `/` is the Sidebar's).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!isBareKey(e, "[")) return;
      // Under the drawer breakpoint there is no rail to fold to.
      if (typeof window.matchMedia === "function" && window.matchMedia(SIDEBAR_DRAWER_QUERY).matches) return;
      e.preventDefault();
      setRailed((rail) => {
        localStorage.setItem(SIDEBAR, rail ? "full" : "rail");
        return !rail;
      });
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
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

  // Bumped on each reload: what reads its own data (an epic's metrics)
  // re-reads with the tree rather than on a stream of its own.
  const [version, setVersion] = useState(0);
  const load = useCallback(async () => {
    try {
      const [{ projects: found }, prs] = await Promise.all([
        client.navigation(),
        // Chips are a nicety: the tree is drawn without them if this fails.
        client.recentPullRequests().catch(() => ({ pullRequests: [] as PullRequest[] })),
      ]);
      setProjects(withPullRequests(found, prs.pullRequests));
      setVersion((v) => v + 1);
      setProblem(null);
    } catch (err) {
      // A refused credential needs a fresh session check; transport errors stay in the app.
      if (err instanceof ApiError && (err.status === 401 || err.status === 403)) onKeyRefused(client);
      else setProblem(errorText(err));
    }
  }, [client, onKeyRefused]);

  useEffect(() => {
    void load();
  }, [load]);

  // Your brainstorm sessions, invitations and questions put to you: the
  // sidebar's list and the inbox's lines. Re-read when a session's event
  // reaches you (the stream gives you only your sessions').
  const [sessionsList, setSessionsList] = useState<SessionsList | null>(null);
  // Only the latest read is kept: an older one answering late would bring back a row just archived.
  const sessionsRead = useRef(0);
  const loadSessions = useCallback(async () => {
    const read = ++sessionsRead.current;
    try {
      const list = await client.sessions();
      if (read === sessionsRead.current) setSessionsList(list);
    } catch {
      // The list is a nicety beside the tree: kept as it was.
    }
  }, [client]);
  // An invitation is not on your stream until you accept it: read again
  // each minute while the page is shown, and once on showing it again.
  useVisibleInterval(() => void loadSessions(), 60_000);
  const reloadSessions = useRef(loadSessions);
  reloadSessions.current = loadSessions;

  // Someone seen is presence, and an agent's words, tools, plan and diff
  // change nothing the tree shows: no reload for either.
  const stream = useReloadOnEvents({ client, all: true }, () => void load(), 400,
    (e) => {
      if (people.seen(e)) return true;
      // A tier added, edited or removed, or the Brainstorm setting changed: the model pickers read them again.
      if (e.eventType === EventTypes.SettingsUpdated) forgetModelOptions(client);
      if (e.sessionId?.startsWith("ssn_") || e.eventType.startsWith("session.")) {
        if (SESSION_LIST_EVENTS.has(e.eventType)) void reloadSessions.current();
        return true;
      }
      return QUIET_EVENTS.has(e.eventType);
    });
  // A settings change missed while the stream was down is never replayed: the model pickers read again.
  const wasStream = useRef(stream);
  useEffect(() => {
    if (cameBack(wasStream.current, stream)) forgetModelOptions(client);
    wasStream.current = stream;
  }, [stream, client]);

  const selected = treeSelection(place);

  // A session opens on its task's page. The tree names the task for the
  // sessions it holds; one it does not (an attempt older than the tree
  // keeps) is read once to learn its task.
  // A session's task never changes, so once known it is kept: a reload that
  // drops the session from the tree (a newer attempt) must not unmount its
  // page. Null: no such Run. A failed read is tried again a few times.
  const [runTasks, setRunTasks] = useState<ReadonlyMap<string, string | null>>(new Map());
  const sessionId = selected?.kind === "session" || selected?.kind === "run" ? selected.id : null;
  const inTreeTask = sessionId && projects ? (locate(projects, sessionId)?.item.id ?? null) : null;
  if (sessionId && inTreeTask && runTasks.get(sessionId) !== inTreeTask) setRunTasks((m) => new Map(m).set(sessionId, inTreeTask));
  const [lookup, setLookup] = useState<{ id: string; tries: number } | null>(null);
  const tries = lookup?.id === sessionId ? lookup.tries : 0;
  const lookupFailed = tries >= 3;
  const loaded = projects !== null;
  useEffect(() => {
    // The tree reloads often; the lookup waits on it having loaded, not on each reload.
    if (!sessionId || !loaded || inTreeTask || runTasks.has(sessionId) || lookupFailed) return;
    let current = true;
    const wait = tries === 0 ? 0 : Math.min(8000, 1000 * 2 ** tries);
    const timer = setTimeout(() => client.getRun(sessionId).then(
      (run) => current && setRunTasks((m) => new Map(m).set(sessionId, run.taskId)),
      (err: unknown) => {
        if (!current) return;
        if (err instanceof ApiError && err.status === 404) setRunTasks((m) => new Map(m).set(sessionId, null));
        else setLookup({ id: sessionId, tries: tries + 1 });
      },
    ), wait);
    return () => {
      current = false;
      clearTimeout(timer);
    };
  }, [client, sessionId, loaded, inTreeTask, runTasks, tries, lookupFailed]);
  const sessionTask = sessionId ? (inTreeTask ?? runTasks.get(sessionId) ?? null) : null;
  const scope = useMemo(() => (projects ? boardScope(projects, selected) : null), [projects, selected]);
  const toBoard = useCallback(() => go(projects?.[0] ? inTree({ kind: "project", id: projects[0].id }) : null), [go, projects]);

  // The tab says where you are: "TEXT-14 · Implement — dude"; teammates
  // see the same beside your face.
  useEffect(() => {
    const where = placeTitle(place, projects);
    document.title = [where, "dude"].filter(Boolean).join(" — ");
    client.setWhere(where);
  }, [place, projects, client]);

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
          return go({ view: "projectSettings", projectId: intent.projectId, ...(intent.page ? { page: intent.page } : {}) });
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

  // The board fills the pane edge to edge; everything else sits in it with a margin.
  let flush = false;
  let main;
  const openSession = (id: string) => go({ view: "brainstorm", id });
  // Reads the sessions list again as it opens one: a session just made is not in it yet.
  const openListedSession = (id: string) => {
    void loadSessions();
    openSession(id);
  };
  // New session is the welcome: a session is made by its first message there.
  const toWelcome = () => go({ view: "welcome" });
  // Settings that are not a project's come first: a new organization with
  // no projects yet still sets up its GitHub connection, and you your view.
  const openRun = (runId: string) => go(inTree({ kind: "session", id: runId }));
  const newProject = () => setOpen({ kind: "newProject" });
  if (place?.view === "welcome") {
    flush = true;
    // A session needs no project, so the welcome is always the welcome; an
    // organisation with none (known, not still loading) is also offered one.
    main = <WelcomeScreen client={client} projects={projects ?? []} sessions={sessionsList?.sessions ?? null} name={people.me?.name ?? null}
      onOpenSession={openSession} onAllSessions={() => go({ view: "sessions" })}
      offer={projects?.length === 0 && isAdmin ? (
        <Button variant="primary" leadingIcon="plus" onClick={newProject} data-testid="new-project-empty">New project</Button>
      ) : null}
      // A create that lands after the welcome was left re-reads the list but leaves the person where they went.
      onCreated={(id, stillHere) => (stillHere ? openListedSession(id) : void loadSessions())} />;
  } else if (place?.view === "orgSettings") {
    main = <OrganizationSettingsScreen client={client} me={people.me} people={people.all} onPeopleChanged={() => void people.refresh()}
      projects={projects ?? []} page={place.page} sub={place.sub} onOpenRun={openRun}
      onPage={(page, sub) => go(sub ? { view: "orgSettings", page, sub } : { view: "orgSettings", page }, !sub)} />;
  } else if (place?.view === "mySettings") {
    main = <MySettingsScreen client={client} me={people.me} onChanged={() => void people.refresh()} />;
  } else if (place?.view === "sessions") {
    main = <SessionsScreen client={client} sessions={sessionsList?.sessions ?? null} onNew={toWelcome} onOpen={openListedSession} />;
  } else if (place?.view === "brainstorm") {
    flush = true;
    main = <SessionScreen key={place.id} client={client} sessionId={place.id} projects={projects ?? []} onBack={() => go({ view: "sessions" })}
      onChanged={() => void loadSessions()} onArchived={(id, archived) => {
        // Archived, it leaves your list and sidebar at once, and you go to the list; the read after confirms it.
        if (archived) {
          setSessionsList((l) => (l ? { ...l, sessions: l.sessions.filter((s) => s.id !== id) } : l));
          go({ view: "sessions" });
        }
        void loadSessions();
      }} />;
  } else if (!projects) {
    main = <div className="centered"><Spinner label="Loading…" /></div>;
  } else if (projects.length === 0) {
    main = (
      <EmptyState
        title="No projects yet"
        description="A project is where work for a codebase lives: its repositories, its agents, its tasks."
        action={isAdmin ? (
          <Button variant="primary" leadingIcon="plus" onClick={newProject} data-testid="new-project-empty">
            New project
          </Button>
        ) : undefined}
      />
    );
  } else if (place?.view === "inbox") {
    flush = true;
    main = <InboxScreen client={client} projects={projects} sessions={sessionsList} onSelect={(ref) => go(inTree(ref))}
      onOpenSession={openSession} onChanged={() => {
        void load();
        void loadSessions();
      }} />;
  } else if (place?.view === "projectSettings") {
    main = (
      <ProjectSettingsScreen
        key={place.projectId}
        client={client}
        projectId={place.projectId}
        projects={projects}
        admin={isAdmin}
        page={place.page}
        onPage={(page) => go({ view: "projectSettings", projectId: place.projectId, page }, true)}
        onOrganization={(page) => go({ view: "orgSettings", page })}
        onOpenRun={openRun}
        onChanged={() => void load()}
        onBack={() => go(inTree({ kind: "project", id: place.projectId }))}
      />
    );
  } else if (scope) {
    const project = scope.project;
    flush = true;
    main = (
      <Board
        project={project}
        you={people.you}
        epic={scope.epic}
        overview={scope.epic ? <EpicMetricsSection client={client} epicId={scope.epic.id} version={version} />
          : <ProjectEpics client={client} projectId={project.id} version={version} onOpenEpic={(id) => go(inTree({ kind: "epic", id }))} />}
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
              <Button size="sm" variant="quiet" leadingIcon="edit" data-testid="edit-epic-button"
                onClick={() => act({ kind: "editEpic", epic: epicRef(project.id, scope.epic!) })}>
                Edit epic
              </Button>
            ) : (
              <>
                <Button size="sm" variant={groupByEpic ? "secondary" : "quiet"} leadingIcon="layers" aria-pressed={groupByEpic}
                  data-testid="group-by-epic"
                  onClick={() => {
                    localStorage.setItem(GROUP_BY_EPIC, groupByEpic ? "0" : "1");
                    setGroupByEpic(!groupByEpic);
                  }}>
                  Group by epic
                </Button>
                <Button size="sm" variant="secondary" leadingIcon="layers" data-testid="new-epic"
                  onClick={() => act({ kind: "newEpic", projectId: project.id })}>
                  New epic
                </Button>
                <Button size="sm" variant="quiet" leadingIcon="settings" data-testid="project-settings-button"
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
  } else if (selected?.kind === "task" || sessionTask) {
    // A session opens on its task's page, beside the task's other sessions.
    const taskId = selected?.kind === "task" ? selected.id : sessionTask!;
    flush = true;
    main = (
      <TaskScreen
        key={taskId}
        client={client}
        taskId={taskId}
        runId={selected?.kind === "task" ? undefined : sessionId ?? undefined}
        tab={place?.view === "tree" && selected?.kind === "task" ? place.tab : undefined}
        attempt={place?.view === "tree" && selected?.kind === "task" ? place.attempt : undefined}
        onOpenRun={(runId) => go(inTree({ kind: "session", id: runId }))}
        onNavigate={(tab, attempt, replace) => go(inTree({ kind: "task", id: taskId }, tab, attempt), replace)}
        onBack={toBoard}
        breadcrumb={trail(taskId)}
      />
    );
  } else if (sessionId && !runTasks.has(sessionId) && !lookupFailed) {
    main = <div className="centered"><Spinner label="Loading…" /></div>;
  } else if (selected && (selected.kind === "session" || selected.kind === "run")) {
    // Its task could not be learned: the session on its own, with the way to it.
    flush = true;
    main = (
      <RunScreen
        key={selected.id}
        client={client}
        runId={selected.id}
        onOpenTask={(taskId, tab) => go(inTree({ kind: "task", id: taskId }, tab))}
        onBack={toBoard}
      />
    );
  } else if (selected && (selected.kind === "epic" || selected.kind === "project")) {
    // Loaded, and not in the tree: deleted, or never this organization's.
    main = <NotFound what={selected.kind} onBack={toBoard} />;
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
  const you = people.me;

  return (
    <div className="shell" data-testid="shell">
      {/* First to take focus: past the sidebar, straight to what is selected. */}
      <a className="skipLink" href="#main" onClick={(e) => {
        e.preventDefault();
        document.getElementById("main")?.focus();
      }}>
        Skip to content
      </a>
      <Sidebar
        id="nav"
        collapsible
        open={navOpen}
        onOpenChange={setNavOpen}
        collapsed={railed}
        onCollapsedChange={setCollapsed}
        railMark={<DudeMark size={28} />}
        onHome={toWelcome}
        homeSelected={place?.view === "welcome"}
        railSessions={{
          onNew: toWelcome,
          onOpenList: () => go({ view: "sessions" }),
          current: place?.view === "sessions" || place?.view === "brainstorm",
          recent: (sessionsList?.sessions ?? []).map(sessionTitle),
        }}
        railFooter={
          <>
            <SidebarRailItem label="Organisation settings" current={place?.view === "orgSettings"} onClick={() => go({ view: "orgSettings" })}
              data-testid="rail-org-settings">
              <Icon name="building" size={16} />
            </SidebarRailItem>
            <SidebarRailItem label="Your settings" current={place?.view === "mySettings"} onClick={() => go({ view: "mySettings" })}
              data-testid="rail-my-settings">
              {you ? <PersonAvatar person={you} size={28} aria-hidden title="" /> : <Icon name="human" size={16} />}
            </SidebarRailItem>
          </>
        }
        projects={projects ?? []}
        loading={!projects}
        selected={selected}
        onSelect={(ref) => go(inTree(ref))}
        you={people.you}
        // You first, then the others as the organization lists them.
        online={people.all.filter((p) => p.online).sort((a, b) => Number(b.id === people.you) - Number(a.id === people.you))}
        onWaitingSelect={() => go({ view: "inbox" })}
        waitingSelected={place?.view === "inbox"}
        waitingExtra={(sessionsList?.invitations.length ?? 0) + (sessionsList?.questions.length ?? 0)}
        sessions={
          <SidebarSessions
            sessions={(sessionsList?.sessions ?? []).map((s) => ({ id: s.id, title: sessionTitle(s), shared: s.shared,
              owner: s.role === "owner" ? null : s.owner }))}
            selected={place?.view === "brainstorm" ? place.id : null}
            onSelect={openSession}
            onOpenList={() => go({ view: "sessions" })}
            onNew={toWelcome}
          />
        }
        mine={mine}
        onMineChange={(m) => {
          localStorage.setItem(MINE, m ? "1" : "0");
          setMine(m);
        }}
        menuItems={menuItems}
        title={<button type="button" className="brandHome" aria-label="El Duderino, home" onClick={toWelcome} data-testid="brand-home">
          <span className="brand"><DudeMark size={30} />El Duderino</span>
        </button>}
        treeActions={isAdmin ? (
          <IconButton size="sm" icon="plus" label="New project" onClick={() => setOpen({ kind: "newProject" })} data-testid="new-project" />
        ) : undefined}
        footer={
          <>
            <SidebarLink icon="building" current={place?.view === "orgSettings"} onClick={() => go({ view: "orgSettings" })}
              data-testid="org-settings-button">
              Organisation settings
            </SidebarLink>
            {you ? (
              <SidebarProfile person={you} detail={you.email ?? undefined} onOpen={() => go({ view: "mySettings" })} openProps={{ "data-testid": "my-settings-button" }}
                actions={<IconButton size="sm" icon="arrow-right" label="Sign out" onClick={onSignOut} data-testid="sign-out" />} />
            ) : (
              <>
                <SidebarLink icon="human" onClick={() => go({ view: "mySettings" })} data-testid="my-settings-button">
                  Your settings
                </SidebarLink>
                <SidebarLink icon="arrow-right" onClick={onSignOut} data-testid="sign-out">
                  Sign out
                </SidebarLink>
              </>
            )}
          </>
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
            // A new epic is shown where it is: its board, which reveals it in
            // the tree — once the tree has it, or its board would say it
            // does not exist.
            if (open.kind === "newEpic") void load().then(() => go(inTree({ kind: "epic", id: epic.id })));
            else saved();
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
      <main className={flush ? "main flush" : "main"} id="main" tabIndex={-1}>
        {/* The sidebar is a drawer on a narrow screen; this opens it. */}
        <SidebarToggle open={navOpen} onOpenChange={setNavOpen} controls="nav" size="sm" />
        {stream === "reconnecting" ? <Reconnecting /> : null}
        {problem ? <Callout tone="danger">{problem}</Callout> : null}
        {main}
      </main>
    </div>
  );
}

/** What a place is called, for the tab: a task's key and, for an agent, which one. */
function placeTitle(place: Place | null, projects: readonly NavProject[] | null): string {
  switch (place?.view) {
    case undefined:
    case "welcome":
      return "";
    case "orgSettings":
      return "Organization settings";
    case "mySettings":
      return "Your settings";
    case "inbox":
      return "Waiting on you";
    // Never a session's title: the tab's words reach teammates as presence.
    case "sessions":
      return "Sessions";
    case "brainstorm":
      return "A session";
    case "projectSettings":
      return [projects?.find((p) => p.id === place.projectId)?.name, "Settings"].filter(Boolean).join(" · ");
    case "tree": {
      if (!projects) return "";
      const { ref } = place;
      if (ref.kind === "project") return projects.find((p) => p.id === ref.id)?.name ?? "";
      if (ref.kind === "epic") {
        for (const p of projects) {
          const epic = p.epics?.find((e) => e.id === ref.id);
          if (epic) return epic.title;
        }
        return "";
      }
      const where = locate(projects, ref.id);
      if (!where) return "";
      const name = where.item.key ?? where.item.title;
      return where.agent !== null ? `${name} · ${where.agent}` : `${name} · ${where.item.title}`;
    }
  }
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

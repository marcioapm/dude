/**
 * A project's running servers (`#/project/<id>/servers`): every live Run's
 * servers, everyone's — task Runs and branch previews — from one read
 * (`GET /v1/projects/:id/servers/running`), grouped by the Run that serves
 * them. The servers connected to this tab come first; that is this tab's
 * knowledge alone (serverWindows.ts), never stored or sent. Listing only:
 * a server is started and stopped from its task's Servers tab.
 */

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { EventTypes, type RunningRun, type RunningServer, type ServerState } from "@dude/domain";
import { Breadcrumb, PersonAvatar, ScreenHeader, Segmented, ServerList, ServerRow } from "@dude/design-system/components";
import { Badge, Button, Callout, EmptyState, SearchField, Spinner, isBareKey } from "@dude/design-system/primitives";
import { describeServer, formatDuration, plural, useNow, useResolvedTheme } from "@dude/design-system";
import type { ApiClient } from "../api/client.ts";
import { usePeople } from "../people.tsx";
import { useEventStream } from "../hooks/useEventStream.ts";
import { errorText } from "../hooks/useSave.tsx";
import { serverKey, tabWindows, type ServerRef } from "../serverWindows.ts";
import { ServerFeedback } from "./ServerFeedback.tsx";

export type ServersFilter = "all" | "preview" | "task";

/** The words search looks in: the server's name, the Run's branch, its task's key and title, who started it. */
export function matches(run: RunningRun, server: RunningServer, q: string): boolean {
  const words = q.trim().toLowerCase();
  if (!words) return true;
  return [server.name, run.branch ?? "", run.task?.key ?? "", run.task?.title ?? "", run.startedBy?.name ?? ""]
    .some((w) => w.toLowerCase().includes(words));
}

export interface ServerGroup {
  readonly run: RunningRun;
  readonly servers: ReadonlyArray<RunningServer>;
}

/**
 * The page's two sections: Runs with a server connected to this tab (those
 * servers first, the one landed on or claimed first of all), then the rest;
 * each searched and filtered.
 */
export function arrange(runs: ReadonlyArray<RunningRun>, connected: ReadonlySet<string>, q: string, filter: ServersFilter,
    first: string | null): { connected: ServerGroup[]; others: ServerGroup[] } {
  const on = (run: RunningRun, s: RunningServer) => connected.has(serverKey({ runId: run.id, server: s.name }));
  const lead = (run: RunningRun, s: RunningServer) => first === serverKey({ runId: run.id, server: s.name });
  const groups = runs.filter((r) => filter === "all" || r.kind === filter)
    .map((run) => ({ run, servers: run.servers.filter((s) => matches(run, s, q)) }))
    .filter((g) => g.servers.length > 0);
  const mine = groups.filter((g) => g.servers.some((s) => on(g.run, s)))
    .map((g) => ({ run: g.run, servers: [...g.servers].sort((a, b) => Number(lead(g.run, b)) - Number(lead(g.run, a))
      || Number(on(g.run, b)) - Number(on(g.run, a))) }))
    .sort((a, b) => Number(a.servers.some((s) => lead(a.run, s)) ? 0 : 1) - Number(b.servers.some((s) => lead(b.run, s)) ? 0 : 1));
  return { connected: mine, others: groups.filter((g) => !g.servers.some((s) => on(g.run, s))) };
}

type Read = { state: "loading" } | { state: "failed"; why: string } | { state: "ok"; runs: RunningRun[] };

/** The events that change the list: a Run's servers, a preview's feedback, a task's feedback. */
const LIST_EVENTS: ReadonlySet<string> = new Set([EventTypes.ServersChanged, EventTypes.RunCreated, EventTypes.RunCompleted,
  EventTypes.RunFailed, EventTypes.RunAborted, EventTypes.PreviewFeedbackSubmitted, EventTypes.PreviewFeedbackResolved,
  EventTypes.PreviewFeedbackReopened, EventTypes.FeedbackSubmitted, EventTypes.FeedbackResolved, EventTypes.FeedbackReopened,
  EventTypes.FeedbackReplied, EventTypes.FeedbackDelivered]);

/** The events after which a branch preview's windows are checked again at once: its Run ended, or its servers changed. */
const RECHECK_EVENTS: ReadonlySet<string> = new Set([EventTypes.ServersChanged, EventTypes.RunCompleted, EventTypes.RunFailed, EventTypes.RunAborted]);

/** The one read, and again on a change the project's stream says (and once when the stream comes back). */
export function useRunningServers(client: ApiClient, projectId: string) {
  const [read, setRead] = useState<Read>({ state: "loading" });
  const reads = useRef(0);
  const load = useCallback(async () => {
    const n = ++reads.current;
    try {
      const { runs } = await client.runningServers(projectId);
      if (n === reads.current) setRead({ state: "ok", runs });
    } catch (err) {
      if (n === reads.current) setRead((r) => (r.state === "ok" ? r : { state: "failed", why: errorText(err) }));
    }
  }, [client, projectId]);
  const retry = useCallback(() => {
    setRead({ state: "loading" });
    void load();
  }, [load]);
  useEffect(() => {
    setRead({ state: "loading" });
    void load();
  }, [load]);
  const stream = useEventStream({ client, projectId, live: true, limit: 50 });
  const seen = useRef(0);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => {
    const fresh = stream.events.filter((e) => e.cursor > seen.current);
    if (stream.events.length) seen.current = Math.max(seen.current, ...stream.events.map((e) => e.cursor));
    if (fresh.some((e) => LIST_EVENTS.has(e.eventType)) && timer.current === undefined) {
      timer.current = setTimeout(() => { timer.current = undefined; void load(); }, 300);
    }
  }, [stream.events, load]);
  useEffect(() => {
    if (stream.reconnects > 0) void load();
  }, [stream.reconnects, load]);
  useEffect(() => () => clearTimeout(timer.current), []);
  return { read, retry, events: stream.events };
}

/** A server as describeServer reads one: what the page has of lux's object. */
const asRunServer = (s: RunningServer) => ({
  name: s.name, port: s.port, command: null, workdir: "", env: {}, fromSpec: true, state: s.state as ServerState, since: s.since ?? "",
  readySince: s.readySince, stopReason: null, stoppedEpoch: null, epoch: 0, url: s.url, ...(s.serverState ? { serverState: s.serverState } : {}),
});

/** Where this tab came from, when a branch preview's Connect to dude opened it on this page. */
export interface Landing extends ServerRef {
  readonly branch: string;
  readonly origin: string;
}

export function ProjectServersScreen({ client, projectId, projectName, landing, onBoard, onTaskFeedback }: {
  client: ApiClient;
  projectId: string;
  projectName: string;
  landing?: Landing | null;
  onBoard: () => void;
  onTaskFeedback: (taskId: string) => void;
}) {
  const people = usePeople();
  const theme = useResolvedTheme();
  const { read, retry, events } = useRunningServers(client, projectId);
  const [q, setQ] = useState("");
  const [filter, setFilter] = useState<ServersFilter>("all");
  const [folds, setFolds] = useState<ReadonlySet<string>>(new Set());
  const search = useRef<HTMLInputElement>(null);
  const ctx = useMemo(() => ({ you: people.you, theme,
    name: (id: string | null | undefined) => (id ? (people.names.get(id) ?? "Someone").split(" ")[0]! : "Someone") }), [people.you, people.names, theme]);
  const { windows, subscribe } = useMemo(() => tabWindows(window, client, ctx), [client]); // eslint-disable-line react-hooks/exhaustive-deps -- ctx is set below
  windows.context = ctx;
  const [, setVersion] = useState(0);
  useEffect(() => subscribe(() => setVersion((v) => v + 1)), [subscribe]);
  const now = useNow(true, 30_000);

  // `/` outside a field reaches the search.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!isBareKey(e, "/")) return;
      e.preventDefault();
      search.current?.focus();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // Arrived from a branch preview's Connect to dude: its server's target is made, so the preview's next hello is adopted.
  const landingRun = useMemo((): RunningRun | null => {
    if (!landing) return null;
    const listed = read.state === "ok" ? read.runs.find((r) => r.id === landing.runId) : undefined;
    return listed ?? { id: landing.runId, kind: "preview", task: null, branch: landing.branch, state: "running", startedAt: null, startedBy: null,
      servers: [{ name: landing.server, port: 0, url: landing.origin, state: "ready", since: null, readySince: null, feedback: { open: 0, resolved: 0, authors: 0 } }] };
  }, [landing, read]);
  useEffect(() => {
    if (landingRun && landing) windows.expect(landingRun, landing.server);
  }, [windows, landingRun, landing]);

  // A server's feedback changed: an open window's pins follow. A branch preview's Run ended or its servers changed: its windows are checked again.
  const pinned = useRef(0);
  useEffect(() => {
    for (const e of events) {
      if (e.cursor <= pinned.current) continue;
      pinned.current = e.cursor;
      const p = e.payload as { previewRunId?: string; server?: string };
      if (e.eventType.startsWith("preview_feedback.") && p.previewRunId && p.server) void windows.reload({ runId: p.previewRunId, server: p.server });
      if (e.runId && RECHECK_EVENTS.has(e.eventType)) windows.recheck(e.runId);
    }
  }, [events, windows]);

  const connected = windows.connected();
  const landed = landing ? serverKey(landing) : null;
  const claimed = windows.claimed().at(-1) ?? null;
  const first = claimed ?? landed;
  const runs = read.state === "ok" ? read.runs : read.state === "failed" ? windows.connectedRuns() : [];
  const { connected: mine, others } = arrange(runs, connected, q, filter, first);
  const total = read.state === "ok" ? read.runs.reduce((n, r) => n + r.servers.length, 0) : 0;
  const label = (k: string) => {
    const [runId, server] = [k.slice(0, k.lastIndexOf("/")), k.slice(k.lastIndexOf("/") + 1)];
    const run = runs.find((r) => r.id === runId) ?? (landingRun?.id === runId ? landingRun : null);
    return `${run?.branch ?? run?.task?.key ?? "this preview"} / ${server}`;
  };

  const toggle = (k: string) => setFolds((f) => {
    const next = new Set(f);
    if (!next.delete(k)) next.add(k);
    return next;
  });

  const group = ({ run, servers }: ServerGroup): ReactNode => (
    <section key={run.id} className="serverGroup" data-testid="server-group" data-run={run.id}
      aria-label={run.task ? `${run.task.key} servers` : `Branch preview ${run.branch ?? ""}`}>
      <header className="serverGroupHead">
        {run.kind === "preview"
          ? <Badge size="sm" tone="neutral" icon="eye">Branch preview</Badge>
          : <Badge size="sm" tone="neutral" icon="agent" mono>{run.task?.key ?? "Task"}</Badge>}
        {run.branch ? <code className="serverGroupBranch" title={run.branch}>{run.branch}</code> : null}
        {run.task ? <span className="serverGroupTitle">{run.task.title}</span> : null}
        <span className="serverGroupGrow" />
        {run.startedBy ? (
          <span className="serverGroupWho">
            <PersonAvatar person={{ ...(people.byId.get(run.startedBy.id) ?? {}), id: run.startedBy.id, name: run.startedBy.name }} size={16} />
            {run.startedBy.name.split(" ")[0]}{run.startedAt ? ` · ${formatDuration(Math.max(0, now - Date.parse(run.startedAt)), { style: "age" })} ago` : ""}
          </span>
        ) : null}
      </header>
      <ServerList>
        {servers.flatMap((s) => {
          const k = serverKey({ runId: run.id, server: s.name });
          const words = describeServer(asRunServer(s), now);
          const here = connected.has(k);
          const open = folds.has(k);
          const moved = !here && windows.moved().has(k);
          return [
            <ServerRow key={k} name={s.name} port={s.port} state={words.state} stateLabel={words.label} url={s.url}
              className={k === first && here ? "serverLanded" : undefined} data-testid="project-server" data-key={k}
              detail={<>
                {words.detail}
                {s.feedback.authors > 1 ? <span className="serverMuted" data-testid="commenting-count">{` · ${s.feedback.authors} people commenting`}</span> : null}
                {here ? <> <Badge size="sm" tone="info" dot data-testid="link-here">{k === first && landed === k ? "Connected just now" : "Connected · this tab"}</Badge></> : null}
                {moved ? <> <Badge size="sm" tone="neutral" data-testid="link-moved">Moved to another of your tabs</Badge></> : null}
              </>}
              onOpen={() => windows.open(run, s)}
              openLabel={here ? "Shows its window, connected to this tab" : "Opens with the comment tool, connected to this tab"}
              menu={
                <Button size="sm" variant={open ? "secondary" : "quiet"} trailingIcon={open ? "chevron-up" : "chevron-down"} aria-expanded={open}
                  data-testid="server-fold-toggle" onClick={() => toggle(k)}>
                  {`${s.feedback.open} open · ${s.feedback.resolved} resolved`}
                </Button>
              } />,
            open ? (
              <li key={`${k}-fold`} className="serverFoldItem">
                <ServerFeedback client={client} run={run} server={s} events={events} you={people.you}
                  onTaskFeedback={run.task ? () => onTaskFeedback(run.task!.id) : undefined} />
              </li>
            ) : null,
          ];
        })}
      </ServerList>
    </section>
  );

  const nothing = read.state === "ok" && read.runs.length === 0;
  const visible = mine.length + others.length;
  return (
    <div className="screen projectServers" data-testid="servers-page">
      <ScreenHeader lead={<Breadcrumb items={[{ id: projectId, label: projectName, onSelect: onBoard }, { id: "servers", label: "Servers" }]} />}
        meta={read.state === "ok" ? <span>{nothing ? "nothing running" : `${total} running on ${plural(read.runs.length, "run")}`}</span> : undefined} />
      <div className="screenBody projectServersBody">
        {claimed ? (
          <Callout tone="info" data-testid="servers-moved">
            Your <b>{label(claimed)}</b> window was connected to another of your dude tabs. It's connected to this one now.
          </Callout>
        ) : null}
        {landed && connected.has(landed) && !claimed ? (
          <Callout tone="success" data-testid="servers-landed">
            <b>{label(landed)}</b> is connected to this tab. Keep it open: its comments go through here. Other servers you open from this page join it.
          </Callout>
        ) : null}
        <div className="projectServersTools">
          <SearchField ref={search} className="projectServersSearch" label="Search servers" placeholder="Search by server, branch, task or person"
            shortcut="/" value={q} onValueChange={setQ} data-testid="servers-search" />
          <Segmented size="sm" label="Which" value={filter} onChange={setFilter}
            options={[{ value: "all", label: "All" }, { value: "preview", label: "Branch previews" }, { value: "task", label: "Tasks" }]} />
        </div>
        {read.state === "loading" ? (
          <div className="projectServersCenter" data-testid="servers-loading"><Spinner size={20} /> <span className="serverMuted">Reading the project's servers…</span></div>
        ) : nothing ? (
          <EmptyState icon="globe" title={`Nothing is running in ${projectName}`} data-testid="servers-none"
            description="Servers show here while a task's run or a branch preview serves them. Start them from a task's Servers tab, or start a branch preview with the API or CLI." />
        ) : (
          <>
            {read.state === "failed" ? (
              <Callout tone="danger" fill data-testid="servers-failed">
                <span className="projectServersFailed">
                  <span>Couldn't read the project's servers. The ones connected to this tab still work.</span>
                  <Button size="sm" variant="secondary" leadingIcon="retry" onClick={retry}>Try again</Button>
                </span>
              </Callout>
            ) : null}
            {mine.length ? (
              <div className="projectServersBlock" data-testid="servers-connected">
                <h2 className="ds-label">Connected to this tab</h2>
                {mine.map(group)}
              </div>
            ) : null}
            {read.state === "ok" && others.length ? (
              <div className="projectServersBlock" data-testid="servers-others">
                <h2 className="ds-label">{mine.length ? "Everything else running" : "Running"}</h2>
                {others.map(group)}
              </div>
            ) : null}
            {read.state === "ok" && visible === 0 ? (
              <EmptyState icon="search" title={`No server matches “${q}”`} data-testid="servers-nomatch"
                description="Search looks at the server's name, its branch, its task's key and title, and who started it."
                action={<Button size="sm" variant="secondary" onClick={() => setQ("")}>Clear search</Button>} />
            ) : null}
          </>
        )}
      </div>
    </div>
  );
}

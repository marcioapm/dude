/**
 * One task: what was asked, who is on it, where it stands, and what each
 * agent did about it.
 *
 * The header says what it is and who it is for: where it sits, its title,
 * its pull requests' states, and its people (the owner with the agent
 * working for them on their face). Below it, tabs in the order a person
 * asks: Overview (the goal, the pipeline, time and cost, and the pull
 * requests beside them), Findings (every review finding), Sessions (every
 * agent that ran down the left, the one open beside them: its conversation
 * and its changes), Files (what they left), and Activity (who did what, by
 * name).
 *
 * A task started over has several attempts, and the page shows one: the
 * current one unless another is picked in the header, beside its branch.
 * The header's status, branch and pull requests, the pipeline, time and
 * cost, the findings, sessions and files are that attempt's; an earlier
 * one is only to read. Goal, owner, Activity (every attempt) and Servers
 * are the task's whatever attempt is shown.
 *
 * Driven by the task's event stream, so a phase starting, a finding
 * landing or the PR opening appears without a reload; a dropped stream
 * is said by the shell, and the page re-reads when it is back.
 */

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import {
  AgentAvatar,
  Duration,
  FindingGroup,
  FindingRow,
  Markdown,
  PersonAvatar,
  PersonLine,
  PlanMeter,
  PrChip,
  PullRequestPanel,
  SessionItem,
  SessionList,
  StatusMark,
  StepList,
  StepRow,
  Timeline,
  TimelineItem,
  planProgress,
} from "@dude/design-system/components";
import { Button, Callout, EmptyState, LinkButton, Select, Spinner, Tab, TabList, TabPanel, Tabs } from "@dude/design-system/primitives";
import { formatUsd, plural, type IconName } from "@dude/design-system";
import { DEFAULT_RUN_ROLE, EventTypes, TERMINAL_RUN_STATUSES, isConductor, runLabel, type PersistedEvent } from "@dude/domain";
import type { ApiClient, Artifact, Finding, MergeMethod, PullRequest, Run, TaskDetail } from "../api/client.ts";
import { ApiError } from "../api/client.ts";
import { actorName, humanActor, planFrom } from "../api/conversation.ts";
import { shortError } from "../escalation.ts";
import { AGENT_CHATTER, cameBack, useReloadOnEvents } from "../hooks/useEventStream.ts";
import { useServers } from "../hooks/useServers.ts";
import { firstName } from "@dude/design-system";
import { usePeople, type People } from "../people.tsx";
import { FilesSection } from "./FilesSection.tsx";
import { EscalationPanel } from "./EscalationPanel.tsx";
import { TaskMetricsSection } from "./MetricsSection.tsx";
import { NotFound } from "./NotFound.tsx";
import { DudeMark, dudeName } from "../DudeMark.tsx";
import { RunScreen, type StoppedRun } from "./RunScreen.tsx";
import { ChatSection } from "./ChatSection.tsx";
import { EndedLedgers } from "./endedLedgers.ts";
import { OwnerSelect } from "./OwnerSelect.tsx";
import { ServersAside } from "./ServersAside.tsx";
import { ServersSection, serversTab } from "./ServersSection.tsx";
import { existingTask, TaskDialog } from "./TaskDialog.tsx";
import { PullRequestActions } from "./PullRequestActions.tsx";
import { PickUpDialog, StoppedNotice, stopOf, useRecoveryOptions } from "./Recovery.tsx";
import type { RecoverAction } from "../api/client.ts";
import { pullRequestActivity } from "../pullRequests.ts";
import { attemptScoped, formatPlace, type TaskTab } from "../place.ts";
import { attemptOfPr, attemptOfRun, attemptOfWork, attemptStarts, attemptStatus, attemptsOf, closedAtStartOver, eventAttempts, prOfEvent, runsById, setAsideOf, type RunsById, type SetAside } from "../attempts.ts";

export interface TaskScreenProps {
  client: ApiClient;
  taskId: string;
  /** A session to show open on the Sessions tab: the page opens there, on its attempt. */
  runId?: string | undefined;
  onOpenRun: (runId: string) => void;
  /**
   * The page moved to another tab or attempt: the URL should say the tab,
   * and the attempt when it is not the current one. `replace` when only the
   * tab changed, so Back goes to where the page was before, not each tab.
   */
  onNavigate?: ((tab: TaskTab | undefined, attempt: number | undefined, replace: boolean) => void) | undefined;
  /** The tab the URL names, to open on. */
  tab?: TaskTab | undefined;
  /** The attempt the URL names; none is the current one. */
  attempt?: number | undefined;
  /** Where it sits, shown at the top: Project › Epic › KEY. */
  breadcrumb?: ReactNode;
  /** Leave for somewhere that exists, when this task does not. */
  onBack: () => void;
}

/** A run's plan, as its latest `agent.plan.updated` left it: for the running step's line. */
type Plans = ReadonlyMap<string, { done: number; total: number; current: string | null }>;

/**
 * The servers.changed that start, end, park or wake a preview run, or
 * carry its lux state (running, or failed on its own): the Sessions tab
 * lists it, so the task is read again. Not a server's own state change,
 * which only the servers re-read for.
 */
const RUN_CHANGES: ReadonlySet<string> = new Set(["created", "submitted", "stopped", "failed", "parked", "resumed"]);
function changesRun(payload: unknown): boolean {
  const p = (payload ?? {}) as { change?: unknown; luxState?: unknown };
  return RUN_CHANGES.has(String(p.change)) || typeof p.luxState === "string";
}

const urlTab = (tab: string): TaskTab | undefined => (tab === "overview" ? undefined : (tab as TaskTab));
const ago = (at: string) => <Duration ms={Math.max(0, Date.now() - Date.parse(at))} format="age" tone="muted" />;
const phasesOf = (runs: readonly Run[], attempt: number) =>
  runs.filter((r) => r.phase && r.attempt === attempt).sort((a, b) => a.createdAt.localeCompare(b.createdAt));
const fileCount = (list: readonly Artifact[]) => new Set(list.map((a) => a.name)).size;

export function TaskScreen({ client, taskId, runId, onOpenRun, onNavigate, tab: openTab, attempt: urlAttempt, breadcrumb, onBack }: TaskScreenProps) {
  // A session's URL is the Sessions tab with it open, on its attempt; the
  // task's URL names the tab and the attempt when it is not the current
  // one. A tab or attempt picked here is written back. A URL naming no tab
  // opens on Chat once someone has written in it, Overview until then
  // (null: that default).
  const [chosenTab, setTab] = useState<string | null>(openTab ?? null);
  // The attempt picked, null for the current one. Kept on Activity and
  // Servers, whose URLs never name one: back on a tab that shows one
  // attempt, it is still the one shown.
  const [chosenAttempt, setChosenAttempt] = useState<number | null>(attemptScoped(openTab) ? (urlAttempt ?? null) : null);
  // A session left here before the URL says so (or with no URL to say it).
  const [leftRun, setLeftRun] = useState<string | undefined>(undefined);
  const [lastPlace, setLastPlace] = useState({ runId, openTab, urlAttempt });
  // Where this page last sent the URL: arriving there keeps the tab picked,
  // Overview too, which the URL does not name.
  const [sent, setSent] = useState<{ tab: TaskTab | undefined; attempt: number | undefined } | null>(null);
  if (lastPlace.runId !== runId || lastPlace.openTab !== openTab || lastPlace.urlAttempt !== urlAttempt) {
    setLastPlace({ runId, openTab, urlAttempt });
    setLeftRun(undefined);
    setSent(null);
    if (!runId) {
      if (sent === null || sent.tab !== openTab || sent.attempt !== urlAttempt) setTab(openTab ?? null);
      if (attemptScoped(openTab)) setChosenAttempt(urlAttempt ?? null);
    }
  }
  const openedRun = runId && runId !== leftRun ? runId : undefined;
  const asked = openedRun ? "sessions" : chosenTab;
  // For the open session, which is memoised: one function each for the page's life.
  const latest = useRef({ pickTab: (_tab: string) => {}, toCurrent: () => {} });
  const openServers = useCallback(() => latest.current.pickTab("servers"), []);
  const toCurrent = useCallback(() => latest.current.toCurrent(), []);
  // The session shown when none is asked for: the last one open, else one
  // picked the first time Sessions shows (what is running, else the newest)
  // and kept — a phase ending must not swap it under someone reading.
  const [picked, setPicked] = useState<string | null>(null);
  const [item, setItem] = useState<TaskDetail | null>(null);
  const [missing, setMissing] = useState(false);
  const [findings, setFindings] = useState<Finding[]>([]);
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [pullRequests, setPullRequests] = useState<PullRequest[]>([]);
  const [events, setEvents] = useState<PersistedEvent[]>([]);
  const ledger = useRef<PersistedEvent[]>([]);
  const [problem, setProblem] = useState<string | null>(null);
  // The task's ended conductors' ledgers, read once while its page is open.
  const endedLedgers = useMemo(() => new EndedLedgers(client), [client, taskId]); // eslint-disable-line react-hooks/exhaustive-deps -- one per task
  const [delivering, setDelivering] = useState(false);
  const [editing, setEditing] = useState(false);
  const people = usePeople();
  // What the open session shows of its task: the owner, the same object while
  // it is the same person, so the session is not redrawn for every reload here.
  const ownerId = item?.owner?.id ?? null;
  const sessionTask = useMemo(() => ({ owner: item?.owner ?? null }), [ownerId]); // eslint-disable-line react-hooks/exhaustive-deps -- `ownerId` stands for the owner
  // Bumped on each reload, for the sections that read their own data.
  const [version, setVersion] = useState(0);
  // The organization's merge method, read once for every pull request here.
  const [mergeMethod, setMergeMethod] = useState<MergeMethod>("squash");
  useEffect(() => {
    void client.githubSettings().then((s) => setMergeMethod(s.mergeMethod), () => undefined);
  }, [client]);

  // Loads overlap when events come quickly: a slower, older one must not
  // put back what a newer one replaced.
  const loads = useRef(0);
  const applied = useRef(0);
  const load = useCallback(async () => {
    const seq = ++loads.current;
    setVersion((v) => v + 1);
    // The ledger is for who did what and the plans; the page stands without
    // it. Only what is new since the last read is fetched, beside the rest.
    const since = ledger.current.at(-1)?.cursor ?? 0;
    const more = allEvents(client, taskId, since).catch(() => [] as PersistedEvent[]);
    try {
      const [fresh, f, p, a] = await Promise.all([
        client.getTask(taskId),
        client.listFindings(taskId),
        client.listPullRequests(taskId),
        client.listArtifacts(taskId),
      ]);
      if (seq > applied.current) {
        applied.current = seq;
        setItem(fresh);
        setFindings(f.findings);
        setArtifacts(a.artifacts);
        setPullRequests(p.pullRequests);
      }
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) setMissing(true);
      else setProblem(err instanceof Error ? err.message : String(err));
    }
    const last = ledger.current.at(-1)?.cursor ?? 0;
    const unseen = (await more).filter((e) => e.cursor > last);
    if (unseen.length > 0) {
      ledger.current = [...ledger.current, ...unseen];
      setEvents(ledger.current);
    }
  }, [client, taskId]);

  useEffect(() => {
    void load();
  }, [load]);
  const reload = useCallback(() => void load(), [load]);

  // What an agent says and does as it works changes nothing on this page
  // but the open session, which has its own stream: no re-read for those.
  // (Its plan and what it spends still re-read: the pipeline and cost show them.)
  // The servers change on their own stream event: they are re-read, and
  // the rest of the page only for a servers.changed that starts or ends a
  // preview run (RUN_CHANGES), not for each server's state change. A
  // stream that comes back replays nothing, so its return re-reads the
  // servers too: they may have moved while nothing could say so.
  const [serversVersion, setServersVersion] = useState(0);
  const stream = useReloadOnEvents({ client, taskId }, () => void load(), undefined, (e) => {
    if (e.eventType === EventTypes.ServersChanged) {
      setServersVersion((v) => v + 1);
      return !changesRun(e.payload);
    }
    return AGENT_CHATTER.has(e.eventType);
  });
  const wasStream = useRef(stream);
  useEffect(() => {
    if (cameBack(wasStream.current, stream)) setServersVersion((v) => v + 1);
    wasStream.current = stream;
  }, [stream]);
  const servers = useServers(client, { taskId }, serversVersion);
  // A stopped task's ways back, read again whenever the page is.
  const recovery = useRecoveryOptions(client, item);
  const [pickingUp, setPickingUp] = useState<RecoverAction | null>(null);

  const deliver = async () => {
    setDelivering(true);
    setProblem(null);
    try {
      await client.deliver(taskId);
      await load();
    } catch (err) {
      setProblem(err instanceof ApiError ? err.message : "Could not start delivery.");
    } finally {
      setDelivering(false);
    }
  };

  // Every attempt, newest first; the current is the highest. The one shown:
  // the open session's, else the one picked, else the current.
  const attempts = useMemo(() => attemptsOf(item?.runs ?? []), [item]);
  const byId = useMemo(() => runsById(item?.runs ?? []), [item]);
  const current = attempts[0] ?? 1;
  const many = attempts.length > 1;
  const openedRow = openedRun ? byId.get(openedRun) : undefined;
  const shown = openedRow ? attemptOfRun(openedRow, current)
    : chosenAttempt !== null && attempts.includes(chosenAttempt) ? chosenAttempt : current;
  const earlier = shown !== current;

  // A URL naming the current attempt, or one the task never had, says the task alone.
  useEffect(() => {
    if (item && urlAttempt !== undefined && !runId && (urlAttempt === current || !attempts.includes(urlAttempt))) {
      onNavigate?.(openTab, undefined, true);
    }
  }, [item, urlAttempt, runId, current, attempts, openTab, onNavigate]);

  // Phases of the attempt shown, and of the current one, in the order they ran.
  const phases = useMemo(() => phasesOf(item?.runs ?? [], shown), [item, shown]);
  const currentPhases = useMemo(() => phasesOf(item?.runs ?? [], current), [item, current]);

  // The page re-renders on every frame of its stream, agent chatter
  // included: what is worked out from the reads is kept until they change,
  // so the sections memoised on it (Activity, Files) are not redone per frame.
  // One per repository the work changed, in the order they were opened.
  const allPrs = useMemo(() => [...pullRequests].sort((a, b) => a.createdAt.localeCompare(b.createdAt)), [pullRequests]);
  // What each attempt made: its pull requests, findings, files and sessions; the one shown's, and the others' counts.
  const parts = useMemo(() => {
    const ofAttempt = (n: number) => ({
      findings: findings.filter((f) => attemptOfWork(f.runId, byId, current) === n),
      artifacts: artifacts.filter((a) => attemptOfWork(a.runId, byId, current) === n),
    });
    return {
      prs: allPrs.filter((pr) => attemptOfPr(pr, byId, current) === shown),
      mine: ofAttempt(shown),
      others: attempts.filter((n) => n !== shown).map((n) => {
        const theirs = ofAttempt(n);
        return { n, findings: theirs.findings.length, files: fileCount(theirs.artifacts) };
      }),
      // Newest first, the task's conductor above them all.
      sessions: (item?.runs ?? []).filter((r) => attemptOfRun(r, current) === shown)
        .sort((a, b) => Number(isConductor(b)) - Number(isConductor(a)) || b.createdAt.localeCompare(a.createdAt)),
    };
  }, [findings, artifacts, allPrs, item, byId, attempts, current, shown]);

  // What the attempt's pull requests heard, for why a fix ran: a few of the ledger's many.
  const prEvents = useMemo(() => {
    const mine = new Set(parts.prs);
    return events.filter((e) => {
      if (!e.eventType.startsWith("pull_request.")) return false;
      const pr = prOfEvent(e, allPrs);
      return pr !== undefined && mine.has(pr);
    });
  }, [events, parts.prs, allPrs]);

  // How it stopped, read from the runs and the ledger once per change of them.
  const stopOfTask = useMemo(() => (item ? stopOf(item, events, people) : null), [item, events, people]);
  // Why the attempt shown was set aside, when it was.
  const aside = useMemo<SetAside | null>(() => (item && earlier ? setAsideOf(shown, item.runs, events, people) : null), [item, earlier, shown, events, people]);
  // What the open session's end strip says, if it stopped: the same object
  // while it says the same, so the session (memoised) is not redrawn.
  const keptUntil = recovery?.keptUntil ?? null;
  const canPickUp = Boolean(recovery?.actions.length) && (!item?.owner || item.owner.id === people.you);
  const stoppedOn = stopOfTask?.run?.id ?? null;
  const shownRun = openedRun ?? picked;
  const retried = shownRun ? retriedRun(byId.get(shownRun), item?.runs ?? []) : false;
  // Only ever the current attempt's: what stopped the task is a Run of it.
  const pickUpHere = canPickUp && shownRun !== null && shownRun === stoppedOn;
  const openStopped = useMemo<StoppedRun | undefined>(
    () => (earlier ? { setAside: "restart", toCurrent: { attempt: current, go: toCurrent } }
      : retried ? { setAside: "retry" }
      : pickUpHere ? { onPickUp: setPickingUp, keptUntil } : undefined),
    [earlier, current, toCurrent, retried, pickUpHere, keptUntil]);

  // Each running phase's plan: the last it wrote, as the transcript has it.
  const plans = useMemo<Plans>(() => {
    const out = new Map<string, { done: number; total: number; current: string | null }>();
    for (const run of phases) {
      if (run.status !== "running") continue;
      let plan: ReturnType<typeof planFrom> = null;
      for (let i = events.length - 1; i >= 0 && !plan; i--) {
        const e = events[i]!;
        if (e.runId === run.id && e.eventType === EventTypes.PlanUpdated) plan = planFrom(e.payload as Record<string, unknown>);
      }
      if (!plan || plan.length === 0) continue;
      const p = planProgress(plan);
      out.set(run.id, { done: p.done, total: p.total, current: p.current?.content ?? null });
    }
    return out;
  }, [phases, events]);

  if (missing) return <NotFound what="task" onBack={onBack} />;
  if (!item) {
    return <div className="centered">{problem ?? <Spinner label="Loading…" />}</div>;
  }

  // The task's conductors, oldest first: Chat shows each conversation in
  // turn, and the latest takes the next message.
  const conductors = [...item.runs].filter(isConductor).sort((a, b) => a.createdAt.localeCompare(b.createdAt));
  const conductor = conductors.at(-1) ?? null;
  const tab = asked ?? (conductor ? "chat" : "overview");

  // Leaves any open session; the URL is replaced only for a tab change with none open.
  const show = (onTab: string, n: number, replace: boolean) => {
    const next = n === current ? null : n;
    setChosenAttempt(next);
    setTab(onTab);
    if (openedRun) setLeftRun(openedRun);
    const to = { tab: urlTab(onTab), attempt: next !== null && attemptScoped(urlTab(onTab)) ? next : undefined };
    // Only a move the URL will make: a stale one would keep a later visit's tab.
    if (openedRun || to.tab !== openTab || to.attempt !== urlAttempt) setSent(to);
    onNavigate?.(to.tab, to.attempt, replace);
  };
  /** Show attempt `n`: picked in the header, from an empty tab, or the way back to the current one. */
  const pickAttempt = (n: number, onTab: string = tab) => show(onTab, n, false);
  const pickTab = (next: string) => {
    if (next !== tab) show(next, shown, openedRun === undefined);
  };
  latest.current = { pickTab, toCurrent: () => pickAttempt(current) };

  const started = currentPhases.length > 0;
  const stopped = item.status === "aborted" || item.status === "failed";
  // The pick-up is the current attempt's: an earlier one is only to read.
  const stop = stopped && !earlier ? stopOfTask : null;
  // The reader picks it up when it is theirs, or nobody's.
  const yours = !item.owner || (people.you !== null && item.owner.id === people.you);
  const { prs, mine, others, sessions } = parts;
  const owner = item.owner ? (people.byId.get(item.owner.id) ?? item.owner) : null;
  const working = currentPhases.find((r) => r.status === "running");
  // The aside says what serves the task when something does, or could: a
  // project with no servers defined has nothing to say there. Servers are
  // the current attempt's: an earlier one has none to show.
  const showServers = !earlier && Boolean(servers.data && (servers.data.run || servers.data.recipes.length > 0));
  // The one open is the one asked for, else the one picked on first sight
  // (what was running, else the newest).
  const openRun = (openedRun && sessions.some((r) => r.id === openedRun) ? openedRun : undefined)
    ?? (picked && sessions.some((r) => r.id === picked) ? picked : undefined)
    ?? sessions.find((r) => r.status === "running")?.id ?? sessions[0]?.id;
  if (tab === "sessions" && openRun && openRun !== picked) setPicked(openRun);
  const reviewing = phases.some((r) => r.phase === "review" && r.status === "running");
  const othersSaid = (what: "findings" | "files") => others.map((o) => `attempt ${o.n} had ${o[what]}`).join(", ");

  return (
    // On Sessions the page holds still and the session scrolls inside it.
    <div className={tab === "sessions" || tab === "chat" ? "screen taskScreen fixed" : "screen taskScreen"} data-testid="task-screen" data-attempt={shown}>
      <header className="taskTop">
        <div className="taskCrumbs">{breadcrumb}</div>
        <span className="taskTopActions">
          {!started ? (
            <Button variant="primary" leadingIcon="zap" onClick={() => void deliver()} disabled={delivering} data-testid="deliver">
              {delivering ? "Starting…" : "Deliver"}
            </Button>
          ) : null}
          {/* Work that changed no code ends waiting to be read, with no PR to merge. */}
          {!earlier && item.status === "review" && prs.length === 0 && started && currentPhases.every((r) => TERMINAL_RUN_STATUSES.includes(r.status)) ? (
            <Button variant="primary" leadingIcon="check" data-testid="mark-done"
              onClick={() => void client.markDone(taskId).then(() => load(), (err: unknown) => setProblem(err instanceof ApiError ? err.message : "Could not mark it done."))}>
              Mark done
            </Button>
          ) : null}
          <Button variant="quiet" leadingIcon="edit" onClick={() => setEditing(true)} data-testid="edit-task">
            Edit
          </Button>
        </span>
      </header>

      <div className="taskHead" data-testid="task-header">
        <div className="taskHeadMain">
          <h1 className="taskTitle">{item.title}</h1>
          <div className="taskMeta">
            {/* The task's status, or on an earlier attempt how that attempt ended. */}
            <StatusMark status={attemptStatus(item, shown, current)} size="sm" data-testid="header-status" />
            {prs.map((pr) => (
              <PrChip key={pr.id} pr={pr} data-testid="pr-link" />
            ))}
            {item.key ? <span className="ds-mono" title={item.id}>{item.key}</span> : null}
            {many ? (
              <AttemptPicker client={client} taskId={taskId} item={item} byId={byId} attempts={attempts} prs={allPrs} events={events} people={people}
                value={shown} onChange={(n) => pickAttempt(n)} />
            ) : null}
            {phases[0]?.branch ? <span className="ds-mono" data-testid="branch">{phases[0].branch}</span> : null}
            <span>created {ago(item.createdAt)} ago</span>
          </div>
        </div>
        <div className="taskPeople">
          {owner ? (
            <PersonLine person={owner} size={56} {...(working ? { agent: working.role ?? DEFAULT_RUN_ROLE, live: true } : {})}
              detail={working ? `Owner · ${firstName(owner.name)}'s ${runLabel(working).toLowerCase()} is working` : "Owner"} />
          ) : null}
          <OwnerSelect client={client} task={item} onChanged={() => void load()} onProblem={setProblem} />
        </div>
      </div>

      {item.escalation || stop || aside || problem ? (
        <div className="taskNotices">
          {aside ? <EarlierBar attempt={shown} current={current} aside={aside} onCurrent={() => pickAttempt(current)} /> : null}
          {item.escalation && !earlier ? (
            <EscalationPanel client={client} task={{ ...item, escalation: item.escalation }} you={people.you}
              onOpenRun={onOpenRun} onDecided={() => void load()} />
          ) : null}
          {stop ? (
            <StoppedNotice task={item} stop={stop} options={recovery} owner={owner?.name ?? null} you={yours}
              onOpenRun={onOpenRun} onChoose={setPickingUp} />
          ) : null}
          {problem ? <Callout tone="danger">{problem}</Callout> : null}
        </div>
      ) : null}

      {editing ? (
        <TaskDialog client={client} projectId={item.projectId} onClose={() => setEditing(false)} existing={existingTask(item, started)} onSaved={() => void load()} />
      ) : null}
      {pickingUp && stop && recovery ? (
        <PickUpDialog client={client} task={item} stop={stop} options={recovery} initial={pickingUp}
          onEdit={() => {
            setPickingUp(null);
            setEditing(true);
          }}
          onClose={() => setPickingUp(null)}
          onDone={(action) => {
            setPickingUp(null);
            void load();
            // A resume goes on in the session that stopped; the rest start new ones.
            if (action === "resume" && stop.run) onOpenRun(stop.run.id);
          }} />
      ) : null}

      <Tabs value={tab} onValueChange={pickTab} fill>
        <TabList aria-label="Task" className="tabsInset">
          <Tab value="chat">Chat</Tab>
          <Tab value="overview">Overview</Tab>
          <Tab value="findings" count={mine.findings.length > 0 ? mine.findings.length : undefined}
            tooltip={many ? <>Attempt {shown}'s findings; {othersSaid("findings")}</> : undefined}>Findings</Tab>
          <Tab value="sessions" count={sessions.length > 0 ? sessions.length : undefined}
            tooltip={many ? <>Attempt {shown}'s sessions</> : undefined}>Sessions</Tab>
          <Tab value="files" count={mine.artifacts.length > 0 ? fileCount(mine.artifacts) : undefined}
            tooltip={many ? <>Attempt {shown}'s files; {othersSaid("files")}</> : undefined}>Files</Tab>
          <Tab value="servers" {...serversTab(servers.data)}>Servers</Tab>
          <Tab value="activity" tooltip={many ? "Every attempt" : undefined}>Activity</Tab>
        </TabList>

        <TabPanel value="chat" fill>
          <ChatSection client={client} task={item} conductorId={conductor?.id ?? null}
            earlier={conductors.slice(0, -1).map((r) => ({ id: r.id, status: r.status }))} ledgers={endedLedgers}
            findings={findings} pullRequests={pullRequests}
            events={events} owner={sessionTask} version={version} onSent={reload} onBack={onBack} />
        </TabPanel>

        <TabPanel value="overview" className="taskPane">
          <div className="taskOverview">
            <div className="taskMain">
              {item.goal ? (
                <section className="taskBlock" aria-label="Goal">
                  <h2 className="ds-label">Goal</h2>
                  <div className="taskGoal">
                    {/* Written in Markdown in the task dialog: shown as its preview showed it. */}
                    <Markdown source={item.goal} breaks />
                  </div>
                </section>
              ) : null}
              {item.acceptanceCriteria.length > 0 ? (
                <section className="taskBlock" aria-label="Acceptance criteria">
                  <h2 className="ds-label">Acceptance criteria</h2>
                  <ul aria-label="Acceptance criteria" className="taskGoal taskCriteria">
                    {item.acceptanceCriteria.map((c, i) => (
                      <li key={i}><Markdown source={c} breaks /></li>
                    ))}
                  </ul>
                </section>
              ) : null}

              <section className="taskBlock" aria-label="Pipeline">
                <h2 className="ds-label">Pipeline{many ? ` · attempt ${shown}` : ""}</h2>
                {phases.length > 0 ? (
                  <StepList data-testid="pipeline">
                    {phases.map((run, index) => (
                      <PhaseStep key={run.id} run={run} plan={plans.get(run.id)} why={whyItRan(run, index, phases, findings, prEvents)}
                        findings={findings.filter((f) => f.runId === run.id)} onOpen={() => onOpenRun(run.id)} />
                    ))}
                    {prs.map((pr) => (
                      <PullRequestStep key={pr.id} pr={pr} named={prs.length > 1} />
                    ))}
                  </StepList>
                ) : (
                  <EmptyState compact icon="git-pr" title="Not started"
                    description="Deliver runs an implementer, reviewers, a fixer if they find problems, a simplifier, and opens a pull request." />
                )}
              </section>

              <TaskMetricsSection client={client} taskId={taskId} attempt={many ? shown : undefined} setAside={earlier}
                live={!earlier && item.status === "running"}
                done={["done", "failed", "aborted"].includes(item.status)} version={version} />
            </div>
            {prs.length > 0 || showServers ? (
              <aside className="taskAside" aria-label="Pull requests and servers">
                {prs.map((pr) => earlier ? (
                  // An earlier attempt's pull request is only to read: no Merge, no requests.
                  <PullRequestPanel key={pr.id} pr={pr} data-testid="pr-panel" data-pr={pr.id}
                    note={pr.state === "closed" && aside?.at ? <ClosedNote pr={pr} events={events} at={aside.at} by={aside.by} attempt={shown} taskId={taskId} /> : undefined}
                    actions={<LinkButton href={pr.url}>Open on GitHub</LinkButton>} />
                ) : (
                  <PullRequestActions key={pr.id} client={client} pr={pr} defaultMethod={mergeMethod} onChanged={() => void load()}>
                    {(actions) => (
                      <PullRequestPanel pr={pr} data-testid="pr-panel" data-pr={pr.id}
                        factActions={actions.facts} factUnder={actions.under} note={actions.note}
                        diagnosticAction={<a href={formatPlace({ view: "orgSettings", page: "github" })}>GitHub settings</a>}
                        actions={
                          <>
                            {actions.merge}
                            <LinkButton href={pr.url}>Open on GitHub</LinkButton>
                          </>
                        } />
                    )}
                  </PullRequestActions>
                ))}
                {showServers ? (
                  <ServersAside client={client} taskId={taskId} servers={servers} onAll={() => pickTab("servers")} />
                ) : null}
              </aside>
            ) : null}
          </div>
        </TabPanel>

        <TabPanel value="findings" className="taskPane">
          {mine.findings.length > 0 ? (
            <FindingGroup data-testid="findings" findings={mine.findings}
              renderRow={(f) => (
                <FindingRow key={f.id} data-testid="finding" data-status={f.status} severity={f.severity} status={f.status}
                  category={f.category} title={f.title} file={f.file} line={f.line} description={f.description}
                  suggestedFix={f.suggestedFix} resolutionNote={f.resolutionNote} fixAttempts={f.fixAttempts}
                  fixedIn={f.resolvedByRunId ? resolvedIn(item, f.resolvedByRunId, onOpenRun) : undefined} />
              )} />
          ) : many ? (
            <Elsewhere icon="check" title={`No findings in attempt ${shown}${reviewing ? " yet" : ""}`}
              description={reviewing ? "Its reviewers are still at work." : phases.some((r) => r.phase === "review") ? "Its reviewers raised nothing." : "Reviewers report here once its review starts."}
              others={others.map((o) => ({ n: o.n, count: o.findings }))} what="finding" onAttempt={(n) => pickAttempt(n)} />
          ) : (
            <EmptyState compact icon="check" title="No findings" description={started ? "The reviewers raised nothing, yet." : "Reviewers report here once delivery starts."} />
          )}
        </TabPanel>

        <TabPanel value="sessions" fill>
          {sessions.length > 0 ? (
            <div className="taskSessions">
              <div className="taskSessionList" data-testid="sessions">
                <SessionList>
                  {sessions.map((run) => (
                    <SessionItem key={run.id} onOpen={() => onOpenRun(run.id)} current={run.id === openRun} data-testid="session"
                      avatar={<AgentAvatar role={run.role ?? DEFAULT_RUN_ROLE} size="lg" live={run.status === "running"} />}
                      title={runLabel(run) + (againOf(run, sessions) ? " · again" : "")}
                      detail={<>{run.model ?? run.harness ?? "agent"} · {run.startedAt ? <Duration since={run.startedAt} until={run.endedAt} live={run.status === "running"} tone="muted" /> : "not started"}</>}
                      trailing={<StatusMark status={run.status} size="sm" iconOnly={run.status === "completed"} />} />
                  ))}
                </SessionList>
              </div>
              {openRun ? (
                <RunScreen key={openRun} client={client} runId={openRun} onBack={onBack} task={sessionTask} onOpenServers={openServers}
                  stopped={openStopped} />
              ) : null}
            </div>
          ) : (
            <div className="taskPane">
              <EmptyState compact icon="agent" title="No sessions yet" description="Each agent that works on the task has a session here, with its conversation." />
            </div>
          )}
        </TabPanel>

        <TabPanel value="files" className="taskPane">
          {mine.artifacts.length > 0 ? (
            <FilesSection client={client} taskId={taskId} taskKey={item.key} artifacts={mine.artifacts} onOpenRun={onOpenRun} />
          ) : many ? (
            <Elsewhere icon="file" title={`No files in attempt ${shown}${earlier ? "" : " yet"}`}
              description="What its agents save — notes, screenshots, reports, recordings — shows here."
              others={others.map((o) => ({ n: o.n, count: o.files }))} what="file" onAttempt={(n) => pickAttempt(n)} />
          ) : (
            <EmptyState compact icon="file" title="No files yet" description="What the agents save — notes, screenshots, reports, recordings — shows here." />
          )}
        </TabPanel>

        <TabPanel value="servers" className="taskPane">
          <ServersSection client={client} servers={servers} taskId={taskId} />
        </TabPanel>

        <TabPanel value="activity" className="taskPane">
          <Activity events={events} people={people} runs={item.runs} prs={allPrs} current={current} many={many} shown={shown}
            onOpenRun={onOpenRun} onShow={(n, t) => pickAttempt(n, t)} />
        </TabPanel>
      </Tabs>
    </div>
  );
}

/**
 * The page's attempt, in the header before its branch: a `Select` whose
 * options say, besides the number, how each ended (its mark), "current" or
 * "set aside", and under it in the list when it started or was set aside
 * and how, its branch, its pull requests and what it cost.
 */
function AttemptPicker({ client, taskId, item, byId, attempts, prs, events, people, value, onChange }: {
  client: ApiClient; taskId: string; item: TaskDetail; byId: RunsById; attempts: readonly number[]; prs: readonly PullRequest[];
  events: readonly PersistedEvent[]; people: People; value: number; onChange: (n: number) => void;
}) {
  // What each attempt cost: its Runs' costs, from one read of the task's
  // each time the list opens; nothing else on the page needs them.
  const [costs, setCosts] = useState<ReadonlyMap<string, number>>(new Map());
  const reads = useRef(0);
  const readCosts = (open: boolean) => {
    if (!open) return;
    const seq = ++reads.current;
    void client.taskMetrics(taskId).then((m) => seq === reads.current && setCosts(new Map(m.runs.map((r) => [r.id, r.cost.totalUsd]))), () => {});
  };
  const current = attempts[0]!;
  const options = useMemo(() => {
    const starts = attemptStarts(item.runs);
    return attempts.map((n) => {
      const runs = item.runs.filter((r) => r.kind !== "preview" && r.attempt === n);
      const started = starts.get(n);
      const aside = n === current ? null : setAsideOf(n, item.runs, events, people);
      const branch = runs.find((r) => r.branch)?.branch;
      const mine = prs.filter((pr) => attemptOfPr(pr, byId, current) === n);
      const cost = runs.reduce((sum, r) => sum + (costs.get(r.id) ?? 0), 0);
      return {
        value: String(n),
        label: <span className="attemptPickLabel"><StatusMark status={attemptStatus(item, n, current)} size="sm" iconOnly />Attempt {n}</span>,
        meta: n === current ? "current" : "set aside",
        description: (
          <>
            {aside ? <>{aside.at ? <>Set aside {ago(aside.at)} ago</> : "Set aside"}{aside.how ? ` · ${aside.how}` : ""}</>
              : started ? <>Started {ago(started)} ago</> : "Not started"}
            {branch ? <> · <span className="ds-mono">{branch}</span></> : null}
            {mine.length > 0 ? <> · {mine.map((pr) => `PR #${pr.number} ${pr.state}`).join(", ")}</> : null}
            {costs.size > 0 ? <> · {formatUsd(cost)}</> : null}
          </>
        ),
      };
    });
  }, [attempts, item, byId, prs, events, people, costs, current]);
  return (
    <Select<string> size="sm" aria-label="Attempt" value={String(value)} onValueChange={(v) => onChange(Number(v))} onOpenChange={readCosts}
      options={options} className="attemptPicker" data-testid="attempt-picker"
      footer="The whole page shows the attempt chosen. Activity always shows every attempt." />
  );
}

/**
 * On an earlier attempt, in the escalation's place: who set it aside,
 * when, their note, how it had ended, and the way to the current one.
 * Neutral: nothing is wrong and nobody is needed.
 */
function EarlierBar({ attempt, current, aside, onCurrent }: { attempt: number; current: number; aside: SetAside; onCurrent: () => void }) {
  return (
    <Callout tone="neutral" data-testid="earlier-bar">
      <div className="escalation">
        <p>
          <strong>Attempt {attempt} was set aside</strong>
          {aside.at ? <> {ago(aside.at)} ago</> : null}
          {aside.by ? <>, when <b>{aside.by}</b> started over</> : null}
          {aside.note ? <>: “{aside.note}”</> : "."}
        </p>
        <p className="earlierSecond">
          {aside.how ? `It had ${aside.how}. ` : null}What it left is here to read; nothing in it can be merged, resumed or steered.{" "}
          <Button size="sm" variant="secondary" trailingIcon="arrow-right" onClick={onCurrent} data-testid="go-current">
            Go to attempt {current} (current)
          </Button>
        </p>
      </div>
    </Callout>
  );
}

/**
 * Under an earlier attempt's closed pull request: dude closed it when the
 * task was started over, or, closed before that (by someone on GitHub),
 * only that it is closed and when the attempt was set aside.
 */
function ClosedNote({ pr, events, at, by, attempt, taskId }: {
  pr: PullRequest; events: readonly PersistedEvent[]; at: string; by: string | null; attempt: number; taskId: string;
}) {
  const closedAt = closedAtStartOver(pr, events, at);
  if (closedAt) return <>Closed by {dudeName(taskId)} {ago(closedAt)} ago, when {by ? firstName(by) : "someone"} started over.</>;
  return <>Closed. Attempt {attempt} was set aside {ago(at)} ago.</>;
}

/** An empty tab of one attempt, and every other attempt that had some, each a way there. */
function Elsewhere({ icon, title, description, others, what, onAttempt }: {
  icon: IconName; title: string; description: string; others: ReadonlyArray<{ n: number; count: number }>;
  what: string; onAttempt: (n: number) => void;
}) {
  return (
    <EmptyState compact icon={icon} title={title}
      description={
        <>
          {description}
          {others.filter((o) => o.count > 0).map((o) => (
            <span key={o.n} className="elsewhere" data-testid="elsewhere" data-attempt={o.n}>
              Attempt {o.n} had {plural(o.count, what)}.{" "}
              <Button size="sm" variant="quiet" trailingIcon="arrow-right" onClick={() => onAttempt(o.n)}>Show attempt {o.n}'s</Button>
            </span>
          ))}
        </>
      } />
  );
}

/** A phase run again in its attempt after one before it stopped: "Implement · again". */
function againOf(run: Run, runs: readonly Run[]): boolean {
  return Boolean(run.phase) && run.phase !== "review" && runs.some((r) => r.id !== run.id && r.phase === run.phase &&
    r.createdAt < run.createdAt && (r.status === "aborted" || r.status === "failed"));
}

/** A stopped session whose step a new session took up again in the same attempt (Try again). */
function retriedRun(run: Run | undefined, runs: readonly Run[]): boolean {
  if (!run || (run.status !== "aborted" && run.status !== "failed")) return false;
  return runs.some((r) => r.attempt === run.attempt && r.phase === run.phase && r.createdAt > run.createdAt);
}

/** A task's events after a cursor, oldest first, in pages. */
async function allEvents(client: ApiClient, taskId: string, after: number): Promise<PersistedEvent[]> {
  const out: PersistedEvent[] = [];
  for (let page = 0; page < 20; page++) {
    const { events, nextCursor } = await client.events({ taskId, after, limit: 1000 });
    out.push(...events);
    if (events.length < 1000) break;
    after = nextCursor;
  }
  return out;
}

/**
 * What woke a step, when the record says: a fix for the review's findings,
 * for a pull request's feedback (and whose), a re-review of a fix. Only
 * what can be read from the runs, findings and events; nothing guessed.
 */
function whyItRan(run: Run, index: number, phases: readonly Run[], findings: readonly Finding[], prEvents: readonly PersistedEvent[]): string | undefined {
  const earlier = phases.slice(0, index);
  if (run.phase === "fix") {
    if (prEvents.some((e) => e.eventType === "pull_request.opened" && e.occurredAt < run.createdAt)) {
      return prFeedbackReason(prEvents, run.createdAt);
    }
    return "for the review";
  }
  if (run.phase === "review" && earlier.some((r) => r.phase === "fix")) {
    const found = findings.filter((f) => f.runId === run.id).length;
    return found === 0 ? "no findings" : undefined;
  }
  return undefined;
}

/** Which pull request feedback, at or before `at`, a fix answered. */
export function prFeedbackReason(prEvents: readonly PersistedEvent[], at: string): string {
  // A checks event whose verdict did not change (read access lost or
  // regained) woke no fixer.
  const feedback = prEvents.findLast((e) => e.occurredAt <= at &&
    (e.eventType === "pull_request.commented" || e.eventType === "pull_request.reviewed" ||
      (e.eventType === "pull_request.checks_changed" && e.payload.to !== e.payload.from)));
  if (feedback?.eventType === "pull_request.checks_changed") return "for failing CI";
  const author = typeof feedback?.payload.author === "string" ? feedback.payload.author : null;
  return author ? `for ${author}'s review` : "for the pull request's feedback";
}

function PhaseStep({ run, findings, plan, why, onOpen }: {
  run: Run; findings: Finding[]; plan: { done: number; total: number; current: string | null } | undefined; why: string | undefined; onOpen: () => void;
}) {
  const blocking = findings.filter((f) => f.severity === "blocking" || f.severity === "high");
  const heads = Object.entries(run.heads);
  const running = run.status === "running";
  const note = run.status === "failed" && run.error
    ? <span title={run.error}>{shortError(run.error, 80)}</span>
    : run.phase === "review" && run.status === "completed"
      ? findings.length === 0 ? "no findings" : `${blocking.length} blocking${findings.length > blocking.length ? `, ${findings.length - blocking.length} more` : ""}`
      : why;
  return (
    <StepRow
      data-testid="phase"
      data-phase={run.phase}
      data-status={run.status}
      onOpen={onOpen}
      avatar={<AgentAvatar role={run.role ?? DEFAULT_RUN_ROLE} size="lg" live={running} />}
      label={runLabel(run)}
      note={note}
      status={<StatusMark status={run.status} size="sm" iconOnly={run.status === "completed"} />}
      meta={heads.length === 0 ? undefined : heads.length === 1 ? heads[0]![1].slice(0, 7) : `${heads.length} repos`}
      metaTitle={heads.length > 0 ? heads.map(([repo, sha]) => `${repo} ${sha}`).join("\n") : undefined}
      duration={run.startedAt ? <Duration since={run.startedAt} until={run.endedAt} live={running} tone="muted" /> : "—"}
      below={running && plan ? (
        <>
          <PlanMeter done={plan.done} total={plan.total} width={72} />
          <span className="ds-tnum">{plan.done} of {plan.total}</span>
          {plan.current ? <span className="planNow">{plan.current}</span> : null}
        </>
      ) : undefined}
    />
  );
}

/**
 * A pull request as the pipeline's last step. With several (work across
 * repositories), each names its repository; they share the branch.
 */
function PullRequestStep({ pr, named }: { pr: PullRequest; named: boolean }) {
  return (
    <StepRow
      data-testid="pr-step"
      data-phase="pr"
      data-status={pr.state}
      href={pr.url}
      avatar={<DudeMark size={32} />}
      label={named ? <><span className="ds-mono">{pr.repositoryName}</span> #{pr.number}</> : <>Pull request #{pr.number}</>}
      note={pr.title}
      status={<PrChip pr={pr} size="sm" showNumber={false} tabIndex={-1} />}
      meta={pr.headBranch}
    />
  );
}

/**
 * Where a finding was fixed, as a way there: the fix the re-review that
 * resolved it judged — the last before that review — as "Fix 2" when
 * there were several, or the review itself when no fix is known.
 */
function resolvedIn(item: TaskDetail, reviewId: string, onOpenRun: (id: string) => void) {
  const review = item.runs.find((r) => r.id === reviewId);
  const fixes = item.runs.filter((r) => r.phase === "fix" && r.attempt === review?.attempt).sort((x, y) => x.createdAt.localeCompare(y.createdAt));
  const index = review ? fixes.findLastIndex((r) => r.createdAt < review.createdAt) : -1;
  const fix = fixes[index];
  const label = fix ? (fixes.length > 1 ? `${runLabel(fix)} ${index + 1}` : runLabel(fix)) : review ? runLabel(review) : "its review";
  return (
    <Button size="sm" variant="quiet" onClick={() => onOpenRun(fix?.id ?? reviewId)} data-testid="finding-fixed-in">
      {label}
    </Button>
  );
}

/**
 * What a person or dude did to the task, newest first, by name: every
 * attempt's, each line marked with its attempt once there are several.
 * A line's way to what it names opens it on that line's attempt.
 */
function Activity({ events, people, runs, prs, current, many, shown, onOpenRun, onShow }: {
  events: readonly PersistedEvent[]; people: People; runs: readonly Run[]; prs: readonly PullRequest[]; current: number;
  many: boolean; shown: number; onOpenRun: (runId: string) => void; onShow: (attempt: number, tab: string) => void;
}) {
  // A task never started over keeps the Activity it had: no attempt marks, ways there, or the lines that carry them.
  const lines = useMemo(() => {
    const all = activityLines(events, people, runs, prs, current);
    return many ? all : all.filter((l) => !l.attemptOnly).map((l): ActivityLine => ({ ...l, open: undefined }));
  }, [events, people, runs, prs, current, many]);
  if (lines.length === 0) return <EmptyState compact icon="list" title="Nothing yet" description="Who did what to the task shows here: deliveries, steers, answers, pull requests." />;
  const now = Date.now();
  return (
    <Timeline data-testid="activity">
      {lines.map((l) => (
        <TimelineItem key={l.id} who={l.who} quote={l.quote} when={<Duration ms={Math.max(0, now - Date.parse(l.at))} format="age" tone="muted" />}
          data-testid="activity-item" data-attempt={l.attempt}>
          {l.text}
          {many ? <span className="lineAttempt" data-shown={l.attempt === shown || undefined}> · attempt {l.attempt}</span> : null}
          {l.open ? (
            <>
              {" "}
              <Button size="sm" variant="quiet" trailingIcon="arrow-right" data-testid="activity-open"
                onClick={() => (l.open!.runId ? onOpenRun(l.open!.runId) : onShow(l.attempt, l.open!.tab))}>
                {l.open.label}
              </Button>
            </>
          ) : null}
        </TimelineItem>
      ))}
    </Timeline>
  );
}

interface ActivityLine {
  id: string;
  at: string;
  attempt: number;
  who: ReactNode;
  text: ReactNode;
  quote?: ReactNode;
  /** A way to what it names: a session, or a tab of its attempt. */
  open?: { label: string; runId?: string; tab: string } | undefined;
  /** Only worth a line once there are attempts to tell apart: findings raised, a file saved. */
  attemptOnly?: boolean;
}

/** The ledger as sentences: the acts worth a line, each with who did it and its attempt. */
export function activityLines(events: readonly PersistedEvent[], people: People, runs: readonly Run[], prs: readonly PullRequest[], current: number): ActivityLine[] {
  const out: ActivityLine[] = [];
  const attemptOf = eventAttempts(runs, prs, current);
  // A task with pull requests in several repositories names each by its repository.
  const named = new Set(events.filter((e) => e.eventType === "pull_request.opened").map((e) => e.payload.repo)).size > 1;
  const labels = new Map(runs.map((r) => [r.id, runLabel(r).toLowerCase()]));
  // One task's events: dude goes by the same name throughout.
  const taskId = events.find((e) => e.taskId)?.taskId;
  const dude = dudeName(taskId ?? "");
  const phase = (runId: string | null) => (runId && labels.get(runId)) || "agent";
  const session = (runId: string | null) => (runId && labels.has(runId) ? { label: "Open session", runId, tab: "sessions" } : undefined);
  for (const e of events) {
    const by = humanActor(e);
    const name = actorName(by, people.names);
    const face = by ? <PersonAvatar person={{ ...(people.byId.get(by.id) ?? {}), id: by.id, name: name ?? "Someone" }} size={32} /> : null;
    const person = <b>{name ?? "Someone"}</b>;
    const p = e.payload;
    // Its attempt is set below, once the event has made a line.
    const base = { id: e.eventId, at: e.occurredAt, attempt: 0 };
    const lines = out.length;
    switch (e.eventType) {
      case "task.created":
        out.push({ ...base, who: face, text: <>{person} created the task</> });
        break;
      case "workflow.transitioned":
        break;
      case "run.steered":
        out.push({ ...base, who: face, text: <>{person} steered the {phase(e.runId)}</>, quote: String(p.text ?? "") });
        break;
      case "question.answered":
        out.push({ ...base, who: face, text: <>{person} answered the {phase(e.runId)}</>, quote: String(p.answer ?? "") });
        break;
      case "run.paused":
        if (by) out.push({ ...base, who: face, text: <>{person} paused the {phase(e.runId)}</> });
        break;
      case "run.resumed":
        if (by) out.push({ ...base, who: face, text: <>{person} resumed the {phase(e.runId)}</> });
        break;
      case "run.aborted":
        out.push({ ...base, who: face ?? <DudeMark size={32} />, text: <>{by ? person : <b>{dude}</b>} aborted the {phase(e.runId)}</>,
          quote: p.reason ? String(p.reason) : undefined, open: session(e.runId) });
        break;
      case "task.recovered":
        out.push({ ...base, who: face, quote: p.note ? String(p.note) : undefined,
          text: <>{person} picked the task back up: {p.action === "resume" ? "resumed where it stopped"
            : p.action === "retry" ? "tried again with a new agent" : <>started over as attempt {String(p.attempt ?? "")}</>}</> });
        break;
      case "task.owner_changed": {
        const to = typeof p.to === "string" ? people.names.get(p.to) : undefined;
        out.push({ ...base, who: face, text: <>{person} handed the task to <b>{to ?? "someone else"}</b></> });
        break;
      }
      case "task.decided":
        out.push({ ...base, who: face, text: <>{person} decided how delivery goes on: {String(p.action ?? "")}</>, quote: p.note ? String(p.note) : undefined });
        break;
      case "question.asked":
        if (p.kind === "agent") out.push({ ...base, who: <AgentAvatar role="implementer" size="lg" />, text: <>The <b>{phase(e.runId)}</b> asked a question</>, quote: String(p.prompt ?? ""), open: session(e.runId) });
        break;
      case "run.created":
        if (typeof p.phase === "string") out.push({ ...base, who: <AgentAvatar role={(typeof p.role === "string" ? p.role : DEFAULT_RUN_ROLE) as Run["role"] & string} size="lg" />, text: <>The <b>{phase(e.runId)}</b> started</>, open: session(e.runId) });
        break;
      case "review.completed": {
        const count = Number(p.count ?? 0);
        if (count > 0) out.push({ ...base, who: <AgentAvatar role="reviewer" size="lg" />, text: <>The <b>{phase(e.runId)}</b> raised {plural(count, "finding")}</>, open: { label: "Show findings", tab: "findings" }, attemptOnly: true });
        break;
      }
      case "artifact.created":
        out.push({ ...base, who: <AgentAvatar role={(runs.find((r) => r.id === e.runId)?.role ?? DEFAULT_RUN_ROLE)} size="lg" />,
          text: <>The <b>{phase(e.runId)}</b> saved <span className="ds-mono">{String(p.name ?? "a file")}</span></>, open: { label: "Show file", tab: "files" }, attemptOnly: true });
        break;
      default: {
        // What happened to a pull request: by the GitHub login that did it,
        // or the person who did it from here, or dude, or GitHub.
        const line = pullRequestActivity(e, named);
        if (!line) break;
        const who = line.actorId ? face : line.who
          ? <PersonAvatar person={{ id: `gh:${line.who}`, name: line.who }} size={32} />
          : line.byDude ? <DudeMark size={32} /> : <AgentAvatar role="integration" size="lg" />;
        const text = line.actorId ? <>{person} {line.text}</> : line.byDude ? <><b>{dude}</b> {line.text}</> : line.text;
        out.push({ ...base, who, text, quote: line.quote, ...(e.eventType === "pull_request.opened" ? { open: { label: "Show", tab: "overview" } } : {}) });
        break;
      }
    }
    if (out.length > lines) out[lines]!.attempt = attemptOf(e);
  }
  return out.reverse();
}

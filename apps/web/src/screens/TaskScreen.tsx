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
import { Button, Callout, EmptyState, LinkButton, Spinner, Tab, TabList, TabPanel, Tabs } from "@dude/design-system/primitives";
import { DEFAULT_RUN_ROLE, EventTypes, TERMINAL_RUN_STATUSES, runLabel, type PersistedEvent } from "@dude/domain";
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
import { RunScreen } from "./RunScreen.tsx";
import { OwnerSelect } from "./OwnerSelect.tsx";
import { ServersAside } from "./ServersAside.tsx";
import { ServersSection, serversTab } from "./ServersSection.tsx";
import { existingTask, TaskDialog } from "./TaskDialog.tsx";
import { PullRequestActions } from "./PullRequestActions.tsx";
import { pullRequestActivity } from "../pullRequests.ts";
import type { TaskTab } from "../place.ts";

export interface TaskScreenProps {
  client: ApiClient;
  taskId: string;
  /** A session to show open on the Sessions tab: the page opens there. */
  runId?: string | undefined;
  onOpenRun: (runId: string) => void;
  /** Left the Sessions tab with a session open, or the tab the URL named: the URL should say the task again. */
  onCloseRun?: (() => void) | undefined;
  /** The tab the URL names, to open on. */
  tab?: TaskTab | undefined;
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

export function TaskScreen({ client, taskId, runId, onOpenRun, onCloseRun, tab: openTab, breadcrumb, onBack }: TaskScreenProps) {
  // A session's URL is the Sessions tab with it open; the task's URL is
  // whichever tab was picked here, Overview first. Coming back to the
  // task's URL from a session's (the tree, Back) is the overview again.
  // A URL that names a tab (`#/task/<id>/servers`) opens the page there.
  const [chosenTab, setTab] = useState<string>(openTab ?? "overview");
  const tab = runId ? "sessions" : chosenTab;
  const [lastRunId, setLastRunId] = useState(runId);
  if (runId !== lastRunId) {
    setLastRunId(runId);
    if (!runId) setTab(openTab ?? "overview");
  }
  const [lastOpenTab, setLastOpenTab] = useState(openTab);
  if (openTab !== lastOpenTab) {
    setLastOpenTab(openTab);
    if (openTab) setTab(openTab);
  }
  const pickTab = (next: string) => {
    setTab(next);
    // Leaving a session's tab, or the tab the URL named: the URL says the task again.
    if ((runId && next !== "sessions") || (openTab && next !== openTab)) {
      setLastRunId(undefined);
      onCloseRun?.();
    }
  };
  // For the open session, which is memoised: one function for the page's life.
  const pickLatest = useRef(pickTab);
  pickLatest.current = pickTab;
  const openServers = useCallback(() => pickLatest.current("servers"), []);
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

  // Phases of the current attempt, in the order they ran.
  const phases = useMemo(() => {
    const runs = [...(item?.runs ?? [])].filter((r) => r.phase);
    const attempt = Math.max(0, ...runs.map((r) => r.attempt));
    return runs.filter((r) => r.attempt === attempt).sort((a, b) => a.createdAt.localeCompare(b.createdAt));
  }, [item]);

  // What a pull request heard, for why a fix ran: a few of the ledger's many.
  const prEvents = useMemo(() => events.filter((e) => e.eventType.startsWith("pull_request.")), [events]);

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

  const started = phases.length > 0;
  // One per repository the work changed, in the order they were opened.
  const prs = [...pullRequests].sort((a, b) => a.createdAt.localeCompare(b.createdAt));
  const owner = item.owner ? (people.byId.get(item.owner.id) ?? item.owner) : null;
  const working = phases.find((r) => r.status === "running");
  // The aside says what serves the task when something does, or could: a
  // project with no servers defined has nothing to say there.
  const showServers = Boolean(servers.data && (servers.data.run || servers.data.recipes.length > 0));
  // Newest first; the one open is the one asked for, else the one picked
  // on first sight (what was running, else the newest).
  const sessions = [...item.runs].sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  const openRun = (runId && sessions.some((r) => r.id === runId) ? runId : undefined)
    ?? (picked && sessions.some((r) => r.id === picked) ? picked : undefined)
    ?? sessions.find((r) => r.status === "running")?.id ?? sessions[0]?.id;
  if (tab === "sessions" && openRun && openRun !== picked) setPicked(openRun);

  return (
    // On Sessions the page holds still and the session scrolls inside it.
    <div className={tab === "sessions" ? "screen taskScreen fixed" : "screen taskScreen"} data-testid="task-screen">
      <header className="taskTop">
        <div className="taskCrumbs">{breadcrumb}</div>
        <span className="taskTopActions">
          {!started ? (
            <Button variant="primary" leadingIcon="zap" onClick={() => void deliver()} disabled={delivering} data-testid="deliver">
              {delivering ? "Starting…" : "Deliver"}
            </Button>
          ) : null}
          {/* Work that changed no code ends waiting to be read, with no PR to merge. */}
          {item.status === "review" && prs.length === 0 && phases.length > 0 && phases.every((r) => TERMINAL_RUN_STATUSES.includes(r.status)) ? (
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
            <StatusMark status={item.status} size="sm" />
            {prs.map((pr) => (
              <PrChip key={pr.id} pr={pr} data-testid="pr-link" />
            ))}
            {item.key ? <span className="ds-mono" title={item.id}>{item.key}</span> : null}
            {phases[0]?.branch ? <span className="ds-mono">{phases[0].branch}</span> : null}
            <span>created <Duration ms={Math.max(0, Date.now() - Date.parse(item.createdAt))} format="age" tone="muted" /> ago</span>
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

      {item.escalation || problem ? (
        <div className="taskNotices">
          {item.escalation ? (
            <EscalationPanel client={client} task={{ ...item, escalation: item.escalation }} you={people.you}
              onOpenRun={onOpenRun} onDecided={() => void load()} />
          ) : null}
          {problem ? <Callout tone="danger">{problem}</Callout> : null}
        </div>
      ) : null}

      {editing ? (
        <TaskDialog client={client} projectId={item.projectId} onClose={() => setEditing(false)} existing={existingTask(item, started)} onSaved={() => void load()} />
      ) : null}

      <Tabs value={tab} onValueChange={pickTab} fill>
        <TabList aria-label="Task" className="tabsInset">
          <Tab value="overview">Overview</Tab>
          <Tab value="findings" count={findings.length > 0 ? findings.length : undefined}>Findings</Tab>
          <Tab value="sessions" count={item.runs.length > 0 ? item.runs.length : undefined}>Sessions</Tab>
          <Tab value="files" count={artifacts.length > 0 ? new Set(artifacts.map((a) => a.name)).size : undefined}>Files</Tab>
          <Tab value="servers" {...serversTab(servers.data)}>Servers</Tab>
          <Tab value="activity">Activity</Tab>
        </TabList>

        <TabPanel value="overview" className="taskPane">
          <div className="taskOverview">
            <div className="taskMain">
              {item.goal || item.acceptanceCriteria.length > 0 ? (
                <section className="taskBlock" aria-label="Goal">
                  <h2 className="ds-label">Goal</h2>
                  <div className="taskGoal">
                    {item.goal ? <p>{item.goal}</p> : null}
                    {item.acceptanceCriteria.length > 0 ? (
                      <ul aria-label="Acceptance criteria">
                        {item.acceptanceCriteria.map((c) => (
                          <li key={c}>{c}</li>
                        ))}
                      </ul>
                    ) : null}
                  </div>
                </section>
              ) : null}

              <section className="taskBlock" aria-label="Pipeline">
                <h2 className="ds-label">Pipeline</h2>
                {started ? (
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

              <TaskMetricsSection client={client} taskId={taskId} live={item.status === "running"}
                done={["done", "failed", "aborted"].includes(item.status)} version={version} />
            </div>
            {prs.length > 0 || showServers ? (
              <aside className="taskAside" aria-label="Pull requests and servers">
                {prs.map((pr) => (
                  <PullRequestActions key={pr.id} client={client} pr={pr} defaultMethod={mergeMethod} onChanged={() => void load()}>
                    {(actions) => (
                      <PullRequestPanel pr={pr} data-testid="pr-panel" data-pr={pr.id}
                        face={(login) => <PersonAvatar person={{ name: login }} size={20} ring={false} />}
                        factActions={actions.facts} note={actions.note}
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
                  <ServersAside client={client} taskId={taskId} servers={servers} onAll={() => setTab("servers")} />
                ) : null}
              </aside>
            ) : null}
          </div>
        </TabPanel>

        <TabPanel value="findings" className="taskPane">
          {findings.length > 0 ? (
            <FindingGroup data-testid="findings" findings={findings}
              renderRow={(f) => (
                <FindingRow key={f.id} data-testid="finding" data-status={f.status} severity={f.severity} status={f.status}
                  category={f.category} title={f.title} file={f.file} line={f.line} description={f.description}
                  suggestedFix={f.suggestedFix} resolutionNote={f.resolutionNote} fixAttempts={f.fixAttempts}
                  fixedIn={f.resolvedByRunId ? resolvedIn(item, f.resolvedByRunId, onOpenRun) : undefined} />
              )} />
          ) : (
            <EmptyState compact icon="check" title="No findings" description={started ? "The reviewers raised nothing, yet." : "Reviewers report here once delivery starts."} />
          )}
        </TabPanel>

        <TabPanel value="sessions" fill>
          {sessions.length > 0 ? (
            <div className="taskSessions">
              <SessionList className="taskSessionList" data-testid="sessions">
                {sessions.map((run) => (
                  <SessionItem key={run.id} onOpen={() => onOpenRun(run.id)} current={run.id === openRun}
                    avatar={<AgentAvatar role={run.role ?? DEFAULT_RUN_ROLE} size="lg" live={run.status === "running"} />}
                    title={runLabel(run) + (run.attempt > 1 ? ` · attempt ${run.attempt}` : "")}
                    detail={<>{run.model ?? run.harness ?? "agent"} · {run.startedAt ? <Duration since={run.startedAt} until={run.endedAt} live={run.status === "running"} tone="muted" /> : "not started"}</>}
                    trailing={<StatusMark status={run.status} size="sm" iconOnly={run.status === "completed"} />} />
                ))}
              </SessionList>
              {openRun ? (
                <RunScreen key={openRun} client={client} runId={openRun} onBack={onBack} task={sessionTask} onOpenServers={openServers} />
              ) : null}
            </div>
          ) : (
            <div className="taskPane">
              <EmptyState compact icon="agent" title="No sessions yet" description="Each agent that works on the task has a session here, with its conversation." />
            </div>
          )}
        </TabPanel>

        <TabPanel value="files" className="taskPane">
          {artifacts.length > 0 ? (
            <FilesSection client={client} taskId={taskId} taskKey={item.key} artifacts={artifacts} onOpenRun={onOpenRun} />
          ) : (
            <EmptyState compact icon="file" title="No files yet" description="What the agents save — notes, screenshots, reports, recordings — shows here." />
          )}
        </TabPanel>

        <TabPanel value="servers" className="taskPane">
          <ServersSection client={client} servers={servers} taskId={taskId} />
        </TabPanel>

        <TabPanel value="activity" className="taskPane">
          <Activity events={events} people={people} runs={item.runs} />
        </TabPanel>
      </Tabs>
    </div>
  );
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

/** What a person or dude did to the task, newest first, by name. */
function Activity({ events, people, runs }: { events: readonly PersistedEvent[]; people: People; runs: readonly Run[] }) {
  const lines = useMemo(() => activityLines(events, people, runs), [events, people, runs]);
  if (lines.length === 0) return <EmptyState compact icon="list" title="Nothing yet" description="Who did what to the task shows here: deliveries, steers, answers, pull requests." />;
  const now = Date.now();
  return (
    <Timeline data-testid="activity">
      {lines.map((l) => (
        <TimelineItem key={l.id} who={l.who} quote={l.quote} when={<Duration ms={Math.max(0, now - Date.parse(l.at))} format="age" tone="muted" />}
          data-testid="activity-item">
          {l.text}
        </TimelineItem>
      ))}
    </Timeline>
  );
}

interface ActivityLine {
  id: string;
  at: string;
  who: ReactNode;
  text: ReactNode;
  quote?: ReactNode;
}

/** The ledger as sentences: the acts worth a line, each with who did it. */
export function activityLines(events: readonly PersistedEvent[], people: People, runs: readonly Run[]): ActivityLine[] {
  const out: ActivityLine[] = [];
  // A task with pull requests in several repositories names each by its repository.
  const named = new Set(events.filter((e) => e.eventType === "pull_request.opened").map((e) => e.payload.repo)).size > 1;
  const labels = new Map(runs.map((r) => [r.id, runLabel(r).toLowerCase()]));
  // One task's events: dude goes by the same name throughout.
  const taskId = events.find((e) => e.taskId)?.taskId;
  const dude = dudeName(taskId ?? "");
  const phase = (runId: string | null) => (runId && labels.get(runId)) || "agent";
  for (const e of events) {
    const by = humanActor(e);
    const name = actorName(by, people.names);
    const face = by ? <PersonAvatar person={{ ...(people.byId.get(by.id) ?? {}), id: by.id, name: name ?? "Someone" }} size={32} /> : null;
    const person = <b>{name ?? "Someone"}</b>;
    const p = e.payload;
    const base = { id: e.eventId, at: e.occurredAt };
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
        out.push({ ...base, who: face ?? <DudeMark size={32} />, text: <>{by ? person : <b>{dude}</b>} aborted the {phase(e.runId)}</>, quote: p.reason ? String(p.reason) : undefined });
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
        if (p.kind === "agent") out.push({ ...base, who: <AgentAvatar role="implementer" size="lg" />, text: <>The <b>{phase(e.runId)}</b> asked a question</>, quote: String(p.prompt ?? "") });
        break;
      case "run.created":
        if (typeof p.phase === "string") out.push({ ...base, who: <AgentAvatar role={(typeof p.role === "string" ? p.role : DEFAULT_RUN_ROLE) as Run["role"] & string} size="lg" />, text: <>The <b>{phase(e.runId)}</b> started</> });
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
        out.push({ ...base, who, text, quote: line.quote });
        break;
      }
    }
  }
  return out.reverse();
}

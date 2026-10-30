/**
 * A task's Overview: what a task opens on. In this order: what needs a
 * person (one attention region, beside the task's other notices); the
 * figures (what the task cost over every attempt, and the attempt's when
 * there are several; lead time; agents at work; what is unresolved), with
 * the full time and cost a disclosure away; then side by side, when the
 * pane is wide enough, the agents running now and the branch's changes,
 * and the pull request; recent milestones; and the details folded under
 * them (goal, relations, pipeline, servers and machines).
 *
 * Every figure and sentence is read from what dude recorded: a fact the
 * API does not give is left out, never estimated or made up.
 */

import { type ReactNode } from "react";
import { AgentAvatar, Disclosure, Duration, MetricGroup, MetricTile, RunningList, RunningRow, StatusMark, Timeline, TimelineItem, planProgress } from "@dude/design-system/components";
import { Button, Callout } from "@dude/design-system/primitives";
import { plural } from "@dude/design-system";
import { DEFAULT_RUN_ROLE, EventTypes, TERMINAL_RUN_STATUSES, isConductor, runLabel, type PersistedEvent } from "@dude/domain";
import type { ApiClient, PullRequest, Run, TaskMetrics } from "../api/client.ts";
import { planFrom } from "../api/conversation.ts";
import { runStatusLabel, runStatusShown } from "../runPresentation.tsx";
import { CostOf, TaskMetricsSection, leadTimeSub, useTaskMetrics } from "./MetricsSection.tsx";

/** A question an agent put to a person that nobody has answered or closed yet. */
export interface OpenQuestion {
  readonly questionId: string;
  readonly runId: string;
  /** The question in its own words: the one asked, or how many and their headers. */
  readonly text: string;
  /** Who it was put to, when it names someone. */
  readonly toName: string | null;
  readonly at: string;
}

/** The agents' questions still open, oldest first, read from the task's ledger (`question.asked` of kind agent). */
export function openAgentQuestions(events: readonly PersistedEvent[]): OpenQuestion[] {
  const open = new Map<string, OpenQuestion>();
  for (const e of events) {
    const p = e.payload ?? {};
    const id = typeof p.questionId === "string" ? p.questionId : "";
    if (e.eventType === EventTypes.QuestionAsked && p.kind === "agent" && id && e.runId) {
      const items = Array.isArray(p.items) ? p.items.filter((i): i is { header?: unknown; question?: unknown } => typeof i === "object" && i !== null) : [];
      const asked = items.map((i) => (typeof i.question === "string" ? i.question : "")).filter(Boolean);
      const headers = items.map((i) => (typeof i.header === "string" ? i.header : "")).filter(Boolean);
      const text = items.length > 1 ? `${items.length} questions${headers.length ? `: ${headers.join(", ")}` : ""}`
        : asked[0] ?? (typeof p.prompt === "string" ? p.prompt : "");
      open.set(id, { questionId: id, runId: e.runId, text, toName: typeof p.toName === "string" && p.toName ? p.toName : null, at: e.occurredAt });
    } else if ((e.eventType === EventTypes.QuestionAnswered || e.eventType === EventTypes.QuestionClosed) && id) {
      open.delete(id);
    }
  }
  return [...open.values()];
}

/**
 * What a live Run is doing, as its own record says: waiting on the answer to
 * its open question, else its plan's current step, else its latest tool
 * call, whichever it recorded last. Null when none is recorded.
 */
export function recordedActivity(runId: string, events: readonly PersistedEvent[]): string | null {
  // Questions answered or closed after they were asked: read newest first, so seen before their ask.
  const settled = new Set<string>();
  for (let i = events.length - 1; i >= 0; i--) {
    const e = events[i]!;
    if (e.runId !== runId) continue;
    const questionId = typeof e.payload?.questionId === "string" ? e.payload.questionId : "";
    if ((e.eventType === EventTypes.QuestionAnswered || e.eventType === EventTypes.QuestionClosed) && questionId) {
      settled.add(questionId);
      continue;
    }
    // Its own question still open: it is waiting on a person, not at its last tool.
    if (e.eventType === EventTypes.QuestionAsked && e.payload?.kind === "agent" && questionId && !settled.has(questionId)) {
      return typeof e.payload.toName === "string" && e.payload.toName ? `Asked ${e.payload.toName} a question · waiting for the answer` : "Asked a question · waiting for the answer";
    }
    if (e.eventType === EventTypes.PlanUpdated) {
      const plan = planFrom(e.payload as Record<string, unknown>);
      if (!plan || plan.length === 0) continue;
      const p = planProgress(plan);
      if (p.current?.content) return `${p.current.content} · step ${Math.min(p.done + 1, p.total)} of ${p.total}`;
      continue;
    }
    if (e.eventType === EventTypes.ToolCalled) {
      const title = typeof e.payload.title === "string" && e.payload.title ? e.payload.title : null;
      const tool = typeof e.payload.tool === "string" ? e.payload.tool : null;
      if (title || tool) return tool && title && title !== tool ? `${tool} · ${title}` : (title ?? tool)!;
    }
  }
  return null;
}

/** The Runs at work: every Run of the attempt not yet ended, previews aside (Servers has those), the conductor first. */
export function liveRuns(runs: readonly Run[]): Run[] {
  return runs.filter((r) => r.kind !== "preview" && !TERMINAL_RUN_STATUSES.includes(r.status))
    .sort((a, b) => Number(isConductor(b)) - Number(isConductor(a)) || a.createdAt.localeCompare(b.createdAt));
}

/**
 * The one attention region: an agent's open question, quoted, who can
 * answer it, and the way to where it is answered (Chat for the conductor,
 * the asking session otherwise).
 */
export function NeedsYou({ question, run, who, onAnswer }: {
  question: OpenQuestion; run: Run | undefined; who: string; onAnswer: () => void;
}) {
  const conductor = run ? isConductor(run) : false;
  const asker = run ? runLabel(run) : "An agent";
  return (
    <Callout tone="attention" fill data-testid="needs-you" data-question={question.questionId}>
      <div className="needsYou">
        <div className="needsYouText">
          <p className="needsYouWho">Needs {who} · {asker} asks</p>
          <p className="needsYouAsk">{question.text}</p>
        </div>
        <Button variant="primary" size="sm" trailingIcon="arrow-right" onClick={onAnswer} data-testid="needs-you-answer">
          {conductor ? "Answer in Chat" : "Answer in its session"}
        </Button>
      </div>
    </Callout>
  );
}

/**
 * The figures above the fold: the task's spend over every attempt (the
 * prominent one), the attempt's when there are several, lead time, agents at
 * work, and what is unresolved. A cost not reported is said so, never $0.00.
 */
export function OverviewSummary({ all, attempt, many, shown, current, earlier, live, done, active, openFindings, feedbackHolds }: {
  /** The whole task's figures; null until read. */
  all: TaskMetrics | null;
  /** The attempt shown's, when there are several; null until read. */
  attempt: TaskMetrics | null;
  many: boolean; shown: number; current: number; earlier: boolean;
  /** The task is running: lead time ticks. */
  live: boolean;
  /** Finished: its lead time is whole. */
  done: boolean;
  /** Agents at work in the attempt shown. */
  active: number;
  /** Open findings in the attempt shown. */
  openFindings: number;
  /** Feedback holding the merge, on the current attempt; undefined until read or on an earlier attempt. */
  feedbackHolds: number | undefined;
}) {
  const ran = (m: TaskMetrics | null) => m !== null && m.runs.length > 0;
  const own = many ? attempt : all;
  const notReported = <span className="muted">Not reported</span>;
  const unresolved = openFindings + (feedbackHolds ?? 0);
  const parts = [plural(openFindings, "open finding"), ...(feedbackHolds !== undefined ? [`${feedbackHolds} feedback holding`] : [])];
  return (
    <MetricGroup joined fill className="overviewFigures" data-testid="overview-summary">
      <MetricTile label="Task spend" data-testid="overview-spend"
        value={ran(all) ? <CostOf cost={all!.cost} tokens={all!.tokens.input + all!.tokens.output} activeMs={all!.activeMs} size="lg" /> : notReported}
        sub={ran(all) ? `${many ? "all attempts" : "this task"} · model + machine` : all ? "no runs yet" : undefined} />
      {many ? (
        <MetricTile label={`Attempt ${shown} spend`} data-testid="overview-attempt-spend"
          value={ran(attempt) ? <CostOf cost={attempt!.cost} tokens={attempt!.tokens.input + attempt!.tokens.output} activeMs={attempt!.activeMs} size="lg" /> : notReported}
          sub={shown === current ? "current attempt" : "set aside"} />
      ) : null}
      <MetricTile label="Lead time" data-testid="overview-lead"
        {...(ran(own) ? { value: own!.leadMs, unit: "ms" as const, live: live && !earlier, sub: leadTimeSub(earlier, done) } : { value: own ? "Not started" : "—" })} />
      <MetricTile label="Active agents" data-testid="overview-active" value={active} unit="count"
        sub={earlier ? `attempt ${shown} · set aside` : active === 0 ? "none at work" : many ? `attempt ${shown}` : "at work now"} />
      <MetricTile label="Unresolved" data-testid="overview-unresolved" value={unresolved} unit="count"
        sub={unresolved === 0 ? "nothing open" : parts.join(" · ")} />
    </MetricGroup>
  );
}

/** One live Run's row: who, on what, doing what its record says, its cost and time, the way to its session. */
export function RunningNowRow({ run, events, metrics, onOpen }: {
  run: Run; events: readonly PersistedEvent[]; metrics: TaskMetrics | null; onOpen: () => void;
}) {
  const figures = metrics?.runs.find((r) => r.id === run.id);
  const activity = recordedActivity(run.id, events);
  return (
    <RunningRow data-testid="running-run" data-run={run.id}
      avatar={<AgentAvatar role={run.role ?? DEFAULT_RUN_ROLE} size="lg" live={run.status === "running"} />}
      label={runLabel(run)}
      detail={<>{run.model ?? run.harness ?? "agent"} · <StatusMark status={runStatusShown(run)} label={runStatusLabel(run)} size="sm" /></>}
      activity={activity}
      cost={figures ? <CostOf cost={figures.cost} tokens={figures.tokens.input + figures.tokens.output} activeMs={figures.activeMs} /> : undefined}
      elapsed={run.startedAt ? <Duration since={run.startedAt} live={run.status === "running"} tone="muted" /> : undefined}
      action={<Button size="sm" variant="quiet" trailingIcon="arrow-right" onClick={onOpen} aria-label={`Open ${runLabel(run)}'s session`}>Open session</Button>} />
  );
}

/** A milestone: one of Activity's lines, as the Overview shows the newest few. */
export interface Milestone {
  readonly id: string;
  readonly at: string;
  readonly who: ReactNode;
  readonly text: ReactNode;
}

/** The branch a repository's work is on, as recorded: its name, its target, the last head a session recorded. */
export interface BranchFact {
  readonly repository: string;
  readonly branch: string;
  /** The pull request's base, when one is recorded. */
  readonly target: string | null;
  /** The newest head a session of the attempt recorded for it. */
  readonly head: string | null;
}

/**
 * The branch's changes, as far as dude records them today: which branch,
 * against what, at which head, and the way to Changes. No file counts: the
 * branch's own diff is not read here (a session's diff is not the
 * branch's), so none is shown.
 */
export function ChangesCard({ branches, onReview }: { branches: readonly BranchFact[]; onReview: () => void }) {
  return (
    <section className="overviewRegion" aria-labelledby="overview-changes-title" data-testid="overview-changes">
      <div className="overviewRegionHead">
        <h2 id="overview-changes-title">Changes</h2>
        <span className="muted">Committed · the branch</span>
      </div>
      {branches.length === 0 ? (
        <p className="muted" data-testid="overview-changes-none">No branch yet. It is recorded once an agent commits.</p>
      ) : (
        <ul className="overviewBranches">
          {branches.map((b) => (
            <li key={b.repository} data-testid="overview-branch" data-repository={b.repository}>
              {branches.length > 1 ? <span className="overviewBranchRepo">{b.repository}</span> : null}
              <code className="overviewBranchName">{b.branch}</code>
              {b.target ? <span className="muted">→ <code>{b.target}</code></span> : null}
              {b.head ? <span className="muted" title={`The last head a session recorded: ${b.head}`}>at <code>{b.head.slice(0, 7)}</code></span> : null}
            </li>
          ))}
        </ul>
      )}
      <div className="overviewRegionFoot">
        <Button size="sm" variant="secondary" trailingIcon="arrow-right" onClick={onReview} data-testid="overview-review-changes">Review changes</Button>
      </div>
    </section>
  );
}

/** Which Overview disclosures are open: kept by the page, so leaving the tab and coming back keeps them. */
export type OverviewOpen = ReadonlySet<string>;

export interface TaskOverviewProps {
  client: ApiClient;
  taskId: string;
  version: number;
  /** Every notice the task has now, the needs-you region first: one place above the figures. */
  notices: ReactNode;
  many: boolean;
  shown: number;
  current: number;
  earlier: boolean;
  live: boolean;
  done: boolean;
  /** The attempt shown's Runs. */
  runs: readonly Run[];
  events: readonly PersistedEvent[];
  onOpenRun: (runId: string) => void;
  openFindings: number;
  feedbackHolds: number | undefined;
  /** Not started, blocked, or planning: what shows in Running now's place, with its ways to start. */
  idle: ReactNode;
  /** Nothing at work: what it waits on, in one line from the recorded state. */
  waiting: string;
  branches: readonly BranchFact[];
  onChanges: () => void;
  /** The attempt's pull requests, each its panel with its actions; null for none. */
  pullRequests: ReactNode;
  prCount: number;
  milestones: readonly Milestone[];
  onActivity: () => void;
  goal: ReactNode;
  criteria: number;
  relations: ReactNode;
  relationCount: number | null;
  pipeline: ReactNode;
  pipelineMeta: string;
  servers: ReactNode;
  open: OverviewOpen;
  onOpenChange: (id: string, open: boolean) => void;
}

export function TaskOverview(p: TaskOverviewProps) {
  const all = useTaskMetrics(p.client, p.taskId, undefined, p.version);
  const own = useTaskMetrics(p.client, p.taskId, p.many ? p.shown : undefined, p.version);
  const live = liveRuns(p.runs);
  const ago = (at: string) => <Duration ms={Math.max(0, Date.now() - Date.parse(at))} format="age" tone="muted" />;
  const fold = (id: string) => ({ open: p.open.has(id), onOpenChange: (o: boolean) => p.onOpenChange(id, o) });
  return (
    <div className="taskOverviewPane" data-testid="task-overview">
      <div className="taskOverview">
        {p.notices}
        <div className="overviewSummary">
          <OverviewSummary all={all} attempt={own} many={p.many} shown={p.shown} current={p.current} earlier={p.earlier}
            live={p.live} done={p.done} active={live.length} openFindings={p.openFindings} feedbackHolds={p.feedbackHolds} />
        </div>
        <Disclosure title="Time & cost" meta={p.many ? `attempt ${p.shown} · every run` : "every run"} data-testid="overview-time-cost" {...fold("cost")}>
          {own && own.runs.length > 0 ? (
            <TaskMetricsSection client={p.client} taskId={p.taskId} attempt={p.many ? p.shown : undefined} setAside={p.earlier}
              live={p.live && !p.earlier} done={p.done} version={p.version} titled={false} metrics={own} />
          ) : <p className="muted">No runs recorded. Cost is not reported, not $0.00.</p>}
        </Disclosure>
        <div className="overviewGrid">
          <div className="overviewColumn">
            <section className="overviewRegion" aria-labelledby="overview-running-title" data-testid="overview-running">
              <div className="overviewRegionHead">
                <h2 id="overview-running-title">Running now</h2>
                <span className="muted">{live.length > 0 ? `${plural(live.length, "agent")}${p.many ? ` · attempt ${p.shown}` : ""}` : "No agent at work"}</span>
              </div>
              {p.idle ?? (live.length > 0 ? (
                <RunningList aria-label="Running now">
                  {live.map((r) => <RunningNowRow key={r.id} run={r} events={p.events} metrics={own} onOpen={() => p.onOpenRun(r.id)} />)}
                </RunningList>
              ) : <p className="muted" data-testid="overview-waiting">{p.waiting}</p>)}
            </section>
            <ChangesCard branches={p.branches} onReview={p.onChanges} />
          </div>
          <section className="overviewPullRequests" aria-labelledby="overview-pr-title" data-testid="overview-pull-requests">
            <h2 id="overview-pr-title">{p.prCount > 1 ? "Pull requests" : "Pull request"}</h2>
            {p.pullRequests ?? <p className="muted">No pull request yet. It shows here once one is opened.</p>}
          </section>
        </div>
        <section className="overviewMilestones" aria-labelledby="overview-milestones-title" data-testid="overview-milestones">
          <div className="overviewRegionHead">
            <h2 id="overview-milestones-title">Recent milestones</h2>
            <Button size="sm" variant="quiet" trailingIcon="arrow-right" onClick={p.onActivity} data-testid="overview-full-activity">Full activity</Button>
          </div>
          {p.milestones.length > 0 ? (
            <Timeline wide>
              {p.milestones.map((m) => <TimelineItem key={m.id} who={m.who} when={ago(m.at)} data-testid="overview-milestone">{m.text}</TimelineItem>)}
            </Timeline>
          ) : <p className="muted">Nothing recorded yet beyond the task itself.</p>}
        </section>
        <div className="overviewDetails">
          <Disclosure title="Goal & acceptance criteria" meta={p.criteria > 0 ? plural(p.criteria, "criterion", "criteria") : undefined}
            data-testid="overview-goal-criteria" {...fold("goal")}>{p.goal}</Disclosure>
          {p.relations ? (
            <Disclosure title="Relations" meta={p.relationCount !== null ? (p.relationCount === 0 ? "none" : String(p.relationCount)) : undefined}
              data-testid="overview-relations" {...fold("relations")}>{p.relations}</Disclosure>
          ) : null}
          <Disclosure title={p.many ? "Attempts & pipeline" : "Pipeline"} meta={p.pipelineMeta} data-testid="overview-pipeline" {...fold("pipeline")}>{p.pipeline}</Disclosure>
          <Disclosure title="Servers, previews & machines" meta="and the task's facts" data-testid="overview-servers" {...fold("servers")}>{p.servers}</Disclosure>
        </div>
      </div>
    </div>
  );
}

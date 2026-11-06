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

import { useState, type ReactNode } from "react";
import { AgentAvatar, DiffStat, Disclosure, Duration, MetricGroup, MetricTile, RunningList, RunningRow, StatusMark, Timeline, TimelineItem, planProgress } from "@dude/design-system/components";
import { Button, Callout } from "@dude/design-system/primitives";
import { plural } from "@dude/design-system";
import { DEFAULT_RUN_ROLE, EventTypes, TERMINAL_RUN_STATUSES, isConductor, runLabel, type PersistedEvent, type TaskBranchDiff } from "@dude/domain";
import { ApiError, type ApiClient, type PullRequest, type RecoveryOptions, type Run, type TaskMetrics, type TaskQuestion } from "../api/client.ts";
import { planFrom } from "../api/conversation.ts";
import { runStatusLabel, runStatusShown } from "../runPresentation.tsx";
import { short, useBranchDiff } from "../branchReview.ts";
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

/** A question's words: the one asked, or how many and their headers. */
function questionText(items: readonly unknown[], prompt: string): string {
  const rows = items.filter((i): i is { header?: unknown; question?: unknown } => typeof i === "object" && i !== null);
  const asked = rows.map((i) => (typeof i.question === "string" ? i.question : "")).filter(Boolean);
  const headers = rows.map((i) => (typeof i.header === "string" ? i.header : "")).filter(Boolean);
  return rows.length > 1 ? `${rows.length} questions${headers.length ? `: ${headers.join(", ")}` : ""}` : asked[0] ?? prompt;
}

/**
 * The questions that need a person now, oldest first, as the server holds
 * them (`TaskDetail.questions`, #404): open and put to a person, whatever
 * their Run (one moved to the conductor replacing a lost one is still open).
 * Cancelled, answered or expired ones never need you. Who it is put to is
 * named from `names`, else from the ledger's question.asked (`events`).
 */
export function openPersonQuestions(questions: readonly TaskQuestion[], names: ReadonlyMap<string, string> = new Map(),
  events: readonly PersistedEvent[] = []): OpenQuestion[] {
  const asked = new Map(openAgentQuestions(events, true).map((q) => [q.questionId, q]));
  return questions.filter((q) => q.status === "open" && q.addressee === "person").map((q) => ({
    questionId: q.id, runId: q.runId, text: questionText(q.items, q.prompt),
    toName: (q.toPerson ? names.get(q.toPerson) : undefined) ?? asked.get(q.id)?.toName ?? null, at: q.askedAt,
  }));
}

/**
 * The agents' questions still open, oldest first, read from the task's
 * ledger (`question.asked` of kind agent): what a page shows only while the
 * control plane gives no `questions` (an older one), as the ledger cannot
 * say a question was cancelled with its Run. `all` keeps settled ones too.
 */
export function openAgentQuestions(events: readonly PersistedEvent[], all = false): OpenQuestion[] {
  const open = new Map<string, OpenQuestion>();
  for (const e of events) {
    const p = e.payload ?? {};
    const id = typeof p.questionId === "string" ? p.questionId : "";
    if (e.eventType === EventTypes.QuestionAsked && p.kind === "agent" && id && e.runId) {
      const text = questionText(Array.isArray(p.items) ? p.items : [], typeof p.prompt === "string" ? p.prompt : "");
      open.set(id, { questionId: id, runId: e.runId, text, toName: typeof p.toName === "string" && p.toName ? p.toName : null, at: e.occurredAt });
    } else if (!all && (e.eventType === EventTypes.QuestionAnswered || e.eventType === EventTypes.QuestionClosed) && id) {
      open.delete(id);
    }
  }
  return [...open.values()];
}

/**
 * The person's question lost with the task's latest conductor (#404),
 * when Try again is offered on it (`recover.conductor.retry`): the one
 * `/recover` names. Only a legacy payload without that field falls back
 * to a question explicitly closed by loss/failure, never a deliberate settlement.
 */
export function lostQuestion(recovery: RecoveryOptions | null | undefined, questions: readonly TaskQuestion[]): LostAsk | null {
  const c = recovery?.conductor;
  if (!c || !c.retry || c.status !== "failed") return null;
  if ("lostQuestion" in c && c.lostQuestion === null) return null;
  const held = questions.filter((q) => q.status === "cancelled" && q.addressee === "person" && q.runId === c.runId);
  const q = c.lostQuestion ? held.find((h) => h.id === c.lostQuestion!.id)
    : held.findLast((h) => h.endedBy === "lost" || h.endedBy === "run_failed");
  if (!c.lostQuestion && !q) return null;
  return {
    questionId: c.lostQuestion?.id ?? q!.id,
    runId: c.runId,
    text: q ? questionText(q.items, q.prompt) : c.lostQuestion!.prompt,
    toPerson: q?.toPerson ?? null,
    endedBy: q?.endedBy ?? null,
    error: c.error ?? q?.askerError ?? null,
  };
}

/** A person's question that died with its conductor's Run, which Try again asks again. */
export interface LostAsk {
  readonly questionId: string;
  readonly runId: string;
  readonly text: string;
  readonly toPerson: string | null;
  /** Its question.closed's by: "lost" when lux lost the Run, else how it failed; null when no event said. */
  readonly endedBy: string | null;
  /** Why the Run failed, as recorded. */
  readonly error: string | null;
}

/** Why the answer was not heard, in the card's words: lux lost the Run, or it failed otherwise. */
export function lostWhy(lost: Pick<LostAsk, "endedBy" | "error">): string {
  if (lost.endedBy === "lost" || (lost.error ?? "").includes("lux no longer has")) {
    return "The conductor's Run was lost before you answered (lux no longer has it).";
  }
  return `The conductor's Run failed before you answered${lost.error ? `: ${lost.error}` : ""}.`;
}

/**
 * The lost asker (#404): the conductor that asked the person a question
 * failed before the answer was heard. The question, why, and Try again,
 * which starts a fresh conductor with the conversation so far that asks
 * again. Pending while it runs; a refusal (409) says the server's words.
 */
export function LostAsker({ lost, who, retry, onRetried, compact = false }: {
  lost: LostAsk; who: string;
  /** Try again (`POST /v1/tasks/:id/retry`); none for a reader who cannot. */
  retry: (() => Promise<unknown>) | undefined;
  onRetried?: (() => void) | undefined;
  compact?: boolean;
}) {
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const tryAgain = retry ? async () => {
    setBusy(true);
    setProblem(null);
    try {
      await retry();
      onRetried?.();
    } catch (err) {
      setProblem(err instanceof ApiError ? err.message : "Could not try again: the request failed.");
    } finally {
      setBusy(false);
    }
  } : undefined;
  return (
    <Callout tone="attention" fill={!compact} data-testid="lost-asker" data-question={lost.questionId} data-run={lost.runId}>
      <div className="needsYou">
        <div className="needsYouText">
          <p className="needsYouWho">{who}’s answer was not heard · Conductor asked</p>
          <p className="needsYouAsk">“{lost.text}”</p>
          <p data-testid="lost-asker-why">
            dude lost the conductor that asked this — your answer wasn’t heard. {lostWhy(lost)}{" "}
            Try again starts a fresh conductor with the conversation so far, and it asks you again.
          </p>
          {problem ? <p role="alert" className="ds-danger" data-testid="lost-asker-problem">{problem}</p> : null}
        </div>
        {tryAgain ? (
          <Button variant="primary" size="sm" onClick={() => void tryAgain()} disabled={busy} data-testid="lost-asker-retry"
            aria-busy={busy || undefined}>
            {busy ? "Trying again…" : "Try again"}
          </Button>
        ) : null}
      </div>
    </Callout>
  );
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

/** What a task, an attempt or a Run cost, from its recorded figures. */
function FiguresCost({ figures: f, large = false }: { figures: Pick<TaskMetrics, "cost" | "tokens" | "activeMs">; large?: boolean }) {
  return <CostOf cost={f.cost} tokens={f.tokens.input + f.tokens.output} activeMs={f.activeMs} {...(large ? { size: "lg" as const } : {})} />;
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
        value={ran(all) ? <FiguresCost figures={all!} large /> : notReported}
        sub={ran(all) ? `${many ? "all attempts" : "this task"} · model + machine` : all ? "no runs yet" : undefined} />
      {many ? (
        <MetricTile label={`Attempt ${shown} spend`} data-testid="overview-attempt-spend"
          value={ran(attempt) ? <FiguresCost figures={attempt!} large /> : notReported}
          sub={shown === current ? "current attempt" : "set aside"} />
      ) : null}
      <MetricTile label="Lead time" data-testid="overview-lead"
        {...(ran(own) ? { value: own!.leadMs, unit: "ms" as const, live: live && !earlier, sub: leadTimeSub(earlier, done) } : { value: own ? "Not started" : "—" })} />
      <MetricTile label="Active agents" data-testid="overview-active" value={active} unit="count"
        sub={earlier ? `attempt ${shown} · set aside` : active === 0 ? "none at work" : many ? `attempt ${shown}` : "at work now"} />
      {/* Never a partial total: on the current attempt the feedback holding the merge is part of it, and until read the total is not known. */}
      {!earlier && feedbackHolds === undefined ? (
        <MetricTile label="Unresolved" data-testid="overview-unresolved" value="—" sub="Reading feedback…" />
      ) : (
        <MetricTile label="Unresolved" data-testid="overview-unresolved" value={unresolved} unit="count"
          sub={unresolved === 0 ? "nothing open" : parts.join(" · ")} />
      )}
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
      cost={figures ? <FiguresCost figures={figures} /> : undefined}
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
 * The branch's changes: the latest snapshot of the attempt's first
 * repository (files, +/−, `main base…head`, committed · whole branch), or
 * what stands in its place — being prepared, unavailable and why, none —
 * and the way to Changes. Without a snapshot read, the branches recorded.
 */
export function ChangesCard({ branches, branch, onReview }: { branches: readonly BranchFact[]; branch?: TaskBranchDiff | null | undefined; onReview: () => void }) {
  const snap = branch?.availability.state === "ok" ? branch.snapshot : null;
  const state = branch?.availability;
  return (
    <section className="overviewRegion" aria-labelledby="overview-changes-title" data-testid="overview-changes">
      <div className="overviewRegionHead">
        <h2 id="overview-changes-title">Changes</h2>
        <span className="muted">Committed · whole branch</span>
      </div>
      {snap ? (
        <p className="overviewSnapshot" data-testid="overview-snapshot">
          {(branch?.repositories.length ?? 0) > 1 ? <span className="overviewBranchRepo">{snap.repository}</span> : null}
          <span>{plural(snap.stats.files, "file")}</span>
          <DiffStat additions={snap.stats.additions} deletions={snap.stats.deletions} />
          <code title={`merge base ${snap.mergeBaseSha} … branch head ${snap.headSha}`}>{snap.targetRef} {short(snap.mergeBaseSha)}…{short(snap.headSha)}</code>
        </p>
      ) : state?.state === "preparing" ? (
        <p className="muted" data-testid="overview-snapshot-preparing">Preparing the branch snapshot: it shows here once captured.</p>
      ) : state?.state === "unavailable" ? (
        <p className="muted" data-testid="overview-snapshot-unavailable">Snapshot unavailable: {state.reason}</p>
      ) : state?.state === "none" && branches.length === 0 ? (
        <p className="muted" data-testid="overview-changes-none">{state.reason}</p>
      ) : branches.length === 0 ? (
        <p className="muted" data-testid="overview-changes-none">No branch yet. It is recorded once an agent commits.</p>
      ) : null}
      {branches.length > 0 && !snap ? (
        <ul className="overviewBranches">
          {branches.map((b) => (
            <li key={b.repository} data-testid="overview-branch" data-repository={b.repository}>
              {branches.length > 1 ? <span className="overviewBranchRepo">{b.repository}</span> : null}
              <code className="overviewBranchName">{b.branch}</code>
              {b.target ? <span className="muted">→ <code>{b.target}</code></span> : null}
              {b.head ? <span className="muted" title={`The last head a session recorded: ${b.head}`}>at <code>{short(b.head)}</code></span> : null}
            </li>
          ))}
        </ul>
      ) : null}
      {snap ? <p className="muted overviewBranchName" data-testid="overview-branch-ref"><code>{snap.branchRef}</code></p> : null}
      <div className="overviewRegionFoot">
        <Button size="sm" variant="secondary" trailingIcon="arrow-right" onClick={onReview} data-testid="overview-review-changes">Review changes</Button>
      </div>
    </section>
  );
}

/**
 * One muted line when the published pull request is not at the branch head:
 * its checks are of another commit than the branch's latest snapshot.
 */
export function PrHeadLine({ branch }: { branch: TaskBranchDiff | null | undefined }) {
  const snap = branch?.availability.state === "ok" ? branch.snapshot : null;
  const pr = branch?.pullRequest ?? null;
  if (!snap || !pr || pr.headSha === snap.headSha) return null;
  return (
    <p className="muted" data-testid="overview-pr-head">
      Pull request #{pr.number} is at <code>{short(pr.headSha)}</code>; the branch is at <code>{short(snap.headSha)}</code>. Its checks apply to <code>{short(pr.headSha)}</code>.
    </p>
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
  // One attempt: its figures are the task's, read once.
  const attemptRead = useTaskMetrics(p.client, p.taskId, p.shown, p.version, !p.many);
  const own = p.many ? attemptRead : all;
  const live = liveRuns(p.runs);
  // The latest snapshot, never pinned: the Overview always says where the branch is now.
  const branch = useBranchDiff(p.client, p.taskId, p.shown, null, p.version, { pin: false });
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
            <ChangesCard branches={p.branches} branch={branch.diff} onReview={p.onChanges} />
          </div>
          <section className="overviewPullRequests" aria-labelledby="overview-pr-title" data-testid="overview-pull-requests">
            <h2 id="overview-pr-title">{p.prCount > 1 ? "Pull requests" : "Pull request"}</h2>
            {p.pullRequests ?? <p className="muted">No pull request yet. It shows here once one is opened.</p>}
            <PrHeadLine branch={branch.diff} />
          </section>
        </div>
        <section className="overviewMilestones" aria-labelledby="overview-milestones-title" data-testid="overview-milestones">
          <div className="overviewRegionHead">
            <h2 id="overview-milestones-title">Recent milestones</h2>
            <Button size="sm" variant="quiet" trailingIcon="arrow-right" onClick={p.onActivity} data-testid="overview-full-activity">Full activity</Button>
          </div>
          {p.milestones.length > 0 ? (
            <Timeline wide>
              {p.milestones.map((m, i) => <TimelineItem key={`${m.id}:${i}`} who={m.who} when={ago(m.at)} data-testid="overview-milestone">{m.text}</TimelineItem>)}
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

/**
 * One task's delivery: what was asked, where it stands, and what each
 * agent did about it.
 *
 * Top to bottom, the questions an operator asks in the order they ask them:
 * what is this, where is it (the phase pipeline), what did the agents leave
 * for a person to read (artifacts), is anything wrong (the findings), and is
 * it shippable (the pull request). Every agent in the
 * pipeline opens its own conversation, because the pipeline is a summary and
 * the chat is the truth.
 *
 * Driven by the task's event stream, so a phase starting, a finding
 * landing or the PR opening appears without a reload.
 */

import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { AgentAvatar, FindingGroup, FindingRow, StatusBadge, StepList, StepRow } from "@dude/design-system/components";
import { Button, Callout, EmptyState, Page, PageHeader, Section, Spinner } from "@dude/design-system/primitives";
import { DEFAULT_RUN_ROLE, runLabel } from "@dude/domain";
import type { ApiClient, Artifact, Finding, MergeMethod, PullRequest, Run, TaskDetail } from "../api/client.ts";
import { ApiError } from "../api/client.ts";
import { shortError } from "../escalation.ts";
import { useReloadOnEvents } from "../hooks/useEventStream.ts";
import { ArtifactsSection } from "./ArtifactsSection.tsx";
import { TaskMetricsSection } from "./MetricsSection.tsx";
import { existingTask, TaskDialog } from "./TaskDialog.tsx";
import { NotFound } from "./NotFound.tsx";
import { OwnerSelect } from "./OwnerSelect.tsx";
import { errorText } from "../hooks/useSave.tsx";
import { EscalationPanel } from "./EscalationPanel.tsx";
import { PrStateChip, PullRequestPanel } from "./PullRequestPanel.tsx";
import { PullRequestActivitySection } from "./PullRequestActivity.tsx";

export interface TaskScreenProps {
  client: ApiClient;
  taskId: string;
  onOpenRun: (runId: string) => void;
  /** Where it sits, shown above its title. */
  breadcrumb?: ReactNode;
  /** Leave for somewhere that exists, when this task does not. */
  onBack: () => void;
}

export function TaskScreen({ client, taskId, onOpenRun, breadcrumb, onBack }: TaskScreenProps) {
  const [item, setItem] = useState<TaskDetail | null>(null);
  const [missing, setMissing] = useState(false);
  const [findings, setFindings] = useState<Finding[]>([]);
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [pullRequests, setPullRequests] = useState<PullRequest[]>([]);
  const [problem, setProblem] = useState<string | null>(null);
  const [delivering, setDelivering] = useState(false);
  const [editing, setEditing] = useState(false);
  // Who is reading: the owner decides what a stopped delivery does next.
  const [you, setYou] = useState<string | null>(null);
  useEffect(() => {
    void client.listPeople().then((p) => setYou(p.you), (err: unknown) => setProblem(errorText(err)));
  }, [client]);
  // Bumped on each reload, for the sections that read their own data.
  const [version, setVersion] = useState(0);
  // What the Merge button does first: the organization's merge method.
  const [mergeMethod, setMergeMethod] = useState<MergeMethod>("squash");
  useEffect(() => {
    void client.githubSettings().then((s) => setMergeMethod(s.mergeMethod), () => undefined);
  }, [client]);

  const load = useCallback(async () => {
    setVersion((v) => v + 1);
    try {
      const [fresh, f, p, a] = await Promise.all([
        client.getTask(taskId),
        client.listFindings(taskId),
        client.listPullRequests(taskId),
        client.listArtifacts(taskId),
      ]);
      setItem(fresh);
      setFindings(f.findings);
      setArtifacts(a.artifacts);
      setPullRequests(p.pullRequests);
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) setMissing(true);
      else setProblem(err instanceof Error ? err.message : String(err));
    }
  }, [client, taskId]);

  useEffect(() => {
    void load();
  }, [load]);

  useReloadOnEvents({ client, taskId }, () => void load());

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
    return runs
      .filter((r) => r.attempt === attempt)
      .sort((a, b) => a.createdAt.localeCompare(b.createdAt));
  }, [item]);

  if (missing) return <NotFound what="task" onBack={onBack} />;
  if (!item) {
    return <div className="centered">{problem ?? <Spinner label="Loading…" />}</div>;
  }

  const started = phases.length > 0;
  // One per repository the work changed, in the order they were opened.
  const prs = [...pullRequests].sort((a, b) => a.createdAt.localeCompare(b.createdAt));

  const description = item.goal || item.acceptanceCriteria.length > 0 ? (
    <>
      {item.goal ? <p>{item.goal}</p> : null}
      {item.acceptanceCriteria.length > 0 ? (
        <ul aria-label="Acceptance criteria">
          {item.acceptanceCriteria.map((c) => (
            <li key={c}>{c}</li>
          ))}
        </ul>
      ) : null}
    </>
  ) : undefined;

  /**
   * Where a finding was fixed, as a way there: the fix the re-review that
   * resolved it judged — the last before that review — as "Fix 2" when
   * there were several, or the review itself when no fix is known.
   */
  const resolvedIn = (reviewId: string) => {
    const review = item.runs.find((r) => r.id === reviewId);
    const fixes = item.runs
      .filter((r) => r.phase === "fix" && r.attempt === review?.attempt)
      .sort((x, y) => x.createdAt.localeCompare(y.createdAt));
    const index = review ? fixes.findLastIndex((r) => r.createdAt < review.createdAt) : -1;
    const fix = fixes[index];
    const label = fix ? (fixes.length > 1 ? `${runLabel(fix)} ${index + 1}` : runLabel(fix)) : review ? runLabel(review) : "its review";
    return (
      <Button size="sm" variant="quiet" onClick={() => onOpenRun(fix?.id ?? reviewId)} data-testid="finding-fixed-in">
        {label}
      </Button>
    );
  };

  return (
    <Page data-testid="task-screen">
      <PageHeader
        data-testid="task-header"
        breadcrumb={breadcrumb}
        status={<StatusBadge status={item.status} />}
        itemKey={item.key ? <span title={item.id}>{item.key}</span> : undefined}
        title={item.title}
        description={description}
        actions={
          <>
            <OwnerSelect client={client} task={item} onChanged={() => void load()} onProblem={setProblem} />
            {!started ? (
              <Button
                variant="primary"
                leadingIcon="zap"
                onClick={() => void deliver()}
                disabled={delivering}
                data-testid="deliver"
              >
                {delivering ? "Starting…" : "Deliver"}
              </Button>
            ) : null}
            {/* Work that changed no code ends waiting to be read, with no PR to merge. */}
            {item.status === "review" && prs.length === 0 && phases.length > 0 &&
            phases.every((r) => ["completed", "failed", "aborted"].includes(r.status)) ? (
              <Button variant="primary" leadingIcon="check" data-testid="mark-done"
                onClick={() => void client.markDone(taskId).then(() => load(),
                  (err: unknown) => setProblem(err instanceof ApiError ? err.message : "Could not mark it done."))}>
                Mark done
              </Button>
            ) : null}
            <Button variant="secondary" leadingIcon="edit" onClick={() => setEditing(true)} data-testid="edit-task">
              Edit
            </Button>
            {prs.map((pr) => (
              <a key={pr.id} href={pr.url} target="_blank" rel="noreferrer" data-testid="pr-header-link" className="prHeaderLink"
                title={`${pr.repositoryName} #${pr.number} on GitHub`}>
                <PrStateChip pr={pr} />
                <span className="ds-sr-only"> (opens in a new tab)</span>
              </a>
            ))}
          </>
        }
      >
        {item.escalation ? (
          <EscalationPanel client={client} task={{ ...item, escalation: item.escalation }} you={you}
            onOpenRun={onOpenRun} onDecided={() => void load()} />
        ) : null}
        {problem ? <Callout tone="danger">{problem}</Callout> : null}
      </PageHeader>
      {editing ? (
        <TaskDialog
          client={client}
          projectId={item.projectId}
          onClose={() => setEditing(false)}
          existing={existingTask(item, started)}
          onSaved={() => void load()}
        />
      ) : null}

      <Section title="Pipeline">
        {started ? (
          <StepList data-testid="pipeline">
            {phases.map((run, index) => (
              <PhaseStep
                key={run.id}
                run={run}
                step={index + 1}
                findings={findings.filter((f) => f.runId === run.id)}
                onOpen={() => onOpenRun(run.id)}
              />
            ))}
            {prs.map((pr) => (
              <PullRequestStep key={pr.id} pr={pr} named={prs.length > 1} />
            ))}
          </StepList>
        ) : (
          <EmptyState
            compact
            icon="git-pr"
            title="Not started"
            description="Deliver runs an implementer, reviewers, a fixer if they find problems, a simplifier, and opens a pull request."
          />
        )}
      </Section>

      {prs.length > 0 ? (
        <Section title={prs.length > 1 ? "Pull requests" : "Pull request"}>
          <div className="taskPullRequests">
            {prs.map((pr) => (
              <PullRequestPanel key={pr.id} client={client} pr={pr} defaultMethod={mergeMethod} onChanged={() => void load()} />
            ))}
          </div>
        </Section>
      ) : null}

      <TaskMetricsSection client={client} taskId={taskId} live={item.status === "running"}
        done={["done", "failed", "aborted"].includes(item.status)} version={version} />

      <ArtifactsSection client={client} artifacts={artifacts} />

      {findings.length > 0 ? (
        <FindingGroup
          data-testid="findings"
          findings={findings}
          renderRow={(f) => (
            <FindingRow
              key={f.id}
              data-testid="finding"
              data-status={f.status}
              severity={f.severity}
              status={f.status}
              category={f.category}
              title={f.title}
              file={f.file}
              line={f.line}
              description={f.description}
              suggestedFix={f.suggestedFix}
              resolutionNote={f.resolutionNote}
              fixAttempts={f.fixAttempts}
              fixedIn={f.resolvedByRunId ? resolvedIn(f.resolvedByRunId) : undefined}
            />
          )}
        />
      ) : null}

      {prs.length > 0 ? <PullRequestActivitySection client={client} taskId={taskId} named={prs.length > 1} version={version} /> : null}
    </Page>
  );
}

function PhaseStep(props: { run: Run; step: number; findings: Finding[]; onOpen: () => void }) {
  const { run } = props;
  const blocking = props.findings.filter((f) => f.severity === "blocking" || f.severity === "high");
  const heads = Object.entries(run.heads);

  return (
    <StepRow
      data-testid="phase"
      data-phase={run.phase}
      data-status={run.status}
      onOpen={props.onOpen}
      step={props.step}
      avatar={<AgentAvatar role={run.role ?? DEFAULT_RUN_ROLE} size="sm" live={run.status === "running"} />}
      label={runLabel(run)}
      status={<StatusBadge status={run.status} size="sm" />}
      note={run.status === "failed" && run.error
        ? <span title={run.error}>{shortError(run.error, 80)}</span>
        : run.phase === "review" && run.status === "completed"
          ? props.findings.length === 0 ? "no findings" : `${blocking.length} blocking`
          : undefined}
      meta={heads.length === 0 ? undefined : heads.length === 1
        ? heads[0]![1].slice(0, 7)
        : heads.map(([repo, sha]) => `${repo}@${sha.slice(0, 7)}`).join(" ")}
      metaTitle={heads.length > 0 ? heads.map(([repo, sha]) => `${repo} ${sha}`).join("\n") : undefined}
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
      step="PR"
      label={named ? <><span className="ds-mono">{pr.repositoryName}</span> #{pr.number}</> : <>Pull request #{pr.number}</>}
      status={<PrStateChip pr={pr} withNumber={false} />}
      note={pr.title}
      meta={pr.headBranch}
    />
  );
}

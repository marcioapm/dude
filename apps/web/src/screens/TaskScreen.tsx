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
import type { ApiClient, Artifact, Finding, PullRequest, Run, TaskDetail } from "../api/client.ts";
import { ApiError } from "../api/client.ts";
import { useReloadOnEvents } from "../hooks/useEventStream.ts";
import { ArtifactsSection } from "./ArtifactsSection.tsx";
import { TaskMetricsSection } from "./MetricsSection.tsx";
import { existingTask, TaskDialog } from "./TaskDialog.tsx";
import { OwnerSelect } from "./OwnerSelect.tsx";

export interface TaskScreenProps {
  client: ApiClient;
  taskId: string;
  onOpenRun: (runId: string) => void;
  /** Where it sits, shown above its title. */
  breadcrumb?: ReactNode;
}

export function TaskScreen({ client, taskId, onOpenRun, breadcrumb }: TaskScreenProps) {
  const [item, setItem] = useState<TaskDetail | null>(null);
  const [findings, setFindings] = useState<Finding[]>([]);
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [pullRequests, setPullRequests] = useState<PullRequest[]>([]);
  const [problem, setProblem] = useState<string | null>(null);
  const [delivering, setDelivering] = useState(false);
  const [editing, setEditing] = useState(false);

  const load = useCallback(async () => {
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
      setProblem(err instanceof Error ? err.message : String(err));
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
              {started ? "Move" : "Edit"}
            </Button>
            {prs.map((pr) => (
              <a key={pr.id} href={pr.url} target="_blank" rel="noreferrer" data-testid="pr-link">
                {prs.length > 1 ? `${pr.repositoryName} #${pr.number}` : `Pull request #${pr.number}`} ↗
              </a>
            ))}
          </>
        }
      >
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

      <TaskMetricsSection client={client} taskId={taskId} live={item.status === "running"} />

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
              fixedIn={
                f.resolvedByRunId ? (
                  <Button size="sm" variant="ghost" onClick={() => onOpenRun(f.resolvedByRunId!)}>
                    judged fixed ›
                  </Button>
                ) : undefined
              }
            />
          )}
        />
      ) : null}
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
      note={run.phase === "review" && run.status === "completed"
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
 * A PR's state in the status vocabulary. Open is "in review", not
 * "running": nothing of ours is executing, it is waiting on people.
 */
const PR_STATE_STATUS: Record<PullRequest["state"], "review" | "done" | "aborted" | "pending"> = {
  draft: "pending",
  open: "review",
  merged: "done",
  closed: "aborted",
};

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
      status={<StatusBadge status={PR_STATE_STATUS[pr.state]} size="sm" />}
      note={`${pr.state} · checks ${pr.checks} · review ${pr.review.replace("_", " ")}`}
      meta={pr.headBranch}
    />
  );
}

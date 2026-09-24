/**
 * One work item's delivery: what was asked, where it stands, and what each
 * agent did about it.
 *
 * Top to bottom, the questions an operator asks in the order they ask them:
 * what is this, where is it (the phase pipeline), what did the agents leave
 * for a person to read (artifacts), is anything wrong (the findings), and is
 * it shippable (the pull request). Every agent in the
 * pipeline opens its own conversation, because the pipeline is a summary and
 * the chat is the truth.
 *
 * Driven by the work item's event stream, so a phase starting, a finding
 * landing or the PR opening appears without a reload.
 */

import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { AgentAvatar, FindingGroup, FindingRow, StatusBadge } from "@dude/design-system/components";
import { Button, EmptyState, Spinner } from "@dude/design-system/primitives";
import { DEFAULT_RUN_ROLE, runLabel } from "@dude/domain";
import type { ApiClient, Artifact, Finding, PullRequest, Run, WorkItemDetail } from "../api/client.ts";
import { ApiError } from "../api/client.ts";
import { useReloadOnEvents } from "../hooks/useEventStream.ts";
import { ArtifactsSection } from "./ArtifactsSection.tsx";
import { existingWorkItem, WorkItemDialog } from "./WorkItemDialog.tsx";

export interface WorkItemScreenProps {
  client: ApiClient;
  workItemId: string;
  onOpenRun: (runId: string) => void;
  /** Where it sits, shown above its title. */
  breadcrumb?: ReactNode;
}

export function WorkItemScreen({ client, workItemId, onOpenRun, breadcrumb }: WorkItemScreenProps) {
  const [item, setItem] = useState<WorkItemDetail | null>(null);
  const [findings, setFindings] = useState<Finding[]>([]);
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [pullRequests, setPullRequests] = useState<PullRequest[]>([]);
  const [problem, setProblem] = useState<string | null>(null);
  const [delivering, setDelivering] = useState(false);
  const [editing, setEditing] = useState(false);

  const load = useCallback(async () => {
    try {
      const [fresh, f, p, a] = await Promise.all([
        client.getWorkItem(workItemId),
        client.listFindings(workItemId),
        client.listPullRequests(workItemId),
        client.listArtifacts(workItemId),
      ]);
      setItem(fresh);
      setFindings(f.findings);
      setArtifacts(a.artifacts);
      setPullRequests(p.pullRequests);
    } catch (err) {
      setProblem(err instanceof Error ? err.message : String(err));
    }
  }, [client, workItemId]);

  useEffect(() => {
    void load();
  }, [load]);

  useReloadOnEvents({ client, workItemId }, () => void load());

  const deliver = async () => {
    setDelivering(true);
    setProblem(null);
    try {
      await client.deliver(workItemId);
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

  return (
    <div className="workItemScreen" data-testid="work-item-screen">
      <header className="wiHeader">
        {breadcrumb}
        <div className="wiTitleRow">
          <StatusBadge status={item.status} />
          {item.key ? <code className="wiKey" title={item.id}>{item.key}</code> : null}
          <h1 className="wiTitle">{item.title}</h1>
        </div>
        {item.goal ? <p className="wiGoal">{item.goal}</p> : null}
        {item.acceptanceCriteria.length > 0 ? (
          <ul className="wiCriteria">
            {item.acceptanceCriteria.map((c) => (
              <li key={c}>{c}</li>
            ))}
          </ul>
        ) : null}
        <div className="wiActions">
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
              onClick={() => void client.markDone(workItemId).then(() => load(),
                (err: unknown) => setProblem(err instanceof ApiError ? err.message : "Could not mark it done."))}>
              Mark done
            </Button>
          ) : null}
          <Button variant="secondary" leadingIcon="edit" onClick={() => setEditing(true)} data-testid="edit-work-item">
            {started ? "Move" : "Edit"}
          </Button>
          {prs.map((pr) => (
            <a key={pr.id} className="wiPrLink" href={pr.url} target="_blank" rel="noreferrer" data-testid="pr-link">
              {prs.length > 1 ? `${pr.repositoryName} #${pr.number}` : `Pull request #${pr.number}`} ↗
            </a>
          ))}
        </div>
        {problem ? <p className="problem">{problem}</p> : null}
        {editing ? (
        <WorkItemDialog
          client={client}
          projectId={item.projectId}
          onClose={() => setEditing(false)}
          existing={existingWorkItem(item, started)}
          onSaved={() => void load()}
        />
        ) : null}
      </header>

      <section className="wiSection" aria-labelledby="pipeline-heading">
        <h2 id="pipeline-heading" className="ds-label wiSectionTitle">Pipeline</h2>
        {started ? (
          <ol className="pipeline" data-testid="pipeline">
            {phases.map((run, index) => (
              <PhaseCard
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
          </ol>
        ) : (
          <EmptyState
            compact
            icon="git-pr"
            title="Not started"
            description="Deliver runs an implementer, reviewers, a fixer if they find problems, a simplifier, and opens a pull request."
          />
        )}
      </section>

      <ArtifactsSection client={client} artifacts={artifacts} />

      {findings.length > 0 ? (
        <FindingGroup
          className="wiSection"
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
    </div>
  );
}

function PhaseCard(props: { run: Run; step: number; findings: Finding[]; onOpen: () => void }) {
  const { run } = props;
  const blocking = props.findings.filter((f) => f.severity === "blocking" || f.severity === "high");
  const label = runLabel(run);

  return (
    <li className="phase" data-phase={run.phase} data-status={run.status}>
      <button type="button" className="phaseButton" onClick={props.onOpen} data-testid="phase">
        <span className="phaseStep">{props.step}</span>
        <AgentAvatar role={run.role ?? DEFAULT_RUN_ROLE} size="sm" live={run.status === "running"} />
        <span className="phaseLabel">{label}</span>
        <StatusBadge status={run.status} size="sm" />
        {run.phase === "review" && run.status === "completed" ? (
          <span className="phaseNote">
            {props.findings.length === 0 ? "no findings" : `${blocking.length} blocking`}
          </span>
        ) : null}
        {Object.keys(run.heads).length > 0 ? (
          <code className="phaseSha" title={Object.entries(run.heads).map(([repo, sha]) => `${repo} ${sha}`).join("\n")}>
            {Object.keys(run.heads).length === 1
              ? Object.values(run.heads)[0]!.slice(0, 7)
              : Object.entries(run.heads).map(([repo, sha]) => `${repo}@${sha.slice(0, 7)}`).join(" ")}
          </code>
        ) : null}
        <span className="phaseOpen" aria-hidden>›</span>
      </button>
    </li>
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
    <li className="phase" data-phase="pr" data-status={pr.state}>
      <a className="phaseButton" href={pr.url} target="_blank" rel="noreferrer" data-testid="pr-step">
        <span className="phaseStep">PR</span>
        <span className="phaseLabel">
          {named ? <><span className="mono">{pr.repositoryName}</span> #{pr.number}</> : <>Pull request #{pr.number}</>}
        </span>
        <StatusBadge status={PR_STATE_STATUS[pr.state]} size="sm" />
        <span className="phaseNote">
          {pr.state} · checks {pr.checks} · review {pr.review.replace("_", " ")}
        </span>
        <code className="phaseSha">{pr.headBranch}</code>
        <span className="phaseOpen" aria-hidden>↗</span>
      </a>
    </li>
  );
}

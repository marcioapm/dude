/**
 * One work item's delivery: what was asked, where it stands, and what each
 * agent did about it.
 *
 * Top to bottom, the questions an operator asks in the order they ask them:
 * what is this, where is it (the phase pipeline), is anything wrong (the
 * findings), and is it shippable (the pull request). Every agent in the
 * pipeline opens its own conversation, because the pipeline is a summary and
 * the chat is the truth.
 *
 * Driven by the work item's event stream, so a phase starting, a finding
 * landing or the PR opening appears without a reload.
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import { AgentAvatar, StatusBadge } from "@dude/design-system/components";
import { Button, EmptyState, Spinner } from "@dude/design-system/primitives";
import { DEFAULT_RUN_ROLE, runLabel } from "@dude/domain";
import type { ApiClient, Finding, PullRequest, Run, WorkItemDetail } from "../api/client.ts";
import { ApiError } from "../api/client.ts";
import { useReloadOnEvents } from "../hooks/useEventStream.ts";
import { WorkItemDialog } from "./WorkItemDialog.tsx";

export interface WorkItemScreenProps {
  client: ApiClient;
  workItemId: string;
  onOpenRun: (runId: string) => void;
}

export function WorkItemScreen({ client, workItemId, onOpenRun }: WorkItemScreenProps) {
  const [item, setItem] = useState<WorkItemDetail | null>(null);
  const [findings, setFindings] = useState<Finding[]>([]);
  const [pullRequests, setPullRequests] = useState<PullRequest[]>([]);
  const [problem, setProblem] = useState<string | null>(null);
  const [delivering, setDelivering] = useState(false);
  const [editing, setEditing] = useState(false);

  const load = useCallback(async () => {
    try {
      const [fresh, f, p] = await Promise.all([
        client.getWorkItem(workItemId),
        client.listFindings(workItemId),
        client.listPullRequests(workItemId),
      ]);
      setItem(fresh);
      setFindings(f.findings);
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
  const pr = pullRequests[0];
  const openFindings = findings.filter((f) => f.status === "open");

  return (
    <div className="workItemScreen" data-testid="work-item-screen">
      <header className="wiHeader">
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
          <Button variant="secondary" leadingIcon="edit" onClick={() => setEditing(true)} data-testid="edit-work-item">
            {started ? "Move" : "Edit"}
          </Button>
          {pr ? (
            <a className="wiPrLink" href={pr.url} target="_blank" rel="noreferrer" data-testid="pr-link">
              Pull request #{pr.number} ↗
            </a>
          ) : null}
        </div>
        {problem ? <p className="problem">{problem}</p> : null}
        <WorkItemDialog
          client={client}
          projectId={item.projectId}
          open={editing}
          onOpenChange={setEditing}
          existing={{
            id: item.id,
            delivering: started,
            title: item.title,
            goal: item.goal,
            acceptanceCriteria: item.acceptanceCriteria,
            epicId: item.epicId,
            repositoryId: item.repositoryId,
          }}
          onSaved={() => void load()}
        />
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
            {pr ? <PullRequestStep pr={pr} /> : null}
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

      {findings.length > 0 ? (
        <section className="wiSection" aria-labelledby="findings-heading" data-testid="findings">
          <h2 id="findings-heading" className="ds-label wiSectionTitle">
            Review findings
            <span className="wiCount">
              {openFindings.length > 0 ? `${openFindings.length} open` : "all addressed"}
            </span>
          </h2>
          <ul className="findings">
            {findings.map((f) => (
              <li key={f.id} className="finding" data-status={f.status} data-severity={f.severity}>
                <span className="findingSeverity">{f.severity}</span>
                <div className="findingBody">
                  <div className="findingTitle">{f.title}</div>
                  <div className="findingMeta">
                    {f.category}
                    {f.file ? <> · <code>{f.file}{f.line ? `:${f.line}` : ""}</code></> : null}
                    {f.fixAttempts > 0 ? ` · ${f.fixAttempts} fix attempt${f.fixAttempts === 1 ? "" : "s"}` : ""}
                  </div>
                  {f.status !== "open" && f.resolutionNote ? (
                    <div className="findingNote">{f.resolutionNote}</div>
                  ) : null}
                </div>
                <span className="findingStatus" data-testid="finding-status">{f.status}</span>
              </li>
            ))}
          </ul>
        </section>
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
        {run.headSha ? <code className="phaseSha">{run.headSha.slice(0, 7)}</code> : null}
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

function PullRequestStep({ pr }: { pr: PullRequest }) {
  return (
    <li className="phase" data-phase="pr" data-status={pr.state}>
      <a className="phaseButton" href={pr.url} target="_blank" rel="noreferrer" data-testid="pr-step">
        <span className="phaseStep">PR</span>
        <span className="phaseLabel">Pull request #{pr.number}</span>
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

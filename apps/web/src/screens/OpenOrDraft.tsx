/**
 * An adopted branch reviewed and ready for its pull request, held for the
 * person under Deliver: dude never publishes someone's branch without their
 * Open or Draft. Said where an escalation would be, in its grammar; only the
 * task's owner answers (the API refuses anyone else), others read whom it
 * waits on. The answer is the hand-back's confirmation (POST /decider with
 * openPullRequest and draft), the path a gate Deliver holds already takes.
 */

import { useState } from "react";
import { Button, Callout } from "@dude/design-system/primitives";
import type { ApiClient, TaskDetail, TaskSource } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";

/** Whether the task's delivery is held at the pull request for an adopted branch's Open or Draft. */
export const heldForOpenOrDraft = (task: Pick<TaskDetail, "source" | "decider" | "awaitingDecision">): boolean =>
  task.source?.kind === "branch" && task.decider !== "conductor" && task.awaitingDecision?.point === "before_pull_request";

export function OpenOrDraft({ client, task, source, you, onDone }: {
  client: ApiClient;
  task: Pick<TaskDetail, "id" | "owner">;
  source: TaskSource;
  /** The reader's key; null until known, when nothing is offered. */
  you: string | null;
  onDone: () => void;
}) {
  const [busy, setBusy] = useState<"open" | "draft" | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const someoneElse = task.owner !== null && you !== null && task.owner.id !== you;
  const answer = async (draft: boolean) => {
    setBusy(draft ? "draft" : "open");
    setProblem(null);
    try {
      await client.setDecider(task.id, "policy", true, draft);
      onDone();
    } catch (err) {
      setProblem(`Could not open the pull request: ${errorText(err)}`);
    } finally {
      setBusy(null);
    }
  };
  return (
    <Callout tone="attention" data-testid="open-or-draft">
      <div className="escalation">
        <p>
          <strong>Open the pull request?</strong> Review is done on <span className="ds-mono">{source.headBranch}</span>.
          dude opens its pull request into <span className="ds-mono">{source.baseBranch}</span> once you say how.
        </p>
        {someoneElse ? (
          <p className="escalationWaiting">Waiting for {task.owner!.name} to decide.</p>
        ) : you !== null ? (
          <div className="escalationActions">
            <Button size="sm" variant="primary" disabled={busy !== null} loading={busy === "open"} onClick={() => void answer(false)}
              data-testid="open-or-draft-open">Open</Button>
            <Button size="sm" variant="secondary" disabled={busy !== null} loading={busy === "draft"} onClick={() => void answer(true)}
              data-testid="open-or-draft-draft">Draft</Button>
          </div>
        ) : null}
        {problem ? <p className="escalationProblem" role="alert">{problem}</p> : null}
      </div>
    </Callout>
  );
}

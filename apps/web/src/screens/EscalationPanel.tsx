/**
 * Delivery stopped for a person: why, and what they may do about it —
 * resume the agent that failed where it stopped (while lux keeps it), go
 * back and try the step again, accept the findings a review got stuck on,
 * take what was merged, wait on the rest, or stop. The orchestrator says
 * which fit the reason (escalation.actions); only the task's owner decides,
 * and anyone else sees whom it waits on.
 *
 * Shipping past a test's failed criteria (accept) or a check it could not
 * make (waive) is also an organisation admin's to take, and needs a reason:
 * those two ask for it before they are sent.
 */

import { useState } from "react";
import { Button, Callout, Textarea } from "@dude/design-system/primitives";
import { runLabel } from "@dude/domain";
import type { ApiClient, EscalationAction, Person, TaskDetail } from "../api/client.ts";
import { escalationWords, needsReason, testCriteria } from "../escalation.ts";
import { errorText } from "../hooks/useSave.tsx";

const ACTION_LABEL: Record<EscalationAction, string> = {
  resume: "Resume it",
  retry: "Try again",
  accept: "Accept the findings and go on",
  waive: "Waive and go on",
  done: "Take what was merged",
  wait: "Wait on the rest",
  stop: "Stop",
};

/** What an action reads as for this reason. */
const actionLabel = (action: EscalationAction, reason: string) => {
  if (action === "accept" && reason === "review_failed") return "Go on without it";
  if (action === "accept" && reason === "test_failed_criteria") return "Accept the failure and go on";
  if (action === "retry" && reason === "test_failed_criteria") return "Fix it once more";
  if (action === "retry" && reason === "test_blocked") return "Test again";
  return ACTION_LABEL[action];
};

export function EscalationPanel({ client, task, you, youAdmin = false, onOpenRun, onDecided }: {
  client: ApiClient;
  task: TaskDetail & { escalation: NonNullable<TaskDetail["escalation"]> };
  /**
   * The reader's key: the owner decides, anyone else is told whom it waits
   * on. Null until known: nothing is offered to someone who may not take it.
   */
  you: string | null;
  /** The reader administers the organisation: they may waive or accept a test's verdict. */
  youAdmin?: boolean;
  onOpenRun: (runId: string) => void;
  onDecided: () => void;
}) {
  const { reason, actions: all } = task.escalation;
  const words = escalationWords(task.escalation);
  const criteria = testCriteria(task.escalation);
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState<EscalationAction | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  // The action waiting on its reason, once picked.
  const [reasoning, setReasoning] = useState<EscalationAction | null>(null);
  const owner: Person | null = task.owner;
  const someoneElse = owner !== null && you !== null && owner.id !== you;
  const theirs = you !== null && !someoneElse;
  const actions = theirs ? all : youAdmin && you !== null ? all.filter((a) => needsReason(reason, a)) : [];

  const decide = async (action: EscalationAction) => {
    if (needsReason(reason, action) && !note.trim()) {
      setReasoning(action);
      setProblem(null);
      return;
    }
    setBusy(action);
    setProblem(null);
    try {
      await client.decide(task.id, action, note);
      setNote("");
      setReasoning(null);
      onDecided();
    } catch (err) {
      setProblem(`Could not ${actionLabel(action, reason).toLowerCase()}: ${errorText(err)}`);
    } finally {
      setBusy(null);
    }
  };

  const required = reasoning !== null;
  return (
    <Callout tone="attention" data-testid="escalation" data-reason={reason}>
      <div className="escalation">
        <p>
          <strong>{words.short}.</strong> {words.sentence}{" "}
          {words.runId ? (
            <Button size="sm" variant="quiet" trailingIcon="arrow-right" onClick={() => onOpenRun(words.runId!)}
              data-testid="escalation-run">
              Open {runLabel(task.runs.find((r) => r.id === words.runId) ?? {})}
            </Button>
          ) : null}
        </p>
        {criteria.length > 0 ? (
          <ul className="escalationCriteria" data-testid="escalation-criteria">
            {criteria.map((c) => (
              <li key={c.criterion} data-testid="escalation-criterion" data-status={c.status}>
                #{c.criterion} {c.text} <span className="escalationCriterionStatus">({c.status})</span>
              </li>
            ))}
          </ul>
        ) : null}
        {someoneElse && actions.length === 0 ? (
          <p className="escalationWaiting">Waiting for {owner.name} to decide.</p>
        ) : null}
        {actions.length > 0 ? (
          <>
            {someoneElse ? (
              <p className="escalationWaiting">Waiting for {owner!.name} to decide; as an admin you may also {actions.map((a) => actionLabel(a, reason).toLowerCase()).join(" or ")}.</p>
            ) : null}
            <Textarea
              label={required ? `Why may it ${actionLabel(reasoning, reason).toLowerCase().replace(/ and go on$/, "")}? (required)` : "A note for the agents (optional)"}
              rows={2} value={note} onChange={(e) => setNote(e.target.value)} required={required}
              error={required && !note.trim() ? "Say why: it is kept with the test's verdict and on the pull request." : undefined}
              hint="Kept with the task: every agent from here on is told it." data-testid="escalation-note" />
            <div className="escalationActions">
              {actions.map((action) => (
                <Button key={action} size="sm" variant={action === "stop" ? "quiet" : action === actions[0] ? "primary" : "secondary"}
                  disabled={busy !== null} loading={busy === action} onClick={() => void decide(action)}
                  data-testid={`escalation-${action}`}>
                  {actionLabel(action, reason)}
                </Button>
              ))}
            </div>
          </>
        ) : null}
        {problem ? <p className="escalationProblem" role="alert">{problem}</p> : null}
      </div>
    </Callout>
  );
}

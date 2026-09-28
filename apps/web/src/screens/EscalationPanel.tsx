/**
 * Delivery stopped for a person: why, and what they may do about it — go
 * back and try the step again, accept the findings a review got stuck on,
 * take what was merged, wait on the rest, or stop. The orchestrator says
 * which fit the reason (escalation.actions); only the task's owner decides,
 * and anyone else sees whom it waits on.
 */

import { useState } from "react";
import { Button, Callout, Textarea } from "@dude/design-system/primitives";
import { runLabel } from "@dude/domain";
import type { ApiClient, EscalationAction, Person, TaskDetail } from "../api/client.ts";
import { escalationWords } from "../escalation.ts";
import { errorText } from "../hooks/useSave.tsx";

const ACTION_LABEL: Record<EscalationAction, string> = {
  retry: "Try again",
  accept: "Accept the findings and go on",
  done: "Take what was merged",
  wait: "Wait on the rest",
  stop: "Stop",
};

export function EscalationPanel({ client, task, you, onOpenRun, onDecided }: {
  client: ApiClient;
  task: TaskDetail & { escalation: NonNullable<TaskDetail["escalation"]> };
  /**
   * The reader's key: the owner decides, anyone else is told whom it waits
   * on. Null until known: nothing is offered to someone who may not take it.
   */
  you: string | null;
  onOpenRun: (runId: string) => void;
  onDecided: () => void;
}) {
  const words = escalationWords(task.escalation);
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState<EscalationAction | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const owner: Person | null = task.owner;
  const someoneElse = owner !== null && you !== null && owner.id !== you;
  const theirs = you !== null && !someoneElse;

  const decide = async (action: EscalationAction) => {
    setBusy(action);
    setProblem(null);
    try {
      await client.decide(task.id, action, note);
      setNote("");
      onDecided();
    } catch (err) {
      setProblem(`Could not ${ACTION_LABEL[action].toLowerCase()}: ${errorText(err)}`);
    } finally {
      setBusy(null);
    }
  };

  return (
    <Callout tone="attention" data-testid="escalation" data-reason={task.escalation.reason}>
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
        {someoneElse ? (
          <p className="escalationWaiting">Waiting for {owner.name} to decide.</p>
        ) : theirs && task.escalation.actions.length > 0 ? (
          <>
            <Textarea label="A note for the agents (optional)" rows={2} value={note} onChange={(e) => setNote(e.target.value)}
              hint="Kept with the task: every agent from here on is told it." data-testid="escalation-note" />
            <div className="escalationActions">
              {task.escalation.actions.map((action) => (
                <Button key={action} size="sm" variant={action === "stop" ? "quiet" : action === task.escalation.actions[0] ? "primary" : "secondary"}
                  disabled={busy !== null} loading={busy === action} onClick={() => void decide(action)}
                  data-testid={`escalation-${action}`}>
                  {ACTION_LABEL[action]}
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

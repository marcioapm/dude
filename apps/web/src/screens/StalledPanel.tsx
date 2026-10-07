/**
 * A phase Run of a plain delivery has made no progress for its role's time
 * limit: dude's facts about it, and what its owner may do — restart it (a
 * fresh agent in its place, told an optional note), leave it (nothing is
 * asked again; its time limit stops it), or stop the task (the abort).
 * Only the task's owner decides; anyone else sees whom it waits on.
 */

import { useState } from "react";
import { Button, Callout, Textarea } from "@dude/design-system/primitives";
import { runLabel } from "@dude/domain";
import type { ApiClient, Person, Run } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";

type Action = "restart" | "leave" | "stop";

const ACTION_LABEL: Record<Action, string> = { restart: "Restart it", leave: "Leave it", stop: "Stop the task" };

/** The Runs a banner is for: stalled, reported to their owner, and not left. */
export function stalledForOwner(runs: readonly Run[]): Run[] {
  return runs.filter((r) => r.stalled?.owner && !r.stalled.left);
}

export function StalledPanel({ client, run, owner, you, onOpenRun, onDone }: {
  client: ApiClient;
  run: Run & { stalled: NonNullable<Run["stalled"]> };
  owner: Person | null;
  /** The reader's key; null until known. */
  you: string | null;
  onOpenRun: (runId: string) => void;
  onDone: () => void;
}) {
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState<Action | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const someoneElse = owner !== null && you !== null && owner.id !== you;
  const theirs = you !== null && !someoneElse;
  const label = runLabel(run);

  const act = async (action: Action) => {
    setBusy(action);
    setProblem(null);
    try {
      if (action === "restart") await client.restart(run.id, note);
      else if (action === "leave") await client.leaveStalled(run.id);
      else await client.abort(run.id, note || `stopped: the ${label.toLowerCase()} made no progress`);
      setNote("");
      onDone();
    } catch (err) {
      setProblem(`Could not ${ACTION_LABEL[action].toLowerCase()}: ${errorText(err)}`);
    } finally {
      setBusy(null);
    }
  };

  return (
    <Callout tone="attention" data-testid="stalled" data-run={run.id}>
      <div className="escalation">
        <p>
          <strong>{label} has made no progress.</strong> {run.stalled.text}{" "}
          <Button size="sm" variant="quiet" trailingIcon="arrow-right" onClick={() => onOpenRun(run.id)} data-testid="stalled-run">
            Open {label}
          </Button>
        </p>
        {someoneElse ? (
          <p className="escalationWaiting">Waiting for {owner.name} to decide.</p>
        ) : theirs ? (
          <>
            <Textarea label="A note for the new agent (optional)" rows={2} value={note} onChange={(e) => setNote(e.target.value)}
              hint="Restart starts over from the task's branch: what it had not committed is lost." data-testid="stalled-note" />
            <div className="escalationActions">
              {(["restart", "leave", "stop"] as const).map((action) => (
                <Button key={action} size="sm" variant={action === "restart" ? "primary" : action === "leave" ? "secondary" : "quiet"}
                  disabled={busy !== null} loading={busy === action} onClick={() => void act(action)} data-testid={`stalled-${action}`}>
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

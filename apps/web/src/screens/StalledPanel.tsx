/**
 * A phase Run of a plain delivery has made no progress for its role's time
 * limit: dude's facts about it, and what its owner may do — restart it (a
 * fresh agent in its place, told an optional note), leave it (nothing is
 * asked again; its time limit stops it), or stop the task (the abort).
 * Only the task's owner decides; anyone else sees whom it waits on.
 */

import { useState, type ReactElement } from "react";
import { Button, Callout, Textarea } from "@dude/design-system/primitives";
import { runLabel } from "@dude/domain";
import type { ApiClient, Person, Run } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";

type Action = "restart" | "leave" | "stop";

const ACTIONS = {
  restart: { label: "Restart it", variant: "primary" },
  leave: { label: "Leave it", variant: "secondary" },
  stop: { label: "Stop the task", variant: "quiet" },
} as const;

/** The Runs a banner is for: stalled, reported to their owner, and not left. */
export function stalledForOwner(runs: readonly Run[]): Run[] {
  return runs.filter((r) => r.stalled?.owner && !r.stalled.left);
}

type StalledPanelProps = {
  client: ApiClient;
  run: Run & { stalled: NonNullable<Run["stalled"]> };
  owner: Person | null;
  /** The reader's key; null until known. */
  you: string | null;
  onOpenRun: (runId: string) => void;
  onDone: () => void;
};

export function StalledPanel({ client, run, owner, you, onOpenRun, onDone }: StalledPanelProps): ReactElement {
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState<Action | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const someoneElse = owner !== null && you !== null && owner.id !== you;
  const theirs = you !== null && !someoneElse;
  const label = runLabel(run);

  async function act(action: Action): Promise<void> {
    setBusy(action);
    setProblem(null);
    try {
      if (action === "restart") await client.restart(run.id, note);
      else if (action === "leave") await client.leaveStalled(run.id);
      else await client.abort(run.id, note || `stopped: the ${label.toLowerCase()} made no progress`);
      setNote("");
      onDone();
    } catch (err) {
      setProblem(`Could not ${ACTIONS[action].label.toLowerCase()}: ${errorText(err)}`);
    } finally {
      setBusy(null);
    }
  }

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
        ) : null}
        {theirs ? (
          <>
            <Textarea label="A note for the new agent (optional)" rows={2} value={note} onChange={(e) => setNote(e.target.value)}
              hint="Restart starts over from the task's branch: what it had not committed is lost." data-testid="stalled-note" />
            <div className="escalationActions">
              {(["restart", "leave", "stop"] as const).map((action) => (
                <Button key={action} size="sm" variant={ACTIONS[action].variant}
                  disabled={busy !== null} loading={busy === action} onClick={() => void act(action)} data-testid={`stalled-${action}`}>
                  {ACTIONS[action].label}
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

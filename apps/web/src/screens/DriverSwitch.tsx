import { useCallback, useRef, useState } from "react";
import { Button, Dialog } from "@dude/design-system/primitives";
import type { Decider } from "@dude/domain";
import { ApiError, type ApiClient } from "../api/client.ts";

export interface DriverControl {
  busy: boolean;
  problem: string | null;
  confirmOpen: boolean;
  change: (decider: Decider, openPullRequest?: boolean) => Promise<void>;
  cancel: () => void;
}

/** Header and Chat use one request and one gate confirmation; neither authorizes a PR implicitly. */
export function useDriverSwitch(client: ApiClient, taskId: string, onChanged: () => void): DriverControl {
  const [busy, setBusy] = useState(false);
  const pending = useRef(false);
  const [problem, setProblem] = useState<string | null>(null);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const change = useCallback(async (decider: Decider, openPullRequest = false) => {
    if (pending.current) return;
    pending.current = true;
    setBusy(true);
    setProblem(null);
    try {
      await client.setDecider(taskId, decider, openPullRequest);
      setConfirmOpen(false);
      onChanged();
    } catch (err) {
      if (decider === "policy" && !openPullRequest && err instanceof ApiError && err.code === "pull_request_gate") setConfirmOpen(true);
      else setProblem(err instanceof ApiError ? `Could not change who decides: ${err.message}` : "Could not change who decides.");
    } finally {
      pending.current = false;
      setBusy(false);
    }
  }, [client, taskId, onChanged]);
  return { busy, problem, confirmOpen, change, cancel: () => setConfirmOpen(false) };
}

export function DriverConfirmation({ control }: { control: DriverControl }) {
  return (
    <Dialog open={control.confirmOpen} onOpenChange={(o) => !o && control.cancel()} size="sm" tone="attention"
      title="Let Deliver finish it?"
      description="Deliver will open the pull request now. You have not answered Open or Draft to the conductor's question."
      footer={<>
        <Button variant="quiet" disabled={control.busy} onClick={control.cancel} data-testid="hand-back-keep">Keep deciding in Chat</Button>
        <Button variant="primary" disabled={control.busy} data-testid="hand-back-open" onClick={() => void control.change("policy", true)}>
          Open it and let Deliver finish
        </Button>
      </>} />
  );
}

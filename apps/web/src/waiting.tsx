/**
 * A Run lux has not placed yet, and why, as lux says it: on a task's
 * Servers tab (a preview stuck on Scheduling) and on the Run page. lux's
 * reason for "no host in the pool can run containers" gets dude's own
 * sentence; any other is shown as lux wrote it.
 */

import { useEffect, useState } from "react";
import { Callout } from "@dude/design-system/primitives";
import { waitsForContainerHost } from "@dude/domain";
import type { ApiClient } from "./api/client.ts";

export function WaitingForHost({ reason, onRunPage }: { reason: string; onRunPage?: boolean | undefined }) {
  const meanwhile = onRunPage ? " Nothing is spent meanwhile." : "";
  return (
    <Callout tone="attention" data-testid="waiting-for-host">
      {waitsForContainerHost(reason) ? (
        <>
          <b>Waiting for a host that can run containers.</b> lux has no host that can run containers. An admin can add one to a pool.{meanwhile}
        </>
      ) : (
        <>
          <b>Waiting for a host.</b> lux says: {sentence(reason)}{meanwhile}
        </>
      )}
    </Callout>
  );
}

/** lux's reason as a sentence: one period at its end, whether lux wrote it or not. */
function sentence(reason: string): string {
  const r = reason.trim();
  return /[.!?]$/.test(r) ? r : `${r}.`;
}

/**
 * Why lux has not placed a Run yet, read with its servers (the one place
 * dude reads lux's Run for the page) whenever `askAgain` moves while it is
 * live: its stream's servers.changed (lux's state changed while it waits
 * for a host, or it got one) and the stream coming back. Null when lux
 * says it is not waiting: a running, resumed or moved Run alike.
 */
export function useWaitingReason(client: ApiClient, runId: string, live: boolean, askAgain: number): string | null {
  const [reason, setReason] = useState<{ runId: string; reason: string | null } | null>(null);
  useEffect(() => {
    if (!live) return;
    let cancelled = false;
    client.runServers(runId).then((s) => {
      if (!cancelled) setReason({ runId, reason: s.run?.waitingReason ?? null });
    }, () => undefined);
    return () => {
      cancelled = true;
    };
  }, [client, runId, live, askAgain]);
  return live && reason?.runId === runId ? reason.reason : null;
}

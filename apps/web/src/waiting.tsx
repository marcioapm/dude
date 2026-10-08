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
          <b>Waiting for a host.</b> lux says: {reason}.{meanwhile}
        </>
      )}
    </Callout>
  );
}

/**
 * Why lux has not placed a Run yet, read with its servers (the one place
 * dude reads lux's Run for the page) whenever `askAgain` moves while it is
 * scheduled: its stream's servers.changed and status changes. Null when
 * it is not waiting.
 */
export function useWaitingReason(client: ApiClient, runId: string, scheduled: boolean, askAgain: number): string | null {
  const [reason, setReason] = useState<{ runId: string; reason: string | null } | null>(null);
  useEffect(() => {
    if (!scheduled) return;
    let cancelled = false;
    client.runServers(runId).then((s) => {
      if (!cancelled) setReason({ runId, reason: s.run?.waitingReason ?? null });
    }, () => undefined);
    return () => {
      cancelled = true;
    };
  }, [client, runId, scheduled, askAgain]);
  return scheduled && reason?.runId === runId ? reason.reason : null;
}

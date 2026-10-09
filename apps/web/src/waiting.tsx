/**
 * A Run lux has not placed yet, and why, as lux says it: on a task's
 * Servers tab (a preview stuck on Scheduling) and on the Run page. lux's
 * reason for "no host in the pool can run containers" gets dude's own
 * sentence; any other is shown as lux wrote it.
 */

import { Callout } from "@dude/design-system/primitives";
import { waitsForContainerHost } from "@dude/domain";

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

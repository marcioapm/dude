/**
 * The cue while an agent starts, from the conversation's `start`: the row
 * where its first turn will be (StartProgress), the header's chip
 * (StartChip), and when its stage counts as slow. One place, so a
 * session, a Run and a task's Chat say the same thing.
 */

import { useEffect, useState, type ReactNode } from "react";
import { FRESH_START_STAGES, MOVE_STAGES, RESUME_STAGES, type AgentRole, type StartStage } from "@dude/domain";
import { AgentAvatar, StartChip, StartProgress, startSteps, type StartStep } from "@dude/design-system/components";
import type { StartState } from "./api/conversation.ts";
import { ApiError } from "./api/client.ts";
import { instant } from "./instant.ts";

/** Past this long in a stage, a calm line says why it can take a while. Not a failure, not amber. */
export const SLOW_AFTER_MS: Record<StartStage, number> = {
  park: 60_000,
  waiting: 60_000,
  image: 120_000,
  volumes: 60_000,
  repositories: 90_000,
  container: 60_000,
  running: 60_000,
};

const SLOW_WORDS: Record<StartStage, string> = {
  park: "Saving its work takes a while for a large workspace.",
  waiting: "No machine free yet: dude is waiting for one.",
  image: "A large image can take a few minutes to pull the first time on a machine.",
  volumes: "A large workspace takes a while to restore.",
  repositories: "A large repository takes a while to check out.",
  container: "The container is taking longer than usual to start.",
  running: "The agent is taking longer than usual to come up.",
};

/** The verb a start reads with: a move (a drain, a preemption) is "Moving to another machine". */
export function startVerbOf(start: StartState): string {
  return start.kind === "move" ? "Moving to another machine" : start.kind === "resume" ? "Resuming" : "Starting";
}

/** Try again found a newer agent already holding the failed one's messages (the API's 409 retry_taken). */
export const RETRY_TAKEN = "A new agent already took the later messages.";

/** What a refused Try again says: RETRY_TAKEN, after which it is not offered again, or why it failed. */
export function retryRefusal(err: unknown, why: (err: unknown) => string): { taken: boolean; text: string } {
  if (err instanceof ApiError && err.code === "retry_taken") return { taken: true, text: RETRY_TAKEN };
  return { taken: false, text: `Could not start it again: ${why(err)}` };
}

const STAGES_OF: Record<StartState["kind"], readonly StartStage[]> = { start: FRESH_START_STAGES, resume: RESUME_STAGES, move: MOVE_STAGES };

export function stepsOf(start: StartState): StartStep[] {
  return startSteps({ stage: start.stage, since: start.stageSince, done: start.done, failed: start.failed !== null, stages: STAGES_OF[start.kind] });
}

/** The slow line, once the current stage has run past its threshold at `now`; none while failed or with no stage. */
export function slowNote(start: StartState, now: number): string | null {
  if (start.failed || !start.stage || !start.stageSince) return null;
  if (now - instant(start.stageSince) < SLOW_AFTER_MS[start.stage]) return null;
  return `${SLOW_WORDS[start.stage]} ${start.kind === "move" ? "Its work is kept" : "Your message is kept"}; nothing is spent meanwhile.`;
}

/** Re-renders when the slow line is due, not every second. */
function useSlowNote(start: StartState): string | null {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (start.failed || !start.stage || !start.stageSince) return;
    const due = instant(start.stageSince) + SLOW_AFTER_MS[start.stage] - Date.now();
    if (due <= 0) return;
    const t = setTimeout(() => setNow(Date.now()), due + 50);
    return () => clearTimeout(t);
  }, [start.failed, start.stage, start.stageSince]);
  return slowNote(start, now);
}

/** A woken conductor's failed start: its reasons go back to dude, which wakes the next conductor with them. */
export const WAKES_NEXT = "dude will wake a new conductor.";

/** The row where the agent's first turn will be; failed, with Try again or why it is not offered. */
export function StartRow({ start, role, onRetry, taken, woken }: {
  start: StartState; role: AgentRole; onRetry?: (() => void) | undefined; taken?: boolean | undefined; woken?: boolean | undefined;
}) {
  const slow = useSlowNote(start);
  return (
    <>
      <StartProgress
        // A failure is a new element: its stages start folded.
        key={start.failed ? "failed" : "live"}
        kind={start.kind}
        verb={startVerbOf(start)}
        steps={stepsOf(start)}
        startedAt={start.startedAt}
        avatar={<AgentAvatar role={role} size="chat" />}
        slowNote={slow ?? undefined}
        slowReason={slow ? start.luxReason : undefined}
        failure={start.failed ? { reason: start.failed.reason, onRetry: taken || woken ? undefined : onRetry } : undefined}
        data-testid="start-progress"
      />
      {start.failed && taken ? <p className="muted" data-testid="start-retry-taken">{RETRY_TAKEN}</p>
        : start.failed && woken ? <p className="muted" data-testid="start-wakes-next">{WAKES_NEXT}</p> : null}
    </>
  );
}

/** The header's line: a session's meta, or bare as a Run's statusNote. */
export function StartHeaderChip({ start, bare }: { start: StartState; bare?: boolean }): ReactNode {
  const step = stepsOf(start).find((s) => s.state === "current" || s.state === "failed");
  return <StartChip kind={start.kind} verb={startVerbOf(start)} step={step} startedAt={start.startedAt} bare={bare} data-testid="start-chip" />;
}

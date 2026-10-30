/**
 * The task's Changes tab, until the whole-branch review is built: one line
 * saying it is coming, and the way to each session of the attempt shown
 * that changed files, whose own Changes show its diff today.
 */

import { AgentAvatar, Duration, StatusMark } from "@dude/design-system/components";
import { Button, EmptyState } from "@dude/design-system/primitives";
import { DEFAULT_RUN_ROLE, runLabel } from "@dude/domain";
import type { Run } from "../api/client.ts";
import { runStatusLabel, runStatusShown } from "../runPresentation.tsx";

/** The attempt's sessions that recorded a commit in some repository, newest first. */
export function sessionsWithChanges(runs: readonly Run[]): Run[] {
  return runs.filter((r) => Object.keys(r.heads ?? {}).length > 0)
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
}

export function ChangesTab({ runs, onOpenRun }: { runs: readonly Run[]; onOpenRun: (runId: string) => void }) {
  const changed = sessionsWithChanges(runs);
  return (
    <div className="taskPane changesTab" data-testid="changes-tab">
      <p className="muted" data-testid="changes-coming">
        The whole-branch review is coming here. Until then, each session's own Changes shows what it changed.
      </p>
      {changed.length > 0 ? (
        <ul className="changesSessions" aria-label="Sessions that changed files">
          {changed.map((r) => {
            const repos = Object.keys(r.heads);
            return (
              <li key={r.id} data-testid="changes-session" data-run={r.id}>
                <AgentAvatar role={r.role ?? DEFAULT_RUN_ROLE} size="sm" live={r.status === "running"} />
                <span className="changesSessionLabel">{runLabel(r)}</span>
                <StatusMark status={runStatusShown(r)} label={runStatusLabel(r)} size="sm" iconOnly={r.status === "completed"} />
                <span className="ds-mono muted" title={repos.map((repo) => `${repo} ${r.heads[repo]}`).join("\n")}>
                  {repos.length === 1 ? r.heads[repos[0]!]!.slice(0, 7) : `${repos.length} repos`}
                </span>
                {r.startedAt ? <Duration since={r.startedAt} until={r.endedAt} live={r.status === "running"} tone="muted" /> : null}
                <Button size="sm" variant="quiet" trailingIcon="arrow-right" onClick={() => onOpenRun(r.id)}>Open session</Button>
              </li>
            );
          })}
        </ul>
      ) : (
        <EmptyState compact icon="git-pr" title="No changes recorded yet" description="Sessions that commit to the branch are listed here." />
      )}
    </div>
  );
}

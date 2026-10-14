/**
 * Where a task starts from: new work (the default), an existing pull
 * request, or an existing branch, in one of the project's repositories the
 * task may change. A pull request is looked up as it is named, and shows
 * its title and head → base; a branch takes the base its pull request will
 * merge into (the repository's default branch when left empty). dude's
 * refusal — a merged pull request, a fork, a protected branch, another
 * task's branch, GitHub unreachable — is shown on the field.
 *
 * Fixed once the task's delivery has started, as what it asks for is.
 */

import { useEffect, useState } from "react";
import { Callout, Fieldset, Input, Segmented, Select } from "@dude/design-system";
import { ApiError, type ApiClient, type Repository, type TaskSource, type TaskSourceInput } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";

export type StartsFrom = "new" | "pr" | "branch";

/** What the person has typed, before it is checked. */
export interface SourceDraft {
  from: StartsFrom;
  repositoryId: string;
  pullRequest: string;
  branch: string;
  base: string;
}

export function sourceDraft(saved: TaskSource | null | undefined, repositories: Repository[]): SourceDraft {
  if (!saved) return { from: "new", repositoryId: repositories[0]?.id ?? "", pullRequest: "", branch: "", base: "" };
  return {
    from: saved.kind, repositoryId: saved.repositoryId, pullRequest: saved.number ? String(saved.number) : "",
    branch: saved.kind === "branch" ? saved.headBranch : "", base: saved.kind === "branch" ? saved.baseBranch : "",
  };
}

/** The source the draft names, for the API; null for new work, undefined while incomplete. */
export function sourceInput(d: SourceDraft): TaskSourceInput | null | undefined {
  if (d.from === "new") return null;
  if (!d.repositoryId) return undefined;
  if (d.from === "pr") return d.pullRequest.trim() ? { kind: "pr", repositoryId: d.repositoryId, pullRequest: d.pullRequest.trim() } : undefined;
  if (!d.branch.trim()) return undefined;
  return { kind: "branch", repositoryId: d.repositoryId, branch: d.branch.trim(), ...(d.base.trim() ? { base: d.base.trim() } : {}) };
}

/** A lookup's outcome: what dude found, or why it will not take it over. */
export type SourceCheck = { state: "idle" } | { state: "checking" } | { state: "ok"; source: TaskSource }
  | { state: "refused"; message: string; code: string };

const LOOKUP_DELAY_MS = 400;

/** Looks the draft's source up as it settles; a newer draft drops an older lookup's answer. */
export function useSourceCheck(client: ApiClient, projectId: string, draft: SourceDraft, taskId: string | undefined, off: boolean): SourceCheck {
  const [check, setCheck] = useState<SourceCheck>({ state: "idle" });
  const input = sourceInput(draft);
  const key = JSON.stringify(input ?? null);
  useEffect(() => {
    if (off || !input) {
      setCheck({ state: "idle" });
      return;
    }
    let current = true;
    setCheck({ state: "checking" });
    const timer = setTimeout(() => {
      void client.checkTaskSource(projectId, input, taskId).then(
        (source) => current && setCheck({ state: "ok", source }),
        (err: unknown) => current && setCheck({ state: "refused", message: errorText(err), code: err instanceof ApiError ? err.code : "error" }),
      );
    }, LOOKUP_DELAY_MS);
    return () => {
      current = false;
      clearTimeout(timer);
    };
    // The draft's source, by value (key): a re-render with the same one is no new lookup.
  }, [client, projectId, taskId, key, off]);
  return check;
}

export interface StartsFromFieldProps {
  /** The project's repositories the task may change: where existing work can be taken over from. */
  repositories: Repository[];
  draft: SourceDraft;
  onChange: (draft: SourceDraft) => void;
  check: SourceCheck;
  /** Delivery has started: shown, not changed. */
  locked: boolean;
}

export function StartsFromField({ repositories, draft, onChange, check, locked }: StartsFromFieldProps) {
  const set = (patch: Partial<SourceDraft>) => onChange({ ...draft, ...patch });
  const repo = repositories.find((r) => r.id === draft.repositoryId);
  const refused = check.state === "refused" ? check.message : undefined;
  const found = check.state === "ok" ? check.source : null;
  return (
    <Fieldset legend="Starts from" disabled={locked} data-testid="task-source"
      hint={locked ? "Delivery has started: where it started from is fixed."
        : draft.from === "new" ? "New work: an implementer starts from the default branch."
        : "dude reviews the existing work first, fixes what review finds on the same branch, then the pull request."}>
      <Segmented<StartsFrom> label="Starts from" size="sm" disabled={locked} value={draft.from} onChange={(from) => set({ from })}
        data-testid="task-source-kind"
        options={[
          { value: "new", label: "New work" },
          { value: "pr", label: "Existing PR", disabled: repositories.length === 0 },
          { value: "branch", label: "Existing branch", disabled: repositories.length === 0 },
        ]} />
      {draft.from === "new" ? null : (
        <>
          {repositories.length > 1 ? (
            <Select label="Repository" size="sm" disabled={locked} value={draft.repositoryId}
              onValueChange={(repositoryId) => set({ repositoryId })} data-testid="task-source-repository"
              options={repositories.map((r) => ({ value: r.id, label: r.name }))} />
          ) : null}
          {draft.from === "pr" ? (
            <Input label="Pull request" size="sm" mono disabled={locked} placeholder="#123 or its URL"
              value={draft.pullRequest} onChange={(e) => set({ pullRequest: e.target.value })}
              error={refused} data-testid="task-source-pr"
              hint={found?.number ? undefined : check.state === "checking" ? "Looking it up…" : "Open, in this repository, not from a fork."} />
          ) : (
            <>
              <Input label="Branch" size="sm" mono disabled={locked} placeholder="feature/…"
                value={draft.branch} onChange={(e) => set({ branch: e.target.value })}
                error={refused} data-testid="task-source-branch"
                hint={check.state === "checking" ? "Looking it up…" : "Not the default branch, a release branch, or a protected one."} />
              <Input label="Base" size="sm" mono disabled={locked} placeholder={repo?.defaultBranch ?? "main"}
                value={draft.base} onChange={(e) => set({ base: e.target.value })} data-testid="task-source-base"
                hint="What its pull request will merge into. Empty: the repository's default branch." />
            </>
          )}
          {found ? (
            <Callout tone="neutral" data-testid="task-source-found">
              {found.number ? <><b>#{found.number}</b> {found.title} · </> : null}
              <span className="ds-mono">{found.headBranch}</span> → <span className="ds-mono">{found.baseBranch}</span>
            </Callout>
          ) : null}
        </>
      )}
    </Fieldset>
  );
}

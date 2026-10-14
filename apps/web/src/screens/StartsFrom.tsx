/**
 * Where a task starts from: new work (the default), an existing pull
 * request, or an existing branch, in one of the project's repositories the
 * task may change and dude may push to (GitHub asked). A pull request is
 * looked up as it is named, and shows its title and head → base; a branch
 * and its base are picked from the repository's branches, found on GitHub
 * as the person types (the base defaults to the repository's default
 * branch). dude's refusal — a merged pull request, a fork, a protected
 * branch, another task's branch, GitHub unreachable — is shown on the field.
 *
 * Fixed once the task's delivery has started, as what it asks for is.
 */

import { useCallback, useEffect, useState } from "react";
import { Callout, Fieldset, Input, SearchPicker, Segmented, Select } from "@dude/design-system";
import { ApiError, type ApiClient, type Repository, type SourceRepository, type TaskSource, type TaskSourceInput } from "../api/client.ts";
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

/**
 * The project's repositories as GitHub answers for dude's connection: which
 * it may push to, and why not the others. Asked once, when existing work is
 * chosen; null while asking, an error string when GitHub could not be.
 */
export function useSourceRepositories(client: ApiClient, projectId: string, wanted: boolean): SourceRepository[] | string | null {
  const [found, setFound] = useState<SourceRepository[] | string | null>(null);
  useEffect(() => {
    if (!wanted) return;
    let current = true;
    void client.sourceRepositories(projectId).then(
      ({ repositories }) => current && setFound(repositories),
      (err: unknown) => current && setFound(errorText(err)),
    );
    return () => {
      current = false;
    };
  }, [client, projectId, wanted]);
  return found;
}

export interface StartsFromFieldProps {
  client: ApiClient;
  projectId: string;
  /** The project's repositories the task may change: where existing work can be taken over from. */
  repositories: Repository[];
  /** GitHub's answer on which dude may push to (useSourceRepositories). */
  access: SourceRepository[] | string | null;
  draft: SourceDraft;
  onChange: (draft: SourceDraft) => void;
  check: SourceCheck;
  /** Delivery has started: shown, not changed. */
  locked: boolean;
  /** The source as saved: what a locked field shows, with no lookup. */
  saved?: TaskSource | null | undefined;
}

export function StartsFromField({ client, projectId, repositories, access, draft, onChange, check, locked, saved }: StartsFromFieldProps) {
  const set = (patch: Partial<SourceDraft>) => onChange({ ...draft, ...patch });
  const repo = repositories.find((r) => r.id === draft.repositoryId);
  // The person set the draft's repository to Reads it: the task would not change what it starts from.
  const onlyRead = !locked && draft.from !== "new" && draft.repositoryId !== "" && !repo;
  const readName = (Array.isArray(access) ? access.find((a) => a.id === draft.repositoryId)?.name : undefined) ?? "the repository";
  const refused = check.state === "refused" ? check.message : undefined;
  const found = check.state === "ok" ? check.source : null;
  const writableIds = Array.isArray(access) ? new Set(access.filter((a) => a.writable).map((a) => a.id)) : null;
  const whyNot = (id: string) => (Array.isArray(access) ? access.find((a) => a.id === id)?.reason : undefined);
  const usable = writableIds ? repositories.filter((r) => writableIds.has(r.id)) : [];
  // The draft's repository is one dude may push to; else the first that is.
  useEffect(() => {
    if (locked || onlyRead || draft.from === "new" || !writableIds || writableIds.has(draft.repositoryId)) return;
    if (usable[0]) onChange({ ...draft, repositoryId: usable[0].id, branch: "", base: "" });
  });
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
      {draft.from === "new" ? null : locked ? (
        <LockedSource draft={draft} repo={repo} saved={saved} />
      ) : onlyRead ? (
        <Callout tone="attention" data-testid="task-source-read">
          This task only reads {readName}, which it starts from: set it to Changes it, or start from New work.
        </Callout>
      ) : typeof access === "string" ? (
        <Callout tone="attention" data-testid="task-source-access">GitHub could not say which repositories dude may push to: {access}</Callout>
      ) : access === null ? (
        <Input label="Repository" size="sm" disabled value="" placeholder="Asking GitHub…" data-testid="task-source-access-pending" />
      ) : usable.length === 0 ? (
        <Callout tone="attention" data-testid="task-source-access">
          dude may not push to any repository this task changes{repositories[0] && whyNot(repositories[0].id) ? `: ${whyNot(repositories[0].id)}` : ""}.
        </Callout>
      ) : (
        <>
          <Select label="Repository" size="sm" value={draft.repositoryId}
            onValueChange={(repositoryId) => set({ repositoryId, branch: "", base: "" })} data-testid="task-source-repository"
            options={repositories.map((r) => ({ value: r.id, label: r.name, disabled: !writableIds?.has(r.id),
              ...(writableIds?.has(r.id) ? {} : { description: whyNot(r.id) ?? "dude may not push to it" }) }))} />
          {draft.from === "pr" ? (
            <Input label="Pull request" size="sm" mono placeholder="#123 or its URL"
              value={draft.pullRequest} onChange={(e) => set({ pullRequest: e.target.value })}
              error={refused} data-testid="task-source-pr"
              hint={found?.number ? undefined : check.state === "checking" ? "Looking it up…" : "Open, in this repository, not from a fork."} />
          ) : (
            <>
              <BranchPicker client={client} projectId={projectId} repositoryId={draft.repositoryId} label="Branch"
                value={draft.branch} onPick={(branch) => set({ branch })} error={refused} testId="task-source-branch"
                hint={check.state === "checking" ? "Looking it up…" : "Not the default branch, a release branch, or a protected one."} />
              <BranchPicker client={client} projectId={projectId} repositoryId={draft.repositoryId} label="Base"
                value={draft.base} placeholderValue={repo?.defaultBranch ?? "main"} onPick={(base) => set({ base })} testId="task-source-base"
                hint="What its pull request will merge into. None picked: the repository's default branch." />
            </>
          )}
          {found ? <SourceFound source={found} testId="task-source-found" /> : null}
        </>
      )}
    </Fieldset>
  );
}

/** A started task's source: shown as it was saved, nothing to pick and nothing asked of GitHub. */
function LockedSource({ draft, repo, saved }: { draft: SourceDraft; repo: Repository | undefined; saved: TaskSource | null | undefined }) {
  return (
    <>
      {draft.from === "pr" ? (
        <Input label="Pull request" size="sm" mono disabled value={draft.pullRequest ? `#${draft.pullRequest}` : ""} data-testid="task-source-pr" />
      ) : (
        <>
          <Input label="Branch" size="sm" mono disabled value={draft.branch} data-testid="task-source-branch-locked" />
          <Input label="Base" size="sm" mono disabled value={draft.base || repo?.defaultBranch || ""} data-testid="task-source-base-locked" />
        </>
      )}
      {saved ? <SourceFound source={saved} testId="task-source-saved" /> : null}
    </>
  );
}

/** A source's pull request, if it is one, and head → base. */
function SourceFound({ source, testId }: { source: TaskSource; testId: string }) {
  return (
    <Callout tone="neutral" data-testid={testId}>
      {source.number ? <><b>#{source.number}</b> {source.title} · </> : null}
      <span className="ds-mono">{source.headBranch}</span> → <span className="ds-mono">{source.baseBranch}</span>
    </Callout>
  );
}

/**
 * One of a repository's branches, found on GitHub as the person types
 * (SearchPicker: only the latest answer lands). What is picked stays shown
 * above the field until another is picked, however the words change; a
 * lookup GitHub refuses says why under it.
 */
function BranchPicker(props: {
  client: ApiClient;
  projectId: string;
  repositoryId: string;
  label: string;
  value: string;
  placeholderValue?: string;
  onPick: (branch: string) => void;
  error?: string | undefined;
  hint: string;
  testId: string;
}) {
  const { client, projectId, repositoryId, label, value, placeholderValue, onPick, error, hint, testId } = props;
  const [problem, setProblem] = useState<string | null>(null);
  const [more, setMore] = useState(false);
  const find = useCallback(async (q: string) => {
    try {
      const out = await client.sourceBranches(projectId, repositoryId, q);
      setProblem(null);
      setMore(out.more);
      return out.branches;
    } catch (err) {
      setProblem(errorText(err));
      return [];
    }
  }, [client, projectId, repositoryId]);
  return (
    <div data-testid={testId}>
      <Input label={label} size="sm" mono readOnly value={value} placeholder={placeholderValue ?? "Pick one below"}
        error={error ?? problem ?? undefined} hint={more ? `${hint} More branches match: type more of the name.` : hint}
        data-testid={`${testId}-value`} />
      <SearchPicker<string> key={repositoryId} label={`Find a ${label.toLowerCase()}`} size="sm" find={find} findOnEmpty
        optionKey={(b) => b} renderOption={(b) => <span className="ds-mono">{b}</span>} onPick={onPick}
        placeholder="Type to find a branch" empty={(q) => `No branch matches “${q}”.`} />
    </div>
  );
}

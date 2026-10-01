/**
 * What a person can do to a pull request from dude, as GitHub's own page
 * would let them: merge it (by the organization's method, or another),
 * update its branch, re-run its failed checks, ask someone to review. Each
 * goes to GitHub through the orchestrator, which records who asked and
 * reads the pull request back.
 *
 * Handed to the design system's PullRequestPanel as its slots: an action at
 * the end of a fact's line, the Merge button at its foot, and why Merge is
 * off (or what GitHub said) under it.
 */

import { useState, type ReactNode } from "react";
import { firstName, plural } from "@dude/design-system";
import type { FactKind } from "@dude/design-system/components";
import { Button, RowMenu, Tooltip } from "@dude/design-system/primitives";
import { prCheckFailed } from "@dude/domain";
import type { ApiClient, MergeMethod, PullRequest, ReviewerCandidate } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";
import { mergeBlockedBy } from "../pullRequests.ts";
import { ReviewerPicker } from "./ReviewerPicker.tsx";

const METHOD_LABEL: Record<MergeMethod, string> = { squash: "Squash and merge", merge: "Create a merge commit", rebase: "Rebase and merge" };

export interface PullRequestActionSlots {
  facts: Partial<Record<FactKind, ReactNode>>;
  /** Under a fact's line: asking for a review opens under the reviewers. */
  under: Partial<Record<FactKind, ReactNode>>;
  merge: ReactNode;
  note: ReactNode;
}

/** "Ask", "Ask Ana", "Ask 2 people". */
function askLabel(picked: ReadonlyArray<ReviewerCandidate>): string {
  if (picked.length === 0) return "Ask";
  if (picked.length > 1) return `Ask ${plural(picked.length, picked.some((p) => p.kind === "team") ? "reviewer" : "person", picked.some((p) => p.kind === "team") ? "reviewers" : "people")}`;
  const one = picked[0]!;
  const name = one.name || one.login;
  return `Ask ${one.kind === "team" ? name : firstName(name)}`;
}

export function PullRequestActions({ client, pr, defaultMethod, onChanged, children }: {
  client: ApiClient;
  pr: PullRequest;
  /** The organization's merge method: what Merge does first. */
  defaultMethod: MergeMethod;
  onChanged: () => void;
  children: (slots: PullRequestActionSlots) => ReactNode;
}) {
  const [busy, setBusy] = useState<string | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [method, setMethod] = useState<MergeMethod | null>(null);
  const [asking, setAsking] = useState(false);
  const [picked, setPicked] = useState<ReviewerCandidate[]>([]);

  const act = async (what: string, action: () => Promise<unknown>): Promise<boolean> => {
    setBusy(what);
    setProblem(null);
    try {
      await action();
      onChanged();
      return true;
    } catch (err) {
      setProblem(errorText(err));
      return false;
    } finally {
      setBusy(null);
    }
  };

  const open = pr.state === "open" || pr.state === "draft";
  if (!open) return <>{children({ facts: {}, under: {}, merge: null, note: null })}</>;

  const blocked = mergeBlockedBy(pr);
  const how = method ?? defaultMethod;
  const facts: PullRequestActionSlots["facts"] = {};
  if (pr.checks.some(prCheckFailed)) {
    facts.checks = (
      <Button size="sm" variant="quiet" disabled={busy !== null} onClick={() => void act("rerun", () => client.rerunFailedChecks(pr.id))}
        data-testid="pr-rerun">
        {busy === "rerun" ? "Asking…" : "Re-run failed"}
      </Button>
    );
  }
  // GitHub cannot update a branch that conflicts: that one is a person's to resolve.
  if (pr.behindBy > 0 && pr.mergeable !== "conflicting") {
    facts.base = (
      <Button size="sm" disabled={busy !== null} onClick={() => void act("update", () => client.updatePullRequestBranch(pr.id))}
        data-testid="pr-update-branch">
        {busy === "update" ? "Updating…" : "Update branch"}
      </Button>
    );
  }
  if (pr.unresolvedThreads > 0) {
    facts.threads = <a href={`${pr.url}/files`} target="_blank" rel="noreferrer">show</a>;
  }
  // Asked already and yet to answer: shown in the picker, not picked again.
  const asked = new Set(pr.reviews.filter((r) => r.state.toUpperCase() === "REQUESTED" || r.rerequested).map((r) => r.login.toLowerCase()));
  const stopAsking = () => {
    setAsking(false);
    setPicked([]);
  };
  const ask = () => {
    if (picked.length === 0 || busy !== null) return;
    void act("review", () => client.requestReview(pr.id, picked.map((p) => p.login))).then((ok) => ok && stopAsking());
  };
  facts.reviews = asking ? null : (
    <Button size="sm" variant="quiet" onClick={() => setAsking(true)} data-testid="pr-request-review">Request review</Button>
  );

  // Asking opens under the reviewers' lines: a picker, and Ask for those picked.
  const asker = asking ? (
    <div data-testid="pr-review-ask">
      <ReviewerPicker client={client} pullRequestId={pr.id} asked={asked} picked={picked} onChange={setPicked} onSubmit={ask} onCancel={stopAsking} autoFocus />
      <span className="prAskFoot">
        <Button size="sm" variant="primary" disabled={busy !== null || picked.length === 0} onClick={ask} data-testid="pr-review-send">
          {busy === "review" ? "Asking…" : askLabel(picked)}
        </Button>
        <Button size="sm" variant="quiet" onClick={stopAsking}>Cancel</Button>
      </span>
    </div>
  ) : null;

  const merge = (
    <span className="prMerge">
      <Tooltip content={blocked ?? `Merges it on GitHub: ${METHOD_LABEL[how].toLowerCase()}`}>
        {/* A disabled button takes no pointer events: the span carries the tooltip. */}
        <span tabIndex={blocked ? 0 : -1}>
          <Button variant="primary" disabled={blocked !== null || busy !== null}
            onClick={() => void act("merge", () => client.mergePullRequest(pr.id, how))} data-testid="pr-merge">
            {busy === "merge" ? "Merging…" : METHOD_LABEL[how]}
          </Button>
        </span>
      </Tooltip>
      <RowMenu label="Merge method" size="md" items={(Object.keys(METHOD_LABEL) as MergeMethod[]).map((m) => ({
        id: m, label: METHOD_LABEL[m], ...(m === how ? { icon: "check" as const } : {}),
      }))} onSelect={(id) => setMethod(id as MergeMethod)} />
    </span>
  );

  const note = problem ? <span className="prProblem" role="alert" data-testid="pr-problem">{problem}</span>
    : blocked ? <span data-testid="pr-blocked">{blocked}</span> : null;
  return <>{children({ facts, under: { reviews: asker }, merge, note })}</>;
}

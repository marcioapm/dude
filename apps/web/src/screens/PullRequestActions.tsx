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
import type { FactKind } from "@dude/design-system/components";
import { Button, Input, RowMenu, Tooltip } from "@dude/design-system/primitives";
import { prCheckFailed } from "@dude/domain";
import type { ApiClient, MergeMethod, PullRequest } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";
import { mergeBlockedBy, parseLogins } from "../pullRequests.ts";

const METHOD_LABEL: Record<MergeMethod, string> = { squash: "Squash and merge", merge: "Create a merge commit", rebase: "Rebase and merge" };

export interface PullRequestActionSlots {
  facts: Partial<Record<FactKind, ReactNode>>;
  merge: ReactNode;
  note: ReactNode;
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
  const [logins, setLogins] = useState("");

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
  if (!open) return <>{children({ facts: {}, merge: null, note: null })}</>;

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
  facts.reviews = asking ? (
    <form className="prAskForm" onSubmit={(e) => {
      e.preventDefault();
      const who = parseLogins(logins);
      if (who.length) {
        void act("review", () => client.requestReview(pr.id, who)).then((ok) => {
          if (ok) {
            setAsking(false);
            setLogins("");
          }
        });
      }
    }}>
      <Input size="sm" autoFocus aria-label="GitHub logins to ask" placeholder="logins, comma-separated" value={logins}
        onChange={(e) => setLogins(e.target.value)} data-testid="pr-review-logins" />
      <Button size="sm" type="submit" disabled={busy !== null || !logins.trim()} data-testid="pr-review-send">Ask</Button>
    </form>
  ) : (
    <Button size="sm" variant="quiet" onClick={() => setAsking(true)} data-testid="pr-request-review">Request review</Button>
  );

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
  return <>{children({ facts, merge, note })}</>;
}

/**
 * A task's pull request as the task's page shows it: its one state, where
 * it goes, what is true of it — checks by name, reviews by person, how it
 * stands against its base, open threads — and what GitHub's own page would
 * let a person do about it, from here: merge, update the branch, re-run
 * failed checks, ask for a review.
 *
 * Thin on purpose: the design system's PrChip and PullRequestPanel (fe
 * track) take its place when the tracks meet; the words and the actions
 * stay.
 */

import { useState, type ReactNode } from "react";
import { HumanAvatar } from "@dude/design-system/components";
import { formatDuration, Icon, type IconName } from "@dude/design-system";
import { Button, Input, RowMenu, Tooltip } from "@dude/design-system/primitives";
import type { ApiClient, MergeMethod, PullRequest } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";
import { baseLine, checkOutcome, checksLine, DISPLAY, mergeBlockedBy, reviewWords } from "../pullRequests.ts";

const METHOD_LABEL: Record<MergeMethod, string> = { squash: "Squash and merge", merge: "Create a merge commit", rebase: "Rebase and merge" };

/** The state as a chip: a glyph, a word and its tone. */
export function PrStateChip({ pr, withNumber = true }: { pr: PullRequest; withNumber?: boolean }) {
  const spec = DISPLAY[pr.display];
  return (
    <span className="prChip" data-tone={spec.tone} data-testid="pr-state" data-state={pr.display}>
      <Icon name={spec.glyph} size={12} />
      {spec.label}
      {withNumber ? <span className="ds-mono">#{pr.number}</span> : null}
    </span>
  );
}

export function PullRequestPanel({ client, pr, defaultMethod, onChanged }: {
  client: ApiClient;
  pr: PullRequest;
  /** The organization's merge method: the button's first word. */
  defaultMethod: MergeMethod;
  onChanged: () => void;
}) {
  const [busy, setBusy] = useState<string | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [method, setMethod] = useState<MergeMethod>(defaultMethod);
  const [asking, setAsking] = useState(false);
  const [logins, setLogins] = useState("");

  const act = async (what: string, action: () => Promise<unknown>) => {
    setBusy(what);
    setProblem(null);
    try {
      await action();
      onChanged();
    } catch (err) {
      setProblem(errorText(err));
    } finally {
      setBusy(null);
    }
  };

  const open = pr.state === "open" || pr.state === "draft";
  const failing = pr.checks.some((c) => checkOutcome(c) === "failed");
  const base = baseLine(pr);
  const blocked = mergeBlockedBy(pr);
  const reviewers = pr.reviews.filter((r) => r.state !== "COMMENTED" || !pr.reviews.some((o) => o.login === r.login && o !== r));

  return (
    <section className="prPanel" aria-label={`Pull request #${pr.number}`} data-testid="pr-panel" data-pr={pr.id}>
      <header className="prPanelHead">
        <Icon name="git-pr" size={16} />
        <a href={pr.url} target="_blank" rel="noreferrer" className="prPanelTitle" data-testid="pr-link">
          #{pr.number} {pr.title}
        </a>
        <PrStateChip pr={pr} withNumber={false} />
      </header>
      <div className="prPanelWhere ds-mono">
        {pr.repositoryName} · {pr.headBranch} → {pr.baseBranch}
      </div>

      <ul className="prFacts">
        {pr.state === "merged" ? <Fact tone="merged" glyph="merge" text="Merged" /> : null}
        {pr.state === "closed" ? <Fact tone="neutral" glyph="stop" text="Closed without merging" /> : null}
        {open || pr.checks.length > 0 ? (
          <Fact tone={failing ? "danger" : pr.checkState === "pending" ? "attention" : pr.checkState === "passing" ? "success" : "neutral"}
            glyph={failing ? "cross" : pr.checkState === "pending" ? "circle-dotted" : "check"}
            text={<b>{checksLine(pr)}</b>} testId="pr-checks"
            action={open && failing ? (
              <Button size="sm" variant="quiet" disabled={busy !== null} onClick={() => void act("rerun", () => client.rerunFailedChecks(pr.id))}
                data-testid="pr-rerun">
                {busy === "rerun" ? "Asking…" : "Re-run failed"}
              </Button>
            ) : undefined}>
            {pr.checks.length > 0 ? (
              <ul className="prChecks" data-testid="pr-check-list">
                {pr.checks.map((c) => {
                  const outcome = checkOutcome(c);
                  return (
                    <li key={c.name} data-testid="pr-check" data-outcome={outcome}>
                      <span className="prGlyph" data-tone={outcome === "failed" ? "danger" : outcome === "running" ? "attention" : "success"}>
                        <Icon name={outcome === "failed" ? "cross" : outcome === "running" ? "circle-dotted" : "check"} size={12} />
                      </span>
                      <span className="prCheckName">{c.name}</span>
                      {c.durationMs ? <span className="muted ds-tnum">{formatDuration(c.durationMs)}</span> : null}
                      {c.url ? <a href={c.url} target="_blank" rel="noreferrer">log</a> : null}
                    </li>
                  );
                })}
              </ul>
            ) : null}
          </Fact>
        ) : null}

        {reviewers.map((r) => {
          const state = r.state.toUpperCase();
          const tone = state === "APPROVED" ? "success" : state === "CHANGES_REQUESTED" ? "danger" : "neutral";
          const glyph = state === "APPROVED" ? "check" : state === "CHANGES_REQUESTED" ? "edit" : "eye";
          return (
            <Fact key={r.login} tone={tone} glyph={glyph} testId="pr-review"
              text={<span className="prWho"><HumanAvatar person={{ id: r.login, name: r.login }} size="xs" /><b>{r.login}</b> {reviewWords(state)}</span>} />
          );
        })}
        {open && reviewers.length === 0 ? <Fact tone="neutral" glyph="eye" text="No review yet" /> : null}
        {open ? (
          <li className="prAsk">
            {asking ? (
              <form className="prAskForm" onSubmit={(e) => {
                e.preventDefault();
                const who = logins.split(/[\s,]+/).map((l) => l.replace(/^@/, "")).filter(Boolean);
                if (who.length) void act("review", () => client.requestReview(pr.id, who)).then(() => { setAsking(false); setLogins(""); });
              }}>
                <Input size="sm" autoFocus aria-label="GitHub logins to ask" placeholder="GitHub logins, comma-separated" value={logins}
                  onChange={(e) => setLogins(e.target.value)} data-testid="pr-review-logins" />
                <Button size="sm" type="submit" disabled={busy !== null || !logins.trim()} data-testid="pr-review-send">Ask</Button>
                <Button size="sm" variant="quiet" onClick={() => setAsking(false)}>Cancel</Button>
              </form>
            ) : (
              <Button size="sm" variant="quiet" leadingIcon="plus" onClick={() => setAsking(true)} data-testid="pr-request-review">
                Request review
              </Button>
            )}
          </li>
        ) : null}

        {open && pr.unresolvedThreads > 0 ? (
          <Fact tone="attention" glyph="message" text={`${pr.unresolvedThreads} unresolved comment${pr.unresolvedThreads === 1 ? "" : "s"}`}
            testId="pr-threads" action={<a href={`${pr.url}/files`} target="_blank" rel="noreferrer">show</a>} />
        ) : null}
        {open && base ? (
          <Fact tone={pr.mergeable === "conflicting" ? "danger" : pr.behindBy > 0 ? "neutral" : "success"}
            glyph={pr.mergeable === "conflicting" ? "warning" : "arrow-up"} text={base} testId="pr-base"
            // GitHub cannot update a conflicting branch: that one is a person's to resolve.
            action={pr.behindBy > 0 && pr.mergeable !== "conflicting" ? (
              <Button size="sm" disabled={busy !== null} onClick={() => void act("update", () => client.updatePullRequestBranch(pr.id))}
                data-testid="pr-update-branch">
                {busy === "update" ? "Updating…" : "Update branch"}
              </Button>
            ) : undefined} />
        ) : null}
      </ul>

      <div className="prActions">
        {open ? (
          <Tooltip content={blocked ?? `Merges on GitHub: ${METHOD_LABEL[method].toLowerCase()}`}>
            <span className="prMerge">
              <Button variant="primary" disabled={blocked !== null || busy !== null}
                onClick={() => void act("merge", () => client.mergePullRequest(pr.id, method))} data-testid="pr-merge">
                {busy === "merge" ? "Merging…" : METHOD_LABEL[method]}
              </Button>
              <RowMenu label="Merge method" size="md" items={(Object.keys(METHOD_LABEL) as MergeMethod[]).map((m) => ({
                id: m, label: METHOD_LABEL[m], ...(m === method ? { icon: "check" as const } : {}),
              }))} onSelect={(id) => setMethod(id as MergeMethod)} />
            </span>
          </Tooltip>
        ) : null}
        <a className="linkButton" href={pr.url} target="_blank" rel="noreferrer">
          Open on GitHub <Icon name="external" size={14} />
          <span className="ds-sr-only"> (opens in a new tab)</span>
        </a>
      </div>
      {blocked && open ? <p className="prNote muted" data-testid="pr-blocked">{blocked}</p> : null}
      {problem ? <p className="prNote prProblem" role="alert" data-testid="pr-problem">{problem}</p> : null}
    </section>
  );
}

function Fact({ tone, glyph, text, action, testId, children }: {
  tone: string;
  glyph: IconName;
  text: ReactNode;
  action?: ReactNode;
  testId?: string;
  children?: ReactNode;
}) {
  return (
    <li data-testid={testId}>
      <span className="prFact">
        <span className="prGlyph" data-tone={tone}><Icon name={glyph} size={14} /></span>
        <span className="prFactText">{text}</span>
        {action}
      </span>
      {children}
    </li>
  );
}


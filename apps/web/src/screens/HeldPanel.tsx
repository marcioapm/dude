/**
 * A delivery held before its pull request: an adopted branch's, or a task
 * on Ask me first. Said where an escalation would be (ReadyForReview): what
 * would be opened, what was checked, and the owner's answers — Open or
 * Draft (the hand-back's confirmation, POST /decider with openPullRequest
 * and draft), Send feedback (the Feedback tab, whose submit sends the work
 * to a fixer), and Done without a pull request (confirmed first). Only the
 * owner is offered them, as the API allows; anyone else reads whom it
 * waits on. While a fix of the owner's feedback runs, the panel says so and
 * Open is refused with how many items are open.
 */

import { useState } from "react";
import { ReadyForReview, ReviewEvidence, type EvidenceChip } from "@dude/design-system/components";
import { Button, Dialog, Icon } from "@dude/design-system";
import { REVIEWER_CATEGORY_LABEL, type ReviewerCategory } from "@dude/domain";
import type { ApiClient, FeedbackItem, Finding, Run, TaskDetail, Verification } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";
import { firstName } from "../beforePullRequest.ts";

/** Whether the task's delivery is held at the pull request under Deliver, for its owner's review. */
export const heldBeforePullRequest = (task: Pick<TaskDetail, "decider" | "awaitingDecision">): boolean =>
  task.decider !== "conductor" && task.awaitingDecision?.point === "before_pull_request";

/** Deliver works on the owner's feedback sent from the hold. */
export const fixingFeedback = (task: Pick<TaskDetail, "decider" | "hold" | "status" | "pullRequestOpened">): boolean =>
  task.decider !== "conductor" && Boolean(task.hold) && task.status === "running" && !task.pullRequestOpened;

/** "2 feedback items still open". */
export const stillOpen = (n: number) => `${n} feedback ${n === 1 ? "item" : "items"} still open`;

const open = (s: FeedbackItem["status"]) => s === "open" || s === "delivered";

export interface HeldPanelProps {
  client: ApiClient;
  task: TaskDetail;
  /** The reader's key; null until known, when nothing is offered. */
  you: string | null;
  /** The attempt's phase Runs, newest first. */
  runs: readonly Run[];
  findings: readonly Finding[];
  verification: Verification | null;
  feedback: readonly FeedbackItem[];
  /** The task's branch preview: open it with commenting, or start it; null when the project has none. */
  preview: { kind: "open"; open: () => void } | { kind: "start"; start: () => void; busy: boolean } | null;
  onFindings: () => void;
  onFiles: () => void;
  onFeedback: () => void;
  onChanges: (repository: string) => void;
  onDone: () => void;
}

export function HeldPanel({ client, task, you, runs, findings, verification, feedback, preview, onFindings, onFiles, onFeedback, onChanges, onDone }: HeldPanelProps) {
  const [busy, setBusy] = useState<"open" | "draft" | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [finishing, setFinishing] = useState(false);
  const hold = task.hold;
  const owner = task.owner ? firstName(task.owner.name) : null;
  const someoneElse = task.owner !== null && you !== null && task.owner.id !== you;
  const openItems = feedback.filter((i) => i.submittedAt !== null && open(i.status)).length;
  const sentBack = fixingFeedback(task);
  const repositories = (hold?.repos ?? []).map((r) => ({ name: r.name, head: r.head, base: r.base, additions: r.additions ?? 0, deletions: r.deletions ?? 0 }));
  if (repositories.length === 0 && task.source) {
    repositories.push({ name: task.source.repository, head: task.source.headBranch, base: task.source.baseBranch, additions: 0, deletions: 0 });
  }

  const answer = async (draft: boolean) => {
    setBusy(draft ? "draft" : "open");
    setProblem(null);
    try {
      await client.setDecider(task.id, "policy", true, draft);
      onDone();
    } catch (err) {
      setProblem(`Could not open the pull request: ${errorText(err)}`);
    } finally {
      setBusy(null);
    }
  };
  const finish = async () => {
    setFinishing(true);
    setProblem(null);
    try {
      await client.doneWithoutPullRequest(task.id);
      setConfirming(false);
      onDone();
    } catch (err) {
      setProblem(`Could not finish it: ${errorText(err)}`);
      setConfirming(false);
    } finally {
      setFinishing(false);
    }
  };

  const reviewers = [...new Set(runs.filter((r) => r.phase === "review" && r.status === "completed").map((r) => r.category).filter(Boolean))] as string[];
  const left = findings.filter((f) => f.status === "open").length;
  const tester = verification && !verification.stale ? verification : null;
  const testerPassed = tester !== null && tester.criteria.every((c) => c.status === "pass" || c.decision !== null);
  const whose = someoneElse && owner ? owner : "you";
  const chips: EvidenceChip[] = [
    ...(reviewers.length > 0 ? [{ id: "reviewers", icon: "check" as const, ok: true, onOpen: onFindings,
      label: <><b>{reviewers.length} {reviewers.length === 1 ? "reviewer" : "reviewers"}</b> passed</>,
      name: `Reviewers passed: ${reviewers.map((c) => REVIEWER_CATEGORY_LABEL[c as ReviewerCategory] ?? c).join(", ")}` }] : []),
    ...(left > 0 ? [{ id: "findings", icon: "list-check" as const, onOpen: onFindings,
      label: <><b>{left} {left === 1 ? "finding" : "findings"}</b> left for {whose}</>, name: `${left} findings left for ${whose}, on Findings` }] : []),
    ...(tester ? [{ id: "tester", icon: "play" as const, ok: testerPassed, onOpen: onFiles,
      label: <><b>Browser test</b> {testerPassed ? "passed" : "settled by a person"}</>, name: `Browser test ${testerPassed ? "passed" : "settled by a person"}, on Files` }] : []),
    ...(preview?.kind === "open" ? [{ id: "preview", icon: "globe" as const, onOpen: preview.open,
      label: <><b>Preview</b> · open with commenting</>, name: "Open the preview with commenting" }] : []),
  ];
  const evidence = (
    <>
      {chips.length > 0 ? <ReviewEvidence chips={chips} /> : null}
      {preview?.kind === "start" && !someoneElse ? (
        <span>
          <Button size="sm" variant="quiet" leadingIcon="play" onClick={preview.start} disabled={preview.busy} loading={preview.busy}>Start preview</Button>
        </span>
      ) : null}
    </>
  );
  const line = task.source && !hold?.round ? <>Review is done on <span className="ds-mono">{task.source.headBranch}</span>. No pull request is open yet.</>
    : <>Review{tester ? ", simplify and the browser test" : " and simplify"} passed. No pull request is open yet.</>;
  const round = hold && hold.round > 1 ? {
    n: hold.round, commitsSince: hold.commitsSince,
    settled: feedback.filter((i) => i.status === "resolved" && i.submittedAt !== null).map((i) => ({ id: i.id, text: i.body, commit: i.resolvedCommit?.slice(0, 7) ?? null })),
  } : undefined;
  const state = sentBack ? { kind: "sent-back" as const, items: openItems, openRefused: stillOpen(openItems) }
    : someoneElse ? { kind: "waiting" as const, on: owner ?? "the owner" }
    : { kind: "yours" as const };
  const heldMs = hold?.since ? Math.max(0, Date.now() - Date.parse(hold.since)) : undefined;

  return (
    <>
      <ReadyForReview
        data-testid="ready-for-review"
        line={line}
        repositories={repositories}
        evidence={evidence}
        state={you === null && !sentBack ? { kind: "waiting", on: owner ?? "the owner" } : state}
        round={round}
        heldMs={heldMs}
        busy={busy}
        onChanges={onChanges}
        onOpen={() => void answer(false)}
        onDraft={() => void answer(true)}
        onFeedback={onFeedback}
        onDoneWithout={() => setConfirming(true)}
      />
      {problem ? <p className="escalationProblem" role="alert">{problem}</p> : null}
      <Dialog
        open={confirming}
        onOpenChange={(o) => !finishing && setConfirming(o)}
        onEscapeKeyDown={(e) => finishing && e.preventDefault()}
        size="sm"
        title="Done without a pull request?"
        footer={
          <>
            <Button variant="quiet" onClick={() => setConfirming(false)} disabled={finishing}>Cancel</Button>
            <Button variant="primary" leadingIcon="check" onClick={() => void finish()} loading={finishing} disabled={finishing}>
              Done without a pull request
            </Button>
          </>
        }
      >
        <ul className="confirmList" data-testid="done-without-confirm">
          <li>
            <Icon name="git-branch" size={14} />
            <span>The branch <code className="ds-mono">{hold?.repos[0]?.head ?? task.source?.headBranch ?? "of this task"}</code> stays on GitHub
              {repositories.length > 0 ? <>, in {repositories.map((r) => r.name).join(" and ")}</> : null}.</span>
          </li>
          <li><Icon name="git-pr" size={14} /><span>No pull request is opened.</span></li>
          <li><Icon name="check" size={14} /><span>{task.key ?? "The task"} moves to Done.</span></li>
        </ul>
      </Dialog>
    </>
  );
}

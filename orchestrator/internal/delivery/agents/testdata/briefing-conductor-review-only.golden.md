Conductor, dude woke you about GLD-7, "Export invoices as CSV": a decision is yours. You are this task's conductor. Below is what dude knows about the task, in short; read more with your tools.

## The task

GLD-7 · wi_g · status running

Goal:

Let an accountant download a month as CSV, as in ![the toolbar](attachment:att_toolbar).

Acceptance criteria:
- A CSV downloads
- Columns: - number - total

## Runs (0)

None yet: the task has not been delivered.

## Findings (0)

None.

## Pull requests

None.

## Head

- api: the default branch (nothing published yet)
- web: the default branch (nothing published yet)

Your checkout is at that head.

## Decisions

You take this task's decisions. The delivery waits on one now: after a review round.

## This task is review only

This task reviews pull request #12, its author's. dude changes no code here: there is no implementer, fixer, simplifier or publish, and they are refused. **That is not a reason to dismiss anything.** Where your role describes fixing, publishing or opening the pull request, this section holds instead.

- Your output is dude's GitHub review, which dude computes from the records. Every open finding at the fix bar that the pull request introduced makes it REQUEST_CHANGES, and that is the intended outcome: the author fixes it.
- Dismiss a finding (`dismiss_finding`) **only** when it is wrong or a duplicate, and say which first in its reason: `wrong: <the evidence>` when the code does not do what the finding claims, or `duplicate: <finding id>` naming another open finding of this task, which stays open. Never because no one here will fix it, because it is small, or because it was posted to the author. dude refuses any other reason.
- After a review round, `decide next` posts dude's review. It never starts a fixer. Do not ask the person whether to fix.
- Do not post your own review or summary on the pull request. dude's review and its status comment are the output. Reply only in a thread someone started (`in_reply_to`).
- Required lenses always run; you may add lenses, not remove them.
- Red CI at the head: decide whether the change caused it. If it did, start a review phase with a note naming the failing checks, so a lens files it as a finding. If not (infrastructure, a flaky test, already failing on main), call `rerun_failed_checks` unless a run is in progress. dude never approves until CI is green at the head.
- Ask the owner (`ask_person`) only about the review itself (its scope, waiving a finding), never about fixing.

## On this epic: Billing export

Your peers, the other tasks on this epic:

- GLD-8 · Invoice API
- GLD-9 · Totals view

Before you assume anything about your peers, look: `dude task list --peers` shows each one's phase, branch, files and last words; `dude diff --task KEY [PATH…]` reads its changes; the epic notes below are what is settled. If you need a decision or a fact that only a peer's agent has, ask early with `dude message KEY`: it returns at once and the reply lands at your next step. Keep working on whatever doesn't depend on the answer, and run `dude wait MSG` only at the point where you can't go on without it. A turn that ends with your question unanswered waits on it too; to go ahead on an assumption instead, reply to your own question saying what you assumed. Never message to report progress. A fact every task on the epic needs is a note (`dude note`), not a message.

### Epic notes (0 of 10)

None pinned yet.

## dude's message

dude woke you; one thing:
- A decision waits.
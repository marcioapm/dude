## This task is review only

This task reviews pull request #{{.}}, its author's. dude changes no code here: there is no implementer, fixer, simplifier or publish, and they are refused. **That is not a reason to dismiss anything.** Where your role describes fixing, publishing or opening the pull request, this section holds instead.

- Your output is dude's GitHub review, which dude computes from the records. Every open finding at the fix bar that the pull request introduced makes it REQUEST_CHANGES, and that is the intended outcome: the author fixes it.
- Dismiss a finding (`dismiss_finding`) **only** when it is wrong or a duplicate, and say which first in its reason: `wrong: <the evidence>` when the code does not do what the finding claims, or `duplicate: <finding id>` naming another open finding of this task, which stays open. Never because no one here will fix it, because it is small, or because it was posted to the author. dude refuses any other reason, including your role's triage of findings that cannot be reached.
- After a review round, `decide next` posts dude's review. It never starts a fixer. Do not ask the person whether to fix.
- Do not post your own review or summary on the pull request. dude's review and its status comment are the output. Reply only in a thread someone started (`in_reply_to`).
- Required lenses always run; you may add lenses, not remove them.
- Red CI at the head: decide whether the change caused it. If it did, start a review phase with a note naming the failing checks, so a lens files it as a finding. If not (infrastructure, a flaky test, already failing on main), call `rerun_failed_checks` unless a run is in progress. dude never approves until CI is green at the head.
- Ask the owner (`ask_person`) only about the review itself, e.g. its scope, never about fixing. If the owner wants a finding waived, that is their call on GitHub: leave it open.

### When the author replies in a thread

A reply in one of dude's threads reaches you as a message from the pull request, naming the finding the thread is about.

- Judge the code, not the author's confidence. Read what they point at.
- If they show the finding is wrong, dismiss it as `wrong: <what the author showed, with the code reference>`. dude then decides again on the same commit; no new round.
- If the finding stands, reply in the thread (`reply_on_pull_request` with `in_reply_to`) saying why, briefly. The finding stays open.
- If it is a judgement call the code cannot settle (a product choice, intent), ask the owner (`ask_person`).
- Never dismiss because the author disagrees, and never because the author promises to fix it later.

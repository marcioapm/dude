---
title: Delivering a task
covers: [settings:delivery, tool:findings, tool:verify]
---
When you press **Deliver**, dude runs a fixed pipeline of agents until the work is an open pull request, or until it needs a person. This page explains each step, which reviewers run, which findings block, and what stops the loop.

## The pipeline

Deliver runs these steps in order:

1. **Implement** — the implementer writes the change and its tests.
2. **Review** — several reviewers read the change at the same time, each for one concern. They report findings.
3. **Fix** — if a finding blocks, the fixer fixes exactly those findings, and review runs again. This repeats until nothing blocks or a limit is reached.
4. **Simplify** — the simplifier tidies the branch without changing what it does. If it changed anything, its diff alone is reviewed again.
5. **Test** — the tester starts the app, uses the change as a person would, records a video, and gives a verdict for each acceptance criterion.
6. **Pull request** — dude opens the pull request on GitHub, then waits on it. Change requests and failing checks send a fixer back. See [Pull requests](help:pull-requests#after-the-pull-request-opens). What happens just before it opens is the project's **Before the pull request** choice: see [Before the pull request](#before-the-pull-request).
7. **Done** — when a person merges the pull request, the task is done. If it is closed without merging, the task is stopped.

Simplify and Test can each be turned off in **Settings → Delivery**. Nothing reaches the tester or the pull request unreviewed.

Each step that writes code (implement, fix, simplify) pushes to a branch of its own, and dude fast-forwards the task's branch to it. Reviewers and the tester never push anything.

## Which reviewers run

Every first review round of a delivery runs the **required reviewers**, plus any **conditional reviewer** whose paths the change touches. By default:

- Required: **Correctness**, **Tests** and **Performance**.
- **Security** joins when a changed path matches `**/auth/**`, `**/payments/**` or `**/*credential*`.
- **Database** joins for `**/migrations/**` or `**/*.sql`.
- **Frontend** joins for `**/*.tsx` or `**/*.css`.
- **API** joins for `**/routes/**` or `**/api/**`.
- **Documentation** never runs by default. Tick it under **Reviewers every delivery runs** (below) to have each round check the repository's product documentation (READMEs, docs, help pages, a changelog if the repository keeps one) against the change: docs the change makes wrong, and user-visible behaviour the repository's own practice would document. Code comments and commit messages are not its concern.

Change the required reviewers in **Settings → Delivery → Reviewers every delivery runs**. At least one must be chosen.

A re-review checks every open finding a fixer already tried. The reviewer of the finding's category judges it; those judged fixed are resolved. That is what lets the loop finish.

A later round (a re-review after a fix, or the review of a pull request's fix or a test's fix) is picked from what changed since the previous review round, not from everything the task has changed. It runs:

- the **reviewers every round runs** (`alwaysReviewers`, by default **Correctness**), even when the round changed only CI or docs;
- the reviewer of each finding it judges;
- each conditional reviewer whose paths the round's own change touches;
- the required reviewers, unless the round changed only **non-product** paths (`nonProductPaths`, by default `.github/**`, `docs/**` and `**/*.md`). These are paths inside a repository: `docs/**` matches a `docs` folder, not a repository named `docs`. To make a whole repository non-product, list its paths.

A round that changed more than 300 lines, or one GitHub cannot measure, runs the first round's reviewers again. A reviewer the conductor or a person names, and a retry of failed reviewers, run as asked. The round's `run.created` event lists why each reviewer ran: `always`, `finding`, `paths`, `required`, `first_round`, `large_round`, `directed` or `retry`. With no reviewer every round runs, a CI-only round with no finding to judge runs no reviewer and the loop goes on as from a clear round. On a pull request's fix being checked, or on a commit the conductor made before the pull request, such a round runs the required reviewers instead (reason `required`): those heads need a completed review before they go on.

Change both under **Settings → Delivery → Reviewers every round runs** and **Not product**.

The default `**/*.md` makes every Markdown file non-product, wherever it is. A project that ships Markdown as behaviour (prompt templates, help pages, content the product renders) should replace the list with one that leaves those out, for example `.github/**`, `docs/**`, `README.md` and `**/README.md`. A glob cannot exclude a path, so the list has to name what is non-product rather than carve out what is not.

## Which findings block

A finding has a severity: Blocking, High, Medium, Low or Note. By default, **Blocking** and **High** findings send the work back to the fixer. Lower severities go into the pull request for a person to weigh.

Change this in **Settings → Delivery → Findings that send it back**. At least one severity must be chosen.

Only open findings block. A finding that is resolved, superseded (its code no longer exists) or accepted by a person never blocks. Open feedback from people always blocks, whatever its severity. See [Questions and feedback](help:questions-and-feedback#feedback-on-a-task).

Agents read a task's findings with `findings`: each one's severity, category, file and line, status, and how it was settled.

## When the loop stops

Every loop ends on a limit. These are set in **Settings → Delivery**, section **Loop**:

| Setting | Default | What it bounds |
| --- | --- | --- |
| Review rounds | 5 | Review → fix cycles before the owner is asked. |
| Attempts per finding | 2 | Fixes one finding may survive before it is raised on its own. |
| Unmet criterion rounds | 2 | Consecutive explicit still verdicts on a blocking finding's acceptance criterion. 0 disables. |
| Regression rounds | 2 | Consecutive fix rounds that resolve blockers but introduce new blockers declared caused by the fix. 0 disables. |
| Pull request fix rounds | 3 | Fixes for pull request feedback, per review round, before the owner is asked. |

Convergence counts only completed, non-replaced reviews of findings sent to both the reviewer and its parent fixer in the current attempt. Each fix is one round, however many reviews judged it: a finding is still for that round if a review said still and none said fixed. A fixed verdict or silence breaks a finding's still streak. Regression rounds group reviews of the same fix; each round must both resolve a blocking finding and raise a blocking finding declared caused by that fix, and the loop stops only while one of the newest round's fix-caused blockers is still open. Blocking severities come from the delivery's policy, not a fixed severity list. Human feedback and tester findings do not count toward these limits.

With no blockers the loop is clear. Otherwise **Not converging** takes precedence over **Review stuck**, which takes precedence over **Review rounds used up**. Findings without acceptance criteria still use the attempts limit. Older deliveries whose frozen policy lacks the convergence limits keep them disabled.

When a limit is reached, delivery stops and asks the task's owner. This is an **escalation**. The task page explains why in a banner, with the choices:

- **Fixes are not converging** — the same named criteria remain unmet after consecutive fixes, or fixes keep causing new blockers. **Fix differently** requires your direction; **Accept the risk** accepts only the named findings with your risk note; **Change the intended behaviour** replaces their criteria with your words and sends them to the fixer. These are owner-only decisions. Stop needs no note; **Try again** on a task stopped here needs your direction, like Fix differently.
- **Review stuck** — the fixer could not resolve a finding after repeated attempts. Trying again, from the banner or after a Stop, requires your direction for the next fix.
- **Review rounds used up** — blocking findings are still open after the last allowed round.
- **No changes made** — the implementer finished without changing anything.
- **A reviewer failed** — try it again, or go on without it.
- **Implementer failed**, **Fixer failed**, **Tester failed** — the agent's Run failed.

See [Why is dude doing this?](help:why-is-dude-doing-this#delivery-stopped-and-asked-me) for what each choice does.

## The tester

The tester is on for every project unless **Browser test** is turned off in **Settings → Delivery**. Say how to start the app in the tester's prompt, or in the project's notes for it.

For each acceptance criterion the tester reports pass, fail or blocked. A pass whose evidence names no file the tester published counts as **unverified**. A criterion it left out counts as **blocked**.

- A **failed** criterion goes back to the fixer, then review, simplify and test again — at most 2 times. Then the owner is asked: **Fix it once more**, or **Accept the failure and go on** with a reason.
- A **blocked** or **unverified** criterion stops at once and asks the owner: **Test again**, or **Waive and go on** with a reason.

Accepting a failure or waiving a check needs a note. The task's owner or an organisation admin can do it.

A test round after a fix whose change is only **non-product** paths (the same **Not product** list) starts no tester when the previous test round passed: every criterion keeps the previous round's verdict, which stands for the new commits. The task does not mark it stale, and the pull request says it was not tested again since. That holds only when the branch still descends from what was tested, no criterion was added, removed or edited since, no finding waits to be judged, and no test was asked for or is due after a failure. A task without acceptance criteria, whose checks the tester derived, is always tested again.

Otherwise a later tester picks up its previous round. It tests again every criterion the change can affect and every one it did not pass or find not applicable, and reports the rest as carried with a reason. dude keeps a carried verdict only if the last one was a pass, not applicable with its reason, or waived or accepted by a person, and only if the criterion reads as it did then; anything else it reports as blocked. A round that changed more than 300 lines, whose branch no longer descends from what was last tested, or that starts a fresh tester, tests every criterion again.

## The checks report

The implementer, fixer and simplifier end their work with a report of the checks they ran. Before their work is published, dude reads it. A missing or malformed report, or a check the agent itself says failed, sends the work back. If the organisation has a judgement tier, dude also asks it whether each reported pass matches the commands the Run actually ran. See [Judgements](help:judgements#what-judgements-are).

An unbacked report is sent back up to **Checks report rounds** times (2 by default, in **Settings → GitHub**). Then the owner is asked: **Send it back once more**, or **Waive and go on** with a reason.

## Verifying a commit

The conductor can have a commit's tests checked without changing it, with `verify`. dude starts a report-only Run that never publishes. It runs the commands given, or the tests for the paths named, and reports each command's exit code. A Run may verify at most 10 times.

## Before the pull request

**Settings → Delivery → Before the pull request** says what Deliver does once the work is reviewed and tested:

- **Open it** (the default) — dude opens the pull request.
- **Open as draft** — dude opens it as a draft. Nobody is asked.
- **Ask me first** — nothing goes to GitHub yet. The task waits for its owner, marked **Ready for your review** on the task page, in Waiting and in the Inbox.

This is a project setting; the organisation has none. A task can override it in its dialog or under **This task** on its Overview. Only the task's owner can change it, and only until its first pull request opens. It applies to the first pull request alone: a later fix round never waits.

While a task waits for your review, its page shows what would be opened, per repository, and what was checked. You can:

- **Open pull request** or **Open as draft**.
- **Write feedback**. Feedback here stays in dude: GitHub's reviewers and CI see none of it. Open feedback sends the work back to a fixer, then review and test again, and the task comes back to you with how many commits are new since you last looked. Open and Draft are refused while any of your feedback is open.
- **Done without a pull request**. The branch is kept and the task is done, marked **Nothing to merge**. Nothing is opened.

Anyone else reads that the task waits on its owner, and can still write feedback. Changing the choice while the task waits takes effect at once: **Open it** or **Open as draft** opens the pull request.

## Other delivery settings

**Settings → Delivery** also holds:

- **Waiting on people** — **Park an agent after** (10 minutes), **Nudge a quiet agent after** (off), **Keep the conductor warm for** (5 minutes). See [Why is dude doing this?](help:why-is-dude-doing-this#an-agent-is-parked-or-idle).
- **Conductor's edits** — **Lines the conductor may change** (60) and **Files the conductor may change** (3) in one publish. See [The conductor](help:conductor#small-edits-by-the-conductor).

A delivery keeps the policy it started with. A change in settings applies to the next delivery, except the waiting and conductor's-edit settings and **Before the pull request**, which always apply as they are now. **Start over** uses the settings as they are now.

Delivery settings are set on the organisation and can be changed for one project, except **Before the pull request**, which is the project's alone. See [Settings](help:settings#organisation-and-project-settings).

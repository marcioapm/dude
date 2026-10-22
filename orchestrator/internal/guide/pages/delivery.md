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
6. **Pull request** — dude opens the pull request on GitHub, then waits on it. Change requests and failing checks send a fixer back. See [Pull requests](help:pull-requests#after-the-pull-request-opens).
7. **Done** — when a person merges the pull request, the task is done. If it is closed without merging, the task is stopped.

Simplify and Test can each be turned off in **Settings → Delivery**. Nothing reaches the tester or the pull request unreviewed.

Each step that writes code (implement, fix, simplify) pushes to a branch of its own, and dude fast-forwards the task's branch to it. Reviewers and the tester never push anything.

## Which reviewers run

Every review round runs the **required reviewers**, plus any **conditional reviewer** whose paths the change touches. By default:

- Required: **Correctness**, **Tests** and **Performance**.
- **Security** joins when a changed path matches `**/auth/**`, `**/payments/**` or `**/*credential*`.
- **Database** joins for `**/migrations/**` or `**/*.sql`.
- **Frontend** joins for `**/*.tsx` or `**/*.css`.
- **API** joins for `**/routes/**` or `**/api/**`.

Change the required reviewers in **Settings → Delivery → Reviewers every delivery runs**. At least one must be chosen.

A re-review checks every open finding a fixer already tried. The reviewer of the finding's category judges it; those judged fixed are resolved. That is what lets the loop finish.

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
| Pull request fix rounds | 3 | Fixes for pull request feedback, per review round, before the owner is asked. |

When a limit is reached, delivery stops and asks the task's owner. This is an **escalation**. The task page explains why in a banner, with the choices:

- **Review stuck** — the fixer could not resolve a finding after repeated attempts.
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

## The checks report

The implementer, fixer and simplifier end their work with a report of the checks they ran. Before their work is published, dude reads it. A missing or malformed report, or a check the agent itself says failed, sends the work back. If the organisation has a judgement tier, dude also asks it whether each reported pass matches the commands the Run actually ran. See [Judgements](help:judgements#what-judgements-are).

An unbacked report is sent back up to **Checks report rounds** times (2 by default, in **Settings → GitHub**). Then the owner is asked: **Send it back once more**, or **Waive and go on** with a reason.

## Verifying a commit

The conductor can have a commit's tests checked without changing it, with `verify`. dude starts a report-only Run that never publishes. It runs the commands given, or the tests for the paths named, and reports each command's exit code. A Run may verify at most 10 times.

## Other delivery settings

**Settings → Delivery** also holds:

- **Waiting on people** — **Park an agent after** (10 minutes), **Nudge a quiet agent after** (off), **Keep the conductor warm for** (5 minutes). See [Why is dude doing this?](help:why-is-dude-doing-this#an-agent-is-parked-or-idle).
- **Conductor's edits** — **Lines the conductor may change** (60) and **Files the conductor may change** (3) in one publish. See [The conductor](help:conductor#small-edits-by-the-conductor).

A delivery keeps the policy it started with. A change in settings applies to the next delivery, except the waiting and conductor's-edit settings, which always apply as they are now. **Start over** uses the settings as they are now.

Delivery settings are set on the organisation and can be changed for one project. See [Settings](help:settings#organisation-and-project-settings).

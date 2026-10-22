---
title: Pull requests
covers: [tool:pull_requests, tool:refresh_pull_requests, tool:reply_on_pull_request, tool:resolve_thread, tool:request_review, tool:mark_ready, tool:rerun_failed_checks, tool:sync_base]
---
dude opens a pull request once the work is reviewed and tested, then keeps working on it until a person merges it. This page explains how the pull request opens, what wakes a fixer, how CI is handled, and how a task keeps up with its base branch.

## How the pull request opens

Under **Deliver**, dude opens the pull request as soon as the last step passes. Under the conductor, it first asks you: Open, Draft, Show me the diff, or Another round. See [The conductor](help:conductor#opening-the-pull-request).

How it opens is set in **Settings → GitHub → Pull requests**:

- **Open pull requests as** — Ready for review (default) or Draft.
- **Request review from** — Nobody, CODEOWNERS (default; GitHub asks the code owners itself) or These people.

The description lists the review and the tester's verdicts. dude refreshes those sections when the branch moves.

A task that changes several repositories gets one pull request per repository.

## After the pull request opens

dude watches the pull request through GitHub's webhooks, and re-reads every open pull request it has not heard about for 15 minutes.

Most of what happens on a pull request only updates what dude shows. Two things send a fixer:

- **A person asks for a change** — a review that requests changes, or a comment that asks for something. Comments from bots, from dude's own login, and plain thanks ("LGTM", "thanks!") never do.
- **A check turns red.** A check that was already red does not wake a fixer again.

Only people allowed by **Who can wake a fixer** (in **Settings → GitHub**) can wake a fixer. Anyone else's comments are shown on the task, not acted on. See [GitHub](help:github#who-can-wake-a-fixer).

A comment that mentions `@dude` is not feedback for a fixer. It goes to the task's conductor as a Chat message, and the conductor answers on the pull request.

## How a fix reaches the pull request

A fix for an open pull request is checked before the pull request shows it. The fixer works on a separate candidate branch. Its change is reviewed, simplified and tested like new work. Only when that passes does dude fast-forward the pull request's branch to it.

If someone pushed to the pull request in the meantime, dude does not move it over their commit. It stops and asks: **The pull request moved**. You can fix the feedback again from what the branch holds now, or wait without it.

After a fix lands, dude asks again every reviewer whose last review was on an older commit. It never re-asks the author or a bot account.

## How many fixes a pull request gets

Two limits bound fixes on a pull request:

- **Pull request fix rounds** (3 by default, **Settings → Delivery**) — fixes per round of review feedback.
- **Fix rounds per pull request** (5 by default, **Settings → GitHub**) — fixes in all, across every review round. 0 means no limit.

A fix that changes nothing in a repository spends none of that pull request's budget. When either limit is reached, the owner is asked: **PR fix rounds used up**. See [Limits](help:limits#fix-rounds-on-pull-requests).

## CI and reruns

If checks stay pending on one commit longer than **Ask when CI is stuck after** (60 minutes by default, **Settings → GitHub**), dude asks a person: **CI is stuck**. A runner may be gone, or a required check never starts.

The conductor can run the failed checks again with `rerun_failed_checks`, once per commit, for a failure that is not the code's — a flaky runner, an outage. A real failure goes to a fixer. It can ask you for one more rerun on the same commit with `request_limit`.

## Merging

dude never merges by itself. Merge on GitHub, or with dude's **Merge** button, which uses **Merge method** from **Settings → GitHub** (Squash by default; also Merge commit or Rebase).

Open feedback from people in dude holds dude's Merge button. A merge on GitHub's own page is not blocked.

When the pull request is merged, the task is **done**. When it is closed without merging, the task is stopped. When a task's pull requests go different ways — some merged, some closed — dude asks you whether the task is done.

The pull request buttons in dude — Merge, Update branch, re-run failed checks, ask for reviewers — are for the task's owner or an organisation admin.

## When the base branch moves ahead

A task keeps up with its base branch by **merging**, never rebasing. What happens is set in **Settings → GitHub → When a task's base moves ahead**:

- **Keep it up to date** (default) — dude merges the base into the task's branch on GitHub and tests the new head. If the merge conflicts, an agent resolves it, the merge is reviewed and tested, and only then does the pull request move.
- **Only tell the task's people** — nothing merges by itself. The pull request card offers **Resolve conflicts**, which only the task's owner can press.

If dude could not resolve a conflict, it asks you: **Could not resolve conflicts**. Try again, or resolve it yourself and wait on it again.

Writing agents (implementer, fixer, simplifier, conductor) can bring the base into their checkout with `sync_base`, then merge it.

## What agents can do on a pull request

- `pull_requests` lists the task's pull requests: state, head, checks, reviews, unresolved threads and people's feedback, as dude last read them.
- `refresh_pull_requests` reads them from GitHub now. Each pull request it reads is one of the task's 60 GitHub reads an hour.
- `reply_on_pull_request` answers a comment as dude's GitHub login. `resolve_thread` resolves a review thread once its request is done.
- The conductor can also `request_review` (repository collaborators only), `mark_ready` a draft (only when you agreed it is ready) and `rerun_failed_checks`.

No agent can merge a pull request or press **Update branch**.

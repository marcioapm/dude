---
title: Limits
covers: [tool:limits, tool:request_limit]
---
Every loop in dude ends on a limit, never on an agent deciding it is finished. This page lists every limit, where it is set, and how an agent can ask you to raise one for a single task.

## Why there are limits

A loop that cannot converge — a fixer that keeps misreading a finding, a flaky check — would otherwise spend money finding that out. When a limit is reached, dude stops and asks a person instead of trying again. The limits are numbers you can read and change in settings, not an agent's judgement.

## Every limit

| Limit | Default | Where it is set |
| --- | --- | --- |
| Review rounds | 5 | Settings → Delivery → Loop |
| Attempts per finding | 2 | Settings → Delivery → Loop |
| Pull request fix rounds (per review round) | 3 | Settings → Delivery → Loop |
| Fix rounds per pull request | 5 (0 is no limit) | Settings → GitHub |
| Checks report rounds | 2 | Settings → GitHub |
| Ask when CI is stuck after | 60 minutes | Settings → GitHub |
| Failed test fix rounds | 2 | dude (fixed) |
| Failed-check reruns | 1 per pull request commit | dude (raised per task with `request_limit`) |
| Lines the conductor may change | 60 per publish | Settings → Delivery → Conductor's edits |
| Files the conductor may change | 3 per publish | Settings → Delivery → Conductor's edits |
| GitHub reads | 60 an hour, per task or session | dude (fixed) |
| Machine moves | 3 per Run | dude (fixed) |
| Verifications | 10 per Run | dude (fixed) |
| Failed-check reruns by one Run | 10 | dude (fixed) |
| Messages to peers | 5 an hour, per Run | dude (fixed) |
| Running time of a phase Run | 4 hours | dude (fixed) |
| Time limit without progress | 2 hours | Settings → Agents → each role |

Delivery settings are the organisation's, and an admin can change them for one project. GitHub settings are the organisation's only.

## Fix rounds on pull requests

A fix for pull request feedback needs room in **both** of two limits:

- **Pull request fix rounds** — fixes in this round of review. It counts from 0 again when a person submits a new review.
- **Fix rounds per pull request** — fixes the pull request has had in all, counted when a fix lands with commits. 0 means no limit here.

Whichever runs out first stops the next fix, and the owner is asked: **PR fix rounds used up**.

## A delivery keeps the limits it started with

When a delivery starts, it keeps a copy of the delivery policy. Changing **Review rounds** or **Pull request fix rounds** in settings affects the next delivery, not the one running. To use the new settings for a task now, press **Start over**.

Some settings are always read as they are now, even for a running delivery: **Park an agent after**, **Nudge a quiet agent after**, **Keep the conductor warm for**, and the two **Conductor's edits** limits. GitHub settings are also read as they are now.

## Seeing a task's limits

Agents with the `limits` tool — the conductor — can see the limits a task's delivery is held to, where each is set, how much is used, and which limit stops the next pull request fix first. It also shows any raise a person granted for the task.

An agent can read the settings themselves with the `settings` tool. See [Settings](help:settings#what-agents-can-read).

## Raising a limit for one task

When the conductor hits a limit and the work needs more, it can ask with `request_limit`. It can ask to raise:

- **perReviewRound** — pull request fix rounds per review round, by at most 10;
- **perPullRequest** — fix rounds per pull request, by at most 10 (only when that limit is not 0);
- **rerun** — one more failed-check rerun on a pull request's current commit.

dude asks the task's people with the conductor's reason and two choices: allow it, or not. The raise applies to this task's delivery only. The setting itself does not change.

## Who can approve a raise

- A raise of **fix rounds** can be approved only by an **organisation admin**, because the settings it overrides are an admin's to change.
- A **rerun** can be approved by the task's **owner** or an **admin**.

The owner can always answer the question in their own words, or decline. An admin who is not the owner can only pick a choice. An approval by someone who may not raise the limit is refused and not recorded.

One request per limit can be open at a time, and it is the conductor's one open question. Each grant, and who made it, shows in the task's history and in `limits`.

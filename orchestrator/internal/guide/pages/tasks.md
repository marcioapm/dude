---
title: Tasks
covers: [tool:create_task, tool:message, tool:wait_for_reply, tool:note]
---
A task is one piece of work dude delivers as a pull request. This page covers writing a task, starting it, relating it to other tasks, and closing, archiving or reopening it.

## Creating a task

Create a task from the board or a project. A task has:

- **Title** — what should change, in one line.
- **Goal** — why it matters, what exists today, and the constraints an agent can't guess. Link issues, designs and logs; paste error output.
- **Acceptance criteria** — one checkable statement per item. Reviewers check every one, and the tester checks each one in a browser.
- **Epic** — optional; puts the task with related work.

The task is not started when you create it. It waits on the board until a person starts it.

Agents can create tasks too, with `create_task`, when they find work outside their own task: a bug, a follow-up, a part to split out. Such a task is never started by the agent. A person reads it and decides. Its goal must be at least 16 characters.

## Good acceptance criteria

Each criterion should be something a person could check by using the change. The tester reports a verdict for each one: pass, fail or blocked. A criterion it cannot show working holds the pull request until it is fixed or a person waives it. See [Delivering a task](help:delivery#the-tester).

Write "the invoice page shows the due date in the customer's time zone", not "handle time zones".

## Starting a task

A task starts in one of two ways:

- **Deliver** — dude's rules run the whole pipeline to a pull request. See [Delivering a task](help:delivery#the-pipeline).
- **Talk it through** — the task's conductor starts and talks with you first. No implementer starts until it says so. See [The conductor](help:conductor#talk-it-through).

Once delivery has started, what the task asks for is fixed: its goal, criteria and images can no longer be edited. You can still move it to another epic. Under the conductor, the conductor can still rewrite the goal and criteria in Chat until an implementer starts.

## Relations between tasks

A task can be **blocked by** another task, **block** another, or be **related** to one.

- A task blocked by another can't start until that task's pull request is merged. Deliver and Talk it through are refused and name the blocker.
- **Start when unblocked** (the task's owner) starts it by itself once every blocker merges.
- **Start anyway** starts it now, with an optional note.
- **Related** gates nothing.

The conductor and an investigator can add relations when they create a task, and the conductor can add or remove its task's relations at any status. An agent can remove only relations its task's agents added. A relation a person added is theirs to remove.

## Tasks in an epic

Agents on the same epic are **peers**. An implementer, the conductor and an investigator can coordinate with them:

- `message` sends a short message to a peer task's agent, delivered at its next step. A Run may send at most 5 messages an hour.
- `wait_for_reply` ends the agent's turn until the reply comes. It parks if no reply comes in time; the reply resumes it.
- `note` pins a short working fact on the epic — a contract, a decision, where the docs are. Every agent on the epic is told at its next step, and every new Run on the epic reads it. An epic has at most 10 current notes, each at most 2,000 characters. Notes are never edited; a new note replaces an old one.

Reviewers, the simplifier and the tester read their peers but never write to them.

## Closing, archiving and reopening

From the task's actions menu:

- **Close** ends a task with a reason: Won't do, Duplicate (of another task) or Can't reproduce. You can add a note.
- **Archive…** takes a task out of every everyday view. A task that has not ended is closed with the reason you give, in the same step.
- **Reopen** and **Unarchive** bring it back.
- **Mark done** finishes work that has nothing to merge, once a person has read it.

Only the task's owner or an organisation admin can close, archive, reopen or unarchive a task. Archiving an epic archives its tasks; each must be yours, or you must be an admin.

## Who owns a task

A task's **owner** is its first active person. Some decisions are the owner's alone: deciding an escalation, answering an agent's question, handing decisions between Deliver and the conductor, picking a stopped task back up, and resolving conflicts. An admin can do some of these too; each page says which. Reassign the task to let someone else decide.

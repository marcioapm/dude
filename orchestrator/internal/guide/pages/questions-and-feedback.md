---
title: Questions and feedback
covers: [tool:ask_person, tool:request_repository, tool:feedback, tool:feedback_reply, tool:feedback_resolve, tool:dismiss_finding, tool:answer_review_question]
---
Agents ask you things, and you tell agents things. This page covers answering an agent's question, deciding a repository request, leaving feedback on a task, accepting review findings, and answering a reviewer's question.

## When an agent asks you a question

An agent that needs a decision only a person can make asks with `ask_person`. It can ask up to 4 questions at once, each with up to 6 suggested answers; you can always answer in your own words too. Its turn ends, and the Run is **waiting** until you answer.

Questions show in the task's Chat and in **Waiting**. On a task, only the task's **owner** can answer. Reassign the task to let someone else answer. In a brainstorm session, any member can answer, unless the agent addressed the question to one member.

While it waits, the agent keeps its machine for **Park an agent after** (10 minutes by default, **Settings → Delivery**). Then it is parked: stopped, with its conversation kept. Your answer resumes it.

A question is cancelled when its Run ends. Answering a cancelled question says so.

## When an agent asks for a repository

An agent can ask for another of the project's repositories with `request_repository` — to read code its work depends on, or (an implementer) to change it too. A repository it changes gets its own pull request.

Only the task's owner can approve or decline. The agent carries on meanwhile if it can. If it cannot go on without the repository, it waits, and your decision resumes it.

## Feedback on a task

You can leave **feedback** on a task directly in dude:

- on the task in general;
- on lines of a Run's diff, in the **Changes** view;
- on a page of a branch preview. See [Servers and previews](help:servers-and-previews#commenting-on-a-preview).

Feedback starts as your **drafts**. Only you can read, change or remove a draft. When you submit, your drafts go together as one batch.

Who hears it first depends on who is driving:

- under the conductor, the conductor;
- else the implementer or fixer still at work, at its next step;
- else the delivery, which starts a fix.

Open feedback always blocks: the review loop, opening the pull request, dude's Merge button and **Mark done** all wait until it is handled, whatever severities the delivery policy blocks on. A merge on GitHub's own page is not blocked.

## How agents handle feedback

Agents read your feedback in your own words with `feedback`. Each item gets one of two answers:

- `feedback_resolve` — fixed by a commit. It holds until the agent's work is published and the commit is found in it. If the Run ends without publishing, the item is reopened.
- `feedback_reply` — not changed, with why: wrong, out of scope, or already so. A reply holds nothing back.

You can reply, resolve or reopen an item yourself. A reopened item is routed again like a new one. The task's **Feedback** section shows what still needs handling and what holds the merge.

## Review findings

Reviewers report **findings**: a severity, a category, a file and line, and a suggested fix. Which severities block is set in **Settings → Delivery**. See [Delivering a task](help:delivery#which-findings-block).

On the task, you can **accept** a finding, with a note. An accepted finding no longer blocks: it records a person's decision to ship it as it is, so the next reader can see it was considered. You can also reopen it.

Under the conductor, the conductor can leave a finding as it is with `dismiss_finding`, giving its reason. The reason shows with the finding.

## A reviewer's question

A reviewer that cannot establish a fact it needs (what the product should do, how something is configured where it runs) asks instead of guessing. Its question shows on the task's Findings tab, above the findings, with why it matters and who can answer it. It is never sent to the fixer.

The task's owner answers it there, or withdraws it with a reason when it no longer matters. Your answer is recorded as a decision on the task, which the fixer reads. The next reviewer of the same kind sees the question and its answer.

**While a reviewer's question is open, dude does not open the pull request**, and does not move a fix onto an open pull request. Review and fixing of the other findings go on. The task waits on you until every question of the attempt is answered or withdrawn.

Under the conductor, the conductor is told of each question. It answers with `answer_review_question` only when it has established the fact itself; otherwise it asks you. Its answer is recorded as the conductor's, not as your decision.

## Escalations

When delivery reaches a limit or a step fails, it stops and asks the owner in a banner on the task. That banner is an **escalation**, not a question: the owner picks one of its actions. Under the conductor, the conductor explains it in Chat and proposes what to do. Picking an offered action decides it as the banner does. Actions requiring a note — retrying a stuck review; fixing differently, accepting the risk or changing intended behaviour when fixes are not converging; accepting a failed test; or waiving a check — are offered only with the conductor's proposed reason or direction in the choice, and picking one records those words as yours. Or answer in your own words with the direction or risk, then the conductor can carry out that decision. Requiring a note does not give admins access to owner-only actions. Changing intended behaviour replaces only the named findings' criteria, not the task's acceptance criteria. See [Why is dude doing this?](help:why-is-dude-doing-this#delivery-stopped-and-asked-me).

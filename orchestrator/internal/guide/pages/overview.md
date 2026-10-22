---
title: What dude is
covers: [tool:list_tasks, tool:list_epics, tool:list_repositories, tool:emit_event, tool:run_chat, tool:run_diff]
---
dude turns a task into a reviewed, tested pull request by running coding agents for you. This page explains the main pieces — projects, tasks, agents and Runs — and who decides what.

## Organisations, projects, epics and tasks

Everything in dude belongs to an **organisation**. Its members share its projects, settings and GitHub connection.

A **project** is a piece of work with one or more GitHub repositories. Each project has a **key** of 2 to 6 letters or digits, starting with a letter (like `BILL`). The key is chosen when the project is made and never changes. No two projects in an organisation have the same key, ignoring case.

An **epic** groups related tasks in a project. Agents working on tasks in the same epic can message each other and pin notes on the epic.

A **task** is one piece of work: a title, a goal and acceptance criteria. Its key is the project's key and a number, like `BILL-12`. See [Tasks](help:tasks#creating-a-task).

## The board

The board shows a project's tasks by status. A task is not started until a person starts it. From then on its card shows whether it is running, waiting on a person, done or stopped.

**Waiting** lists everything that waits on a person: questions from agents, escalations, repository requests and approvals.

## Agents and Runs

An **agent** is a coding agent running in its own container, with its own checkout of the repository. Each agent works in a **role**: Implementer, Reviewer, Fixer, Simplifier, Tester, Investigator, Conductor or Brainstorm.

A **Run** is one agent doing one phase of work, from start to finish. Delivering a task takes several Runs: an implementer, several reviewers, a fixer, a simplifier, a tester. Each Run shows as an agent on the task's page, with its own Chat.

An **attempt** is one go at the whole task on its own branch. **Start over** begins a new attempt: a new branch and the whole pipeline again. **Try again** starts a new Run inside the same attempt. The task page shows one attempt at a time; when there is more than one, pick it in the header.

A phase Run has a hard limit of 4 hours of running time. Past that, it fails. The conductor has no such limit.

## What an agent can see and do

Agents work only through their checkout and dude's own tools. Every tool belongs to a **capability**, and each role has a fixed set of capabilities. An agent can list each role's capabilities with the `settings` tool. See [Settings](help:settings#capabilities).

Every agent with the basic tools can:

- list the project's tasks, epics and repositories (`list_tasks`, `list_epics`, `list_repositories`);
- report progress on its Run, shown in its Chat (`emit_event`);
- read what a Run of its task changed, including another task on its epic (`run_diff`);
- ask for another of the project's repositories (`request_repository`; a person decides).

The conductor and a brainstorm can also read another Run's Chat and the question it waits on (`run_chat`), so they can relay a question to you.

## Who decides what

dude separates **doing** from **deciding**.

- **Agents do the work.** They write code, review it, fix findings and test. A reviewer reports findings; it never decides whether work may go on, and nothing a reviewer does is ever pushed.
- **Rules decide.** When you press **Deliver**, a fixed delivery policy decides each step: which reviewers run, which findings block, how many rounds are allowed. Every loop ends on a limit set in settings, never on an agent saying it is done. See [Delivering a task](help:delivery#the-pipeline).
- **Or the conductor decides.** When you talk to a task in its Chat, or press **Talk it through**, the conductor takes the decisions, within the same limits. See [The conductor](help:conductor#what-the-conductor-is).
- **People decide when the rules run out.** When a loop reaches its limit or something fails, delivery stops and asks the task's owner. This is an **escalation**.

dude never merges a pull request by itself. It stops at an open pull request and waits for a person to merge it, on GitHub or with dude's Merge button.

## Who can do what

Every member of an organisation can create, deliver and steer tasks. Admins also manage the organisation: its members, invitations and settings. Some decisions belong to a task's **owner**: its first active person. See [Accounts](help:accounts#members-and-admins).

## Where to go next

- [Tasks](help:tasks#creating-a-task) — writing a good task.
- [Delivering a task](help:delivery#the-pipeline) — what happens after Deliver.
- [Why is dude doing this?](help:why-is-dude-doing-this#delivery-stopped-and-asked-me) — common questions.

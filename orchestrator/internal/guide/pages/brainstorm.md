---
title: Brainstorm sessions
covers: [tool:propose, tool:relate, tool:name_session, tool:refresh_checkout, tool:chat_history]
---
A brainstorm session is a conversation with an agent about your projects, before any work is planned. This page covers starting a session, linking projects, sharing it, filing what it proposes, and the investigators it can start.

## What a session is for

Use a session to think something through: where a change would live, what the smallest useful first slice is, what already exists, what could go wrong. The brainstorm agent reads your code and dude's records and brings its own view.

The brainstorm agent never changes code and never creates anything itself. When you agree on work, it puts it on a **proposal card** for a member to file.

## Starting a session

Start a session with **New session** on the **Sessions** screen, or from the welcome page, with a first message. You can link projects at once, and choose the session's model tier and harness. Without a choice, the session follows the organisation's **Brainstorm** role settings.

The agent names the session once its subject is clear (`name_session`). Once a member names it, the agent can no longer rename it.

## Linked projects

A session works across the projects linked to it. Each linked project's repositories are checked out for the agent, read only, at their default branches.

Only the session's owner links projects. The agent can propose linking another of the organisation's projects; the owner confirms, and its repositories are checked out at the agent's next turn.

If a checkout failed, the agent can ask for it again with `refresh_checkout` and see why it failed last — at most 3 times a Run.

## Sharing a session

A session belongs to its owner and the members they share it with. Each member can **chat** (write to the agent) or **read** (follow, but write nothing). An organisation admin who is not in a session cannot open it.

Only the owner shares the session, changes members' roles, removes members and hands the session over to someone else. When handing over, the owner chooses to keep chatting, keep reading, or leave.

Several people can write at once. The agent answers each by name. It can put a question to one member; then only that member can answer it.

## The proposal card

When work is agreed, the agent fills the session's proposal card with `propose`:

- new epics and tasks in linked projects, with goals and acceptance criteria;
- edits to an existing task's goal or criteria, shown before and after;
- comments on a task;
- notes for an epic;
- what each new task waits on and holds up.

With `relate`, it can propose a relation between two existing tasks: one blocks the other, or they are related.

A member who can chat files the items they keep, as themselves. Readers can't file. A new proposal replaces the card. Filed tasks are not started: they wait on the board like any other.

## Investigators

The brainstorm agent can start **investigator** agents with `start_agent` to research the linked repositories in more depth. An investigator is read only, has no project secrets and cannot start another agent. It may publish report files to the session's **Files**.

The brainstorm agent and its investigators can also publish documents — a design note, a diagram, a table — to the session's Files.

## What a brainstorm can read

Besides the checkouts, the brainstorm agent can:

- search memories, tasks and epics in linked projects and the organisation's memories (`search_memory`), and remember things for the session;
- read linked projects' tasks, epics, repositories, findings and pull requests;
- read GitHub issues, pull requests, Actions runs' jobs and check logs;
- read another Run's Chat, for the Runs the session started (`run_chat`);
- for a session started from chat, read more of the conversation it came from: its thread, or the channel when the request was not in one, 30 messages a page, at most 30 reads an hour (`chat_history`);
- read dude's settings as they apply to the session (`settings`).

A session started from chat never sees a project an admin hid from that chat (on the chat provider's settings page): it is not offered, cannot be linked, and every tool refuses it.

Its machine is small and does not run code. What needs running to be known, it says is not checked, and proposes a task that finds out.

## The web

By default a brainstorm reaches only the hosts in **Settings → Network**. Turn on **Brainstorm sessions** there to let it read documentation, competitors' pages and anything else it finds while it researches. Other agents keep the list. See [Network](help:network#brainstorm-sessions).

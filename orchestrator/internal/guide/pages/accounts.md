---
title: Accounts and organisations
covers: []
---
This page covers organisations, members and admins, invitations, and your own settings.

## Organisations

An organisation is where your projects, settings and GitHub connection live. You can belong to several. **Your settings → Organisations** lists yours; switch between them there or in the app's menu.

Everything one organisation holds is separate from every other's.

## Members and admins

Each person in an organisation is a **member** or an **admin**.

- **Every member** can create, deliver and steer tasks, start brainstorm sessions, search and add memories, and see the settings pages except Invitations, which only admins see.
- **Admins** also manage the organisation: its members, invitations and settings — GitHub, Agents, Network, Models, Judgements, Machines, Images, Delivery, and each project's settings. Only an admin creates a project.

There is no project-level admin: changing a project's settings takes an organisation admin.

An organisation always has at least one admin. To remove the last admin, or make them a member, first make someone else an admin.

## Task owners

A task's **owner** is its first active person. Some decisions on a task are the owner's:

| Decision | Who |
| --- | --- |
| Answer an agent's question | Owner |
| Approve or decline a repository request | Owner |
| Decide an escalation | Owner |
| Accept a failed test, waive what could not be checked, or waive an unbacked checks report (with a reason) | Owner or admin |
| Hand decisions between Deliver and the conductor | Owner |
| Resolve conflicts on a pull request | Owner |
| Restart or leave a stalled agent | Owner |
| Pick a stopped task back up | Owner |
| Merge, update the branch, re-run failed checks, ask for reviewers (dude's pull request buttons) | Owner or admin |
| Close, archive, reopen, unarchive | Owner or admin |
| Approve a conductor's request for more fix rounds | Admin |
| Approve a conductor's request for one more rerun | Owner or admin |

Reassign a task to let someone else make the owner's decisions.

## Invitations

An admin invites people from **Members** or **Invitations** with **Invite people**, as members or admins. An invitation link works only for the address it was sent to, for 14 days. **Renew** gives an expired one 14 more days.

**Invitations** lists every invitation and what became of it, and shows how many are pending.

## Your settings

**Your settings** are yours, not the organisation's:

- **Profile** — your name and photo.
- **Keys** — API keys for using dude from scripts.
- **Organisations** — the ones you belong to.
- **GitHub authorization** — connect your GitHub account, when the organisation uses a GitHub App.
- **Appearance** — how dude looks in this browser.
- **Notifications** — whether this browser shows a notification when a question or request from an agent waits on you, per organisation. Waiting work always shows in dude too.

## Online and last seen

dude shows a person as online when they used dude in the last five minutes: a visible, focused tab with some input. API keys never count as being online.

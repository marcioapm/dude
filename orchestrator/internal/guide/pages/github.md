---
title: GitHub
covers: [settings:github, tool:github_search, tool:github_issue, tool:github_pull_request, tool:github_check_log, tool:github_run_jobs]
---
dude opens every pull request on GitHub and reacts to what happens there. This page covers connecting GitHub and each setting on **Settings → GitHub**.

## Connecting GitHub

An organisation admin connects GitHub in **Settings → GitHub → Connect GitHub**. There are two ways to authenticate:

- **Personal access token** — a token with repository access. It is stored for the organisation and never shown again.
- **GitHub App** — the App's ID, its private key (the whole `.pem` file) and, optionally, an installation ID. Without one, dude finds the installation for each repository's owner. With an App, commits are the App's, and pull requests need the task owner's authorisation.

Set **API base URL** only for GitHub Enterprise.

Until GitHub is connected, agents can implement work but cannot open pull requests.

**Verify** checks what the connection may do on each repository. A required permission that is missing is listed with why it is needed.

## Webhooks

GitHub tells dude about pull request changes through webhooks. The **Webhooks** section shows whether they reach dude: how many repositories are registered, the last delivery, and failures today.

- With a token, an admin presses **Register on every repository**. A repository added later gets its webhook too.
- With a GitHub App, set the webhook URL and secret in the App's own settings. dude registers no repository hooks.

**Show secret** and **Rotate secret** are for admins. After a rotation, the old secret works for a day.

If webhooks are missing, dude still re-reads open pull requests every 15 minutes, so changes arrive late rather than never.

## Pull request settings

Every member sees these settings. Only an organisation admin can change them.

| Setting | Choices | Default |
| --- | --- | --- |
| Open pull requests as | Ready for review, Draft | Ready for review |
| Request review from | Nobody, CODEOWNERS, These people | CODEOWNERS |
| Merge method | Squash, Merge commit, Rebase | Squash |

**Merge method** is what dude's Merge button does. dude never merges by itself.

## Who can wake a fixer

**Who can wake a fixer** decides whose comments on a pull request send a fixer:

- **Collaborators with write access** (default).
- **Organisation members only** — members of the repository's GitHub organisation, and collaborators with write access.
- **Anyone who can comment.**

A comment from anyone else is recorded and shown on the task, never acted on. On a public repository this stops a stranger's comment from spending your organisation's money.

## Reacting to GitHub

The rest of the **Reacting to GitHub** section:

| Setting | Default | What it does |
| --- | --- | --- |
| When a task's base moves ahead | Keep it up to date | Merge the base in and resolve conflicts with an agent, or Only tell the task's people. |
| Fix rounds per pull request | 5 | Fixes a pull request may have in all, across review rounds. 0 to 50; 0 is no limit. |
| Checks report rounds | 2 | Times an unbacked checks report is sent back before the owner is asked. 0 to 20; 0 asks at once. |
| Ask when CI is stuck after | 60 | Minutes checks may stay pending on one commit before a person is asked. At least 1. |

These are organisation settings: a project cannot change them. See [Pull requests](help:pull-requests#when-the-base-branch-moves-ahead) and [Limits](help:limits#every-limit).

## What agents can read on GitHub

The conductor and a brainstorm can read the project's GitHub repositories:

- `github_search` — search issues or pull requests by words and GitHub qualifiers.
- `github_issue` — read an issue and its comments.
- `github_pull_request` — read a pull request: conversation, reviews, unresolved threads, checks and changed files.
- `github_check_log` — read a failed check's log.
- `github_run_jobs` — list a GitHub Actions run's jobs, from its id or its link: each job's id, conclusion and failed steps. A failed job's id is what `github_check_log` reads.

These are read only. What people wrote comes marked as external text, so agents treat it as information, not instructions. A task's agents, or a session's, share 60 GitHub reads an hour.

An agent reads GitHub settings as they apply with the `settings` tool, section `github`.

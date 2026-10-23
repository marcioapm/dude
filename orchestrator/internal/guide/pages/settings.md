---
title: Settings
covers: [tool:settings, settings:roles, settings:prompts, settings:capabilities]
---
dude's settings live on the organisation, and a project can change most of them for itself. This page lists each settings page, explains where a value comes from, and covers agent roles, prompts and capabilities.

## Where settings are

There are three places:

- **Organisation settings** — Members, Invitations, General, GitHub, Agents (one page per role), Network, Models, Judgements, Machines, Images, Delivery and Memory.
- **Project settings** — General, Repositories, Servers, Network, Agents, Delivery and Memory.
- **Your settings** — yours, not the organisation's: your profile and keys, your organisations, GitHub authorization, appearance and notifications.

Every member can see the organisation's and projects' settings, except Invitations, which only admins see. Only an organisation admin can change them.

## Organisation and project settings

The organisation's values are the defaults every project starts from. On a project's settings, each value says where it comes from:

- **From** the organisation's name — the project follows the organisation.
- **Overridden** — the project set its own. **Reset** puts the organisation's value back.

A project can change its agent roles, its delivery policy, its network list, its servers and its runtime image. GitHub, Models, Judgements, Machines and Images belong to the organisation alone.

Where neither sets a value, dude's built-in default applies.

## What each page controls

| Page | What it controls | See |
| --- | --- | --- |
| Members, Invitations | Who is in the organisation, and who is an admin | [Accounts](help:accounts#members-and-admins) |
| General | The organisation's or project's name; a project's key and runtime image | — |
| Repositories (project) | What agents check out. The first is where tasks start | — |
| GitHub | The connection, webhooks, and how dude behaves on GitHub | [GitHub](help:github#pull-request-settings) |
| Agents | Each role's harness, model, time limit, machine, image and prompt | below |
| Network | Hosts agents may reach | [Network](help:network#what-agents-may-reach) |
| Models | The model tiers agents run on | [Models and tiers](help:models-and-tiers#what-a-tier-is) |
| Judgements | Which tier answers dude's one-question decisions | [Judgements](help:judgements#what-judgements-are) |
| Machines | The machine sizes agents can run on | [Machines and images](help:machines-and-images#machine-sizes) |
| Images | The container images agents work in | [Machines and images](help:machines-and-images#images) |
| Delivery | Reviewers, blocking severities, loop limits, simplify, test, waiting and conductor's edits | [Delivering a task](help:delivery#the-pipeline) |
| Servers (project) | Server definitions, branch previews and project secrets | [Servers and previews](help:servers-and-previews#server-definitions) |
| Memory | Search, Memories and Index | [Memory](help:memory#what-memory-is) |

## Delivery policy

**Delivery → Loop** includes **Unmet criterion rounds** (`maxStillRounds`) and **Regression rounds** (`maxRegressionRounds`). Both default to 2 and accept whole numbers from 0 to 20; 0 disables that convergence limit. They follow the organisation/project inheritance and Reset rules above. A delivery freezes these limits when it starts; older frozen policies without them keep both off. See [When the loop stops](help:delivery#when-the-loop-stops) for how the rounds count.

Older deliveries are not changed by enabling a bound now; **Start over** reads today's policy. Reaching a convergence bound asks for the owner's direction rather than starting another undirected fixer.

## Agent roles

**Agents** has a page for each role: Conductor, Brainstorm, Investigator, Implementer, Reviewer, Fixer, Simplifier and Tester. Each page sets:

- **Harness** — the coding agent that runs it: OpenCode (default), Claude Code or Codex.
- **Model** — the model tier it asks for. See [Models and tiers](help:models-and-tiers#what-a-tier-is).
- **Time limit without progress** — after this long without progress, the owner is told. 2 hours by default, at least 30 minutes. The conductor has none. Separately, phase Runs are stopped at 4 hours.
- **Machine** — its machine size.
- **Image** — what it works in, over the project's runtime image.
- **Prompt** — its saved prompt, below.

The Simplifier and Tester each have an on/off switch, the same as **Simplify pass** and **Browser test** in Delivery. The other roles are always on.

The Fixer runs on the Implementer's harness, tier, time limit, machine and image unless you give it its own.

## Prompts

Each role has dude's built-in prompt. A saved prompt replaces only the role's "How you work as …" section. The rest is always kept: how it works, how dude works, the role's own sections, a reviewer's aspect, and what the role hands back.

- On the organisation, the saved prompt replaces dude's stance for that role.
- On a project, choose **Add to** the organisation's (it comes after), **Replace** the organisation's, or **Use** the organisation's.

Every save is kept. **History** shows each version, who saved it, the Runs that used it, and lets an admin restore one. A Run keeps the prompt it started with.

A prompt can use `{{task.title}}`, `{{task.goal}}`, `{{task.criteria}}`, `{{run.branch}}` and `{{run.base_ref}}`.

## Capabilities

Every tool an agent has belongs to a **capability** — a named grant such as "Search the project's memory" or "Drive the task's delivery". Each role has a fixed set. For example, reviewers, the simplifier and the tester read their peers but never message them, and only the conductor can start phases, publish or act on pull requests.

There is no settings page to change them. An agent can list each role's capabilities with `settings`, section `capabilities`.

## Project secrets

A project's **Secrets** (under **Servers**) are environment variables given only to the previews and agent roles each one names. They are write-only: dude shows a secret's name, who gets it and its last characters, never its value. Secrets never reach the conductor or a brainstorm.

## What agents can read

Agents with the `settings` tool — the conductor and a brainstorm — read dude's settings as they apply to them, each value with where it is set: dude's default, the organisation, or the project. They can ask for one section: `delivery`, `roles`, `prompts`, `github`, `network`, `capabilities`, `machines`, `models`, `judgements`, `servers`, `memory` or `images`.

Agents never see credentials, secret values or prompt text. Given a task, `settings` also shows what that task's delivery is held to, any raises people granted, and which reviewers its changed paths pick.

So you can ask the conductor "why did the security reviewer run?" or "how many review rounds does this project allow?" and it answers from the settings themselves. Settings are changed only by a person, in Settings.

---
title: Settings
covers: [tool:settings, settings:delivery, settings:roles, settings:prompts, settings:github, settings:network, settings:capabilities, settings:machines, settings:models, settings:judgements, settings:servers, settings:memory, settings:images]
---
dude's settings decide how it delivers work, which agents it runs and what they may reach. Most are set for the organisation and can be set again for a project; each value shows where it is set: dude (the built-in default), the organisation, or the project. People change settings in Settings; agents read them with the `settings` tool and change nothing.

## Delivery

The delivery policy: which reviewers review every change and which path globs add one, which finding severities block, how many review and fix rounds a delivery may take, whether the simplifier and the tester run, after how many minutes a Run waiting on a person is parked, when an idle one is nudged, and how large a publish the conductor may make. A task's delivery keeps the policy it started under; the settings tool shows that frozen policy for a task where it differs from today's.

## Roles

Each agent role (conductor, implementer, reviewer, fixer, simplifier, tester, investigator, brainstorm) has a model tier, a harness, a time limit, a machine size and an image, and may have notes that are added to its instructions.

## Prompts

A role's built-in prompt can be replaced by the organisation, and added to or replaced by a project. Settings shows whether each is built-in or saved, and when and by whom it was saved.

## GitHub

How dude reaches the project's repositories on GitHub: the connection it uses and its repositories.

## Network

What agents may reach: the organisation's and the project's egress lists, and which roles may search and read the web.

## Capabilities

Each role's tools, as named grants: a capability unlocks a set of dude's tools or the agent's own web tools.

## Machines

The machine sizes the organisation offers and which size each role and a project's previews run on.

## Models

The organisation's model tiers. A role runs on a tier, never a model: a tier names the model dude asks the LLM proxy for and its reasoning effort.

## Judgements

The tier dude asks one-question judgements of (for example whether reported checks match the commands a Run ran), or Off.

## Servers

A project's server recipes (a port, a command, a workdir and a setup step) that a person can start on a task's Run, and its branch preview settings.

## Memory

How search embeds text for meaning: the embedding model and its dimensions, configured by whoever runs dude, not per organisation. Without embeddings, search works by words.

## Images

The organisation's image library: the images agents and previews run in, which is the default, and how each is built.
